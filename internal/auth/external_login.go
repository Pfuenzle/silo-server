package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const externalLoginAttempts = 3

var ErrTrustedLinkOwnershipConflict = errors.New("trusted link ownership conflict: another subject already linked for this installation/capability/username")

type TrustedLinkOwnershipConflictError struct {
	InstallationID  int
	CapabilityID    string
	ExistingUserID  int
	ExternalSubject string
}

func (e *TrustedLinkOwnershipConflictError) Error() string {
	return fmt.Sprintf(
		"trusted link conflict: installation %d/%s username already linked to user %d by a different subject",
		e.InstallationID, e.CapabilityID, e.ExistingUserID,
	)
}

func (e *TrustedLinkOwnershipConflictError) Is(target error) bool {
	return target == ErrTrustedLinkOwnershipConflict
}

func (*TrustedLinkOwnershipConflictError) TrustedLinkOwnershipConflict() {}

type preparedExternalAccount struct {
	usernameBase    string
	email           string
	passwordHash    string
	externalSubject string
}

func (p *PluginProvider) completeExternalLogin(ctx context.Context, creds Credentials, response *pluginv1.AuthenticateResponse, session *models.AuthSession) (*models.User, error) {
	// Unbound legacy plugin providers do not participate in external authorization.
	// Keep their established compensation path intact; LDAP/OIDC bindings always
	// carry a capability ID and use the atomic path below.
	if strings.TrimSpace(p.config.CapabilityID) == "" {
		user, err := p.provisionIdentity(ctx, creds, response)
		if err != nil {
			return nil, err
		}
		return p.completePluginLogin(ctx, user, response.GetClaims(), session)
	}
	prepared, err := p.prepareExternalAccount(creds, response)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < externalLoginAttempts; attempt++ {
		user, err := p.completeExternalLoginAttempt(ctx, response, session, prepared)
		if err == nil || !isRetryableExternalLoginError(err) || ctx.Err() != nil {
			return user, err
		}
	}
	return nil, fmt.Errorf("completing external login: retry limit exceeded")
}

