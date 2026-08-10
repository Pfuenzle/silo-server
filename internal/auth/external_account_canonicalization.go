package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var (
	ErrCanonicalizationForbidden = errors.New("external account canonicalization is forbidden")
	ErrCanonicalizationBlocked   = errors.New("external account canonicalization is blocked")
	ErrCanonicalizationStale     = errors.New("external account canonicalization preview is stale")
)

const canonicalizationPreviewLifetime = 5 * time.Minute

type CanonicalizationOperator struct {
	UserID  int
	IsAdmin bool
}

type CanonicalizationPreview struct {
	Token               string
	SourceFingerprint   string
	TargetFingerprint   string
	IdentityFingerprint string
	Dependencies        map[string]int
	FavoritesSupported  int
	InterestsSupported  int
	Blockers            []string
	ExpiresAt           time.Time
}

type CanonicalizationReceipt struct {
	SourceDeleted        bool
	IdentityOwner        int
	FavoritesTransferred int
	InterestsTransferred int
}

type ExternalAccountCanonicalizer struct {
	pool          *pgxpool.Pool
	key           []byte
	now           func() time.Time
	storeProvider userstore.UserStoreProvider
}

// WithStoreProvider makes canonicalization prove separately-owned user-store
// state before it can delete an account.
func (s *ExternalAccountCanonicalizer) WithStoreProvider(provider userstore.UserStoreProvider) *ExternalAccountCanonicalizer {
	s.storeProvider = provider
	return s
}

func NewExternalAccountCanonicalizer(pool *pgxpool.Pool, previewKey []byte, now func() time.Time) *ExternalAccountCanonicalizer {
	if now == nil {
		now = time.Now
	}
	return &ExternalAccountCanonicalizer{pool: pool, key: append([]byte(nil), previewKey...), now: now}
}

type canonicalizationToken struct {
	ActorID             int       `json:"actor_id"`
	Source              int       `json:"source"`
	Target              int       `json:"target"`
	InstallationID      int       `json:"installation_id"`
	CapabilityID        string    `json:"capability_id"`
	IdentityFingerprint string    `json:"identity_fingerprint"`
	Digest              string    `json:"digest"`
	Expires             time.Time `json:"expires"`
}

func (s *ExternalAccountCanonicalizer) Preview(ctx context.Context, actor CanonicalizationOperator, sourceID, targetID int) (CanonicalizationPreview, error) {
	if err := s.authorize(actor, sourceID, targetID); err != nil {
		return CanonicalizationPreview{}, err
	}
	snapshot, err := s.snapshot(ctx, s.pool, sourceID, targetID, false)
	if err != nil {
		return CanonicalizationPreview{}, err
	}
	s.checkStoreState(ctx, sourceID, &snapshot)
	expires := s.now().UTC().Add(canonicalizationPreviewLifetime)
	token, err := s.sign(canonicalizationToken{
		ActorID: actor.UserID, Source: sourceID, Target: targetID,
		InstallationID: snapshot.installationID, CapabilityID: snapshot.capabilityID,
		IdentityFingerprint: snapshot.identityFingerprint, Digest: snapshot.digest(), Expires: expires,
	})
	if err != nil {
		return CanonicalizationPreview{}, fmt.Errorf("sign canonicalization preview: %w", err)
	}
	return CanonicalizationPreview{
		Token: token, SourceFingerprint: snapshot.sourceFingerprint, TargetFingerprint: snapshot.targetFingerprint,
		IdentityFingerprint: snapshot.identityFingerprint, Dependencies: snapshot.dependencies,
		FavoritesSupported: snapshot.dependencies["user_favorites"], InterestsSupported: snapshot.dependencies["profile_series_interest"],
		Blockers: snapshot.blockers, ExpiresAt: expires,
	}, nil
}

func (s *ExternalAccountCanonicalizer) Execute(ctx context.Context, actor CanonicalizationOperator, previewToken string) (CanonicalizationReceipt, error) {
	token, err := s.verify(previewToken)
	if err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := s.authorize(actor, token.Source, token.Target); err != nil {
		return CanonicalizationReceipt{}, err
	}
	if actor.UserID != token.ActorID || actor.UserID != token.Target || !token.Expires.After(s.now().UTC()) {
		return CanonicalizationReceipt{}, ErrCanonicalizationStale
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("begin canonicalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCanonicalizationDependencies(ctx, tx, token); err != nil {
		if canonicalizationRaceError(err) {
			return CanonicalizationReceipt{}, ErrCanonicalizationStale
		}
		if errors.Is(err, ErrNotFound) {
			return s.replayReceipt(ctx, tx, token)
		}
		return CanonicalizationReceipt{}, err
	}
	snapshot, err := s.snapshot(ctx, tx, token.Source, token.Target, true)
	if errors.Is(err, ErrNotFound) {
		return s.replayReceipt(ctx, tx, token)
	}
	if err != nil {
		return CanonicalizationReceipt{}, err
	}
	s.checkStoreState(ctx, token.Source, &snapshot)
	if snapshot.digest() != token.Digest {
		return CanonicalizationReceipt{}, ErrCanonicalizationStale
	}
	if len(snapshot.blockers) != 0 {
		return CanonicalizationReceipt{}, fmt.Errorf("%w: %s", ErrCanonicalizationBlocked, strings.Join(snapshot.blockers, ","))
	}
	receipt, err := s.apply(ctx, tx, snapshot)
	if err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("commit canonicalization: %w", err)
	}
	return receipt, nil
}

func canonicalizationRaceError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

func (s *ExternalAccountCanonicalizer) checkStoreState(ctx context.Context, sourceID int, snapshot *canonicalizationSnapshot) {
	provider, ok := s.storeProvider.(userstore.CanonicalizationStoreStateProvider)
	if !ok {
		snapshot.blockers = append(snapshot.blockers, "user_store_unproven")
		snapshot.dependencies["user_store_unproven"]++
		snapshot.sortBlockers()
		return
	}
	state, err := provider.CanonicalizationStoreState(ctx, sourceID)
	if err != nil || !state.Proven || !state.Empty || !state.SharedTransaction || state.Kind != userstore.CanonicalizationStorePostgres {
		snapshot.blockers = append(snapshot.blockers, "user_store_nonempty")
		snapshot.dependencies["user_store_nonempty"]++
		snapshot.sortBlockers()
	}
}

func (s *ExternalAccountCanonicalizer) authorize(actor CanonicalizationOperator, sourceID, targetID int) error {
	if len(s.key) == 0 || !actor.IsAdmin || actor.UserID <= 0 || sourceID <= 0 || targetID <= 0 || sourceID == targetID {
		return ErrCanonicalizationForbidden
	}
	return nil
}

func (s *ExternalAccountCanonicalizer) sign(payload canonicalizationToken) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *ExternalAccountCanonicalizer) verify(token string) (canonicalizationToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return canonicalizationToken{}, ErrCanonicalizationStale
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return canonicalizationToken{}, ErrCanonicalizationStale
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return canonicalizationToken{}, ErrCanonicalizationStale
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return canonicalizationToken{}, ErrCanonicalizationStale
	}
	var payload canonicalizationToken
	if err := json.Unmarshal(body, &payload); err != nil {
		return canonicalizationToken{}, ErrCanonicalizationStale
	}
	return payload, nil
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
