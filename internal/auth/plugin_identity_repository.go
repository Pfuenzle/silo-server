package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPluginIdentityOwnershipConflict = errors.New("plugin identity ownership conflict")

type PluginIdentityOwnershipConflictError struct {
	InstallationID  int
	ExternalSubject string
	OwnerUserID     int
}

func (e *PluginIdentityOwnershipConflictError) Error() string {
	return fmt.Sprintf(
		"plugin identity %d/%q is already owned by user %d",
		e.InstallationID,
		e.ExternalSubject,
		e.OwnerUserID,
	)
}

func (e *PluginIdentityOwnershipConflictError) Is(target error) bool {
	return target == ErrPluginIdentityOwnershipConflict
}

func (*PluginIdentityOwnershipConflictError) IdentityOwnershipConflict() {}

type PluginIdentityRepository struct {
	pool           *pgxpool.Pool
	claim          func(context.Context, pgx.Tx, PluginIdentityKey, int) error
	commit         func(context.Context, pgx.Tx) error
	runTransaction func(context.Context, func(context.Context, pgx.Tx) error) error
}

type PluginIdentityKey struct {
	InstallationID  int
	ExternalSubject string
}

func NewPluginIdentityRepository(pool *pgxpool.Pool) *PluginIdentityRepository {
	return &PluginIdentityRepository{pool: pool}
}

func (r *PluginIdentityRepository) Lookup(ctx context.Context, key PluginIdentityKey) (int, error) {
	return lookupPluginIdentity(ctx, r.pool, key)
}

func lookupPluginIdentity(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key PluginIdentityKey) (int, error) {
	var userID int
	err := queryer.QueryRow(ctx, `
		SELECT user_id
		FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2`,
		key.InstallationID,
		key.ExternalSubject,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lookup plugin auth identity: %w", err)
	}
	return userID, nil
}

func (r *PluginIdentityRepository) Claim(ctx context.Context, key PluginIdentityKey, userID int) error {
	return claimPluginIdentity(ctx, r.pool, key, userID)
}

func (r *PluginIdentityRepository) claimInTransaction(ctx context.Context, tx pgx.Tx, key PluginIdentityKey, userID int) error {
	if r.claim != nil {
		return r.claim(ctx, tx, key, userID)
	}
	return claimPluginIdentity(ctx, tx, key, userID)
}

func claimPluginIdentity(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key PluginIdentityKey, userID int) error {
	var insertedUserID int
	err := queryer.QueryRow(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, external_subject) DO NOTHING
		RETURNING user_id`,
		key.InstallationID,
		key.ExternalSubject,
		userID,
	).Scan(&insertedUserID)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return fmt.Errorf("insert plugin auth identity: %w", err)
		}
		return nil
	}

	ownerUserID, err := lookupPluginIdentity(ctx, queryer, key)
	if err != nil {
		return fmt.Errorf("load conflicting plugin auth identity: %w", err)
	}
	if ownerUserID == userID {
		return nil
	}
	return &PluginIdentityOwnershipConflictError{
		InstallationID:  key.InstallationID,
		ExternalSubject: key.ExternalSubject,
		OwnerUserID:     ownerUserID,
	}
}

func (r *PluginIdentityRepository) withProvisioningTransaction(
	ctx context.Context,
	key PluginIdentityKey,
	fn func(context.Context, pgx.Tx) error,
) error {
	return r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pluginIdentityLockKey(key)); err != nil {
			return fmt.Errorf("acquire plugin identity lock: %w", err)
		}
		return fn(ctx, tx)
	})
}

func (r *PluginIdentityRepository) withTransaction(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if r.runTransaction != nil {
		return r.runTransaction(ctx, fn)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		cleanupCtx, cancelCleanup := detachedCleanupContext(ctx)
		defer cancelCleanup()
		_ = tx.Rollback(cleanupCtx)
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	commit := r.commit
	if commit == nil {
		commit = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
	}
	if err := commit(ctx, tx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func pluginIdentityLockKey(key PluginIdentityKey) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", key.InstallationID, key.ExternalSubject)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