func (p *PluginProvider) completeExternalLoginAttempt(ctx context.Context, response *pluginv1.AuthenticateResponse, session *models.AuthSession, prepared preparedExternalAccount) (*models.User, error) {
	var user *models.User
	err := p.identities.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var attemptErr error
		user, attemptErr = p.completeExternalLoginInTransaction(ctx, tx, response, session, prepared)
		return attemptErr
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (p *PluginProvider) completeExternalLoginInTransaction(ctx context.Context, tx pgx.Tx, response *pluginv1.AuthenticateResponse, session *models.AuthSession, prepared preparedExternalAccount) (*models.User, error) {
	installationEnabled, err := lockPluginInstallation(ctx, tx, p.config.InstallationID)
	if err != nil {
		return nil, err
	}
	if !installationEnabled {
		return nil, ErrInvalidCredentials
	}
	var binding externalLoginBinding
	if err := tx.QueryRow(ctx, `SELECT enabled, auto_provision, authorization_mode, trusted_link_mode FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2 FOR UPDATE`, p.config.InstallationID, p.config.CapabilityID).Scan(&binding.enabled, &binding.autoProvision, &binding.authorizationMode, &binding.trustedLinkMode); err != nil || !binding.enabled {
		return nil, ErrInvalidCredentials
	}
	key := PluginIdentityKey{InstallationID: p.config.InstallationID, ExternalSubject: response.GetExternalSubject()}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pluginIdentityLockKey(key)); err != nil {
		return nil, fmt.Errorf("acquiring external identity lock: %w", err)
	}

	userID, err := lookupPluginIdentity(ctx, tx, key)
	var user *models.User
	if err == nil {
		user, err = externalAuthorizationUser(ctx, tx, userID)
		if err != nil {
			return nil, err
		}
	} else if errors.Is(err, ErrNotFound) {
		// Trusted linking: when no immutable identity exists and the binding's
		// trusted_link_mode is "trusted_existing", try to match the asserted
		// username to exactly one enabled local-password account.
		if binding.trustedLinkMode == "trusted_existing" {
			asserted := strings.TrimSpace(response.GetAssertedUsername())
			if asserted != "" {
				linked, linkErr := p.completeTrustedUsernameLinkInTransaction(ctx, tx, key, binding, response, session)
				if linkErr != nil {
					return nil, linkErr
				}
				if linked != nil {
					return linked, nil
				}
			}
		}
		if !binding.autoProvision || !p.config.AutoProvision || !p.supportsTransactionalProvisioning() {
			return nil, ErrInvalidCredentials
		}
		user, err = p.createExternalAccountTx(ctx, tx, prepared)
		if err != nil {
			return nil, err
		}
		if err := p.identities.claimInTransaction(ctx, tx, key, user.ID); err != nil {
			return nil, fmt.Errorf("claiming external identity: %w", err)
		}
	} else {
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	if binding.authorizationMode == "external_groups_v1" {
		if p.config.AuthMode != "credentials" {
			return nil, ErrInvalidCredentials
		}
		groups, parseErr := ParseExternalGroupsV1(response.GetClaims())
		if parseErr != nil {
			return nil, ErrInvalidCredentials
		}
		user, err = p.reconcileLDAPGroupsTx(ctx, tx, user, groups)
		if err != nil {
			return nil, err
		}
	} else if binding.authorizationMode != "none" {
		return nil, ErrInvalidCredentials
	}
	if session != nil {
		session.UserID = user.ID
		if err := p.sessions.createWithQuerier(ctx, tx, *session); err != nil {
			return nil, err
		}
	}
	return user, nil
}

type externalLoginBinding struct {
	enabled           bool
	autoProvision     bool
	authorizationMode string
	trustedLinkMode   string
}

func (p *PluginProvider) supportsTransactionalProvisioning() bool {
	provider, ok := p.config.StoreProvider.(userstore.TransactionalProvisioningProvider)
	return ok && provider.SupportsTransactionalProvisioning()
}

func (p *PluginProvider) prepareExternalAccount(creds Credentials, response *pluginv1.AuthenticateResponse) (preparedExternalAccount, error) {
	usernameBase := strings.TrimSpace(response.GetDisplayName())
	if usernameBase == "" {
		usernameBase = strings.TrimSpace(creds.Username)
	}
	if usernameBase == "" {
		usernameBase = response.GetExternalSubject()
	}
	usernameBase = sanitizeUsername(usernameBase)
	if usernameBase == "" {
		usernameBase = fmt.Sprintf("plugin_%d", p.config.InstallationID)
	}
	email := strings.TrimSpace(response.GetEmail())
	if email == "" {
		email = fmt.Sprintf("%s@plugin-%d.local", usernameBase, p.config.InstallationID)
	}
	password, err := randomPluginOnlyPassword()
	if err != nil {
		return preparedExternalAccount{}, fmt.Errorf("generate plugin-only password: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return preparedExternalAccount{}, fmt.Errorf("hash plugin-only password: %w", err)
	}
	return preparedExternalAccount{usernameBase: usernameBase, email: email, passwordHash: string(hash), externalSubject: response.GetExternalSubject()}, nil
}

func (p *PluginProvider) createExternalAccountTx(ctx context.Context, tx pgx.Tx, prepared preparedExternalAccount) (*models.User, error) {
	email := prepared.email
	usingSyntheticEmail := false
	for i := 0; i < 10; i++ {
		username := prepared.usernameBase
		if i > 0 {
			username = fmt.Sprintf("%s_%d", prepared.usernameBase, i+1)
		}
		user, err := createExternalUserAtSavepoint(ctx, tx, username, email, prepared.passwordHash)
		if err == nil {
			if err := createExternalPrimaryProfile(ctx, tx, user.ID, prepared.usernameBase); err != nil {
				return nil, err
			}
			return user, nil
		}
		if !IsDuplicate(err) {
			return nil, fmt.Errorf("create external account: %w", err)
		}
		if !usingSyntheticEmail && IsDuplicateEmail(err) {
			email = syntheticPluginEmail(p.config.InstallationID, prepared.externalSubject)
			usingSyntheticEmail = true
			i = -1
		}
	}
	return nil, fmt.Errorf("create external account: exhausted username attempts")
}

func createExternalUserAtSavepoint(ctx context.Context, tx pgx.Tx, username, email, passwordHash string) (*models.User, error) {
	if _, err := tx.Exec(ctx, "SAVEPOINT external_user"); err != nil {
		return nil, fmt.Errorf("creating external user savepoint: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, `INSERT INTO users (email, username, password_hash, local_password_login_enabled, role, permissions, library_ids, max_playback_quality, access_group_id) VALUES ($1, $2, $3, false, 'user', $4, NULL, '', (SELECT id FROM access_groups WHERE is_default)) RETURNING `+allColumns, NormalizeEmail(email), NormalizeUsername(username), passwordHash, DefaultUserPermissions()))
	if err != nil {
		if _, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT external_user"); rollbackErr != nil {
			return nil, fmt.Errorf("rolling back external user savepoint: %w", rollbackErr)
		}
		if isDuplicateKeyError(err) {
			return nil, &DuplicateUserError{Constraint: extractConstraint(err)}
		}
		return nil, fmt.Errorf("inserting external user: %w", err)
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT external_user"); err != nil {
		return nil, fmt.Errorf("releasing external user savepoint: %w", err)
	}
	return user, nil
}

func createExternalPrimaryProfile(ctx context.Context, tx pgx.Tx, userID int, name string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary, show_forced_subtitles) VALUES ($1, $2, $3, true, true)`, uuid.NewString(), userID, name); err != nil {
		return fmt.Errorf("creating external primary profile: %w", err)
	}
	return nil
}

func isRetryableExternalLoginError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func (p *PluginProvider) completeTrustedUsernameLinkInTransaction(ctx context.Context, tx pgx.Tx, identityKey PluginIdentityKey, binding externalLoginBinding, response *pluginv1.AuthenticateResponse, session *models.AuthSession) (*models.User, error) {
	asserted := NormalizeUsername(strings.TrimSpace(response.GetAssertedUsername()))
	if asserted == "" {
		return nil, nil
	}
	providerUsernameLockKey := trustedLinkProviderUsernameLockKey(p.config.InstallationID, p.config.CapabilityID, asserted)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pluginIdentityLockKey(identityKey)); err != nil {
		return nil, fmt.Errorf("acquiring trusted link identity lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, providerUsernameLockKey); err != nil {
		return nil, fmt.Errorf("acquiring trusted link provider username lock: %w", err)
	}
	var recheckBinding externalLoginBinding
	if err := tx.QueryRow(ctx, `SELECT enabled, trusted_link_mode FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2 FOR UPDATE`, p.config.InstallationID, p.config.CapabilityID).Scan(&recheckBinding.enabled, &recheckBinding.trustedLinkMode); err != nil || !recheckBinding.enabled || recheckBinding.trustedLinkMode != "trusted_existing" {
		return nil, nil
	}
	recheckEnabled, err := lockPluginInstallation(ctx, tx, p.config.InstallationID)
	if err != nil || !recheckEnabled {
		return nil, nil
	}
	if existingUserID, err := lookupPluginIdentity(ctx, tx, identityKey); err == nil {
		user, authErr := externalAuthorizationUser(ctx, tx, existingUserID)
		if authErr != nil {
			return nil, authErr
		}
		return user, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	normalizedUsername := NormalizeUsername(asserted)
	var targetUserID int
	var targetEnabled, targetLocalLogin bool
	err = tx.QueryRow(ctx, `
		SELECT id, enabled, local_password_login_enabled FROM users
		WHERE username = $1
		LIMIT 1
		FOR UPDATE`, normalizedUsername,
	).Scan(&targetUserID, &targetEnabled, &targetLocalLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trusted link target lookup: %w", err)
	}
	if !targetEnabled || !targetLocalLogin {
		return nil, ErrInvalidCredentials
	}

	usernameFP := sha256Fingerprint(asserted)
	var conflictUserID int
	err = tx.QueryRow(ctx, `
		SELECT user_id FROM external_identity_link_audit
		WHERE plugin_installation_id = $1
		  AND capability_id = $2
		  AND asserted_username_fingerprint = $3
		  AND event_type = 'trusted_link'
		LIMIT 1`,
		p.config.InstallationID, p.config.CapabilityID, usernameFP,
	).Scan(&conflictUserID)
	if err == nil {
		return nil, &TrustedLinkOwnershipConflictError{
			InstallationID:  p.config.InstallationID,
			CapabilityID:    p.config.CapabilityID,
			ExistingUserID:  conflictUserID,
			ExternalSubject: identityKey.ExternalSubject,
		}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("trusted link ownership check: %w", err)
	}

	if err := p.identities.claimInTransaction(ctx, tx, identityKey, targetUserID); err != nil {
		return nil, fmt.Errorf("trusted link identity claim: %w", err)
	}

	correlationID := uuid.New()
	if err := insertTrustedLinkAudit(ctx, tx, targetUserID, p.config.InstallationID, p.config.CapabilityID, identityKey.ExternalSubject, asserted, "trusted_username_link", correlationID); err != nil {
		return nil, fmt.Errorf("trusted link audit: %w", err)
	}

	if err := revokeUserAuthorizationSessions(ctx, tx, targetUserID); err != nil {
		return nil, fmt.Errorf("trusted link session revocation: %w", err)
	}

	user, err := externalAuthorizationUser(ctx, tx, targetUserID)
	if err != nil {
		return nil, err
	}
	if session != nil {
		session.UserID = user.ID
		if err := p.sessions.createWithQuerier(ctx, tx, *session); err != nil {
			return nil, err
		}
	}
	return user, nil
}

func trustedLinkProviderUsernameLockKey(installationID int, capabilityID, normalizedUsername string) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("trusted_link:%d:%s:%s", installationID, capabilityID, normalizedUsername)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func insertTrustedLinkAudit(ctx context.Context, tx pgx.Tx, userID, installationID int, capabilityID, externalSubject, assertedUsername, reasonCode string, correlationID uuid.UUID) error {
	subjectFP := sha256Fingerprint(externalSubject)
	usernameFP := sha256Fingerprint(assertedUsername)
	_, err := tx.Exec(ctx, `
		INSERT INTO external_identity_link_audit (
			user_id, original_user_id, original_profile_id,
			plugin_installation_id, capability_id,
			event_type, trusted_link_mode,
			external_subject_fingerprint, asserted_username_fingerprint,
			reason_code, correlation_id
		) VALUES ($1, $1, '', $2, $3, 'trusted_link', 'trusted_existing', $4, $5, $6, $7)`,
		userID, installationID, capabilityID, subjectFP, usernameFP, reasonCode, correlationID,
	)
	return err
}

func sha256Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
