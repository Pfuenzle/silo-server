//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPackagedLDAP_ABSCompatibilitySessionRevokedOnMembershipDemotion(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	t.Logf("packaged ABS executable SHA-256=%s", runCommand(t, ctx, h.buildRoot, "sha256sum", h.binary))
	h.login(t, ctx, "test-password")
	adminGroupID := h.admin.createLDAPAccessGroup(t, ctx)
	h.admin.replaceLDAPMappings(t, ctx, h.installID, []map[string]any{{"external_group_id": "baseline", "target_role": "admin", "access_group_id": adminGroupID}})
	h.login(t, ctx, "test-password")
	h.assertLDAPIdentity(t, ctx, "admin", "false")
	h.assertLDAPAccessGroup(t, ctx, adminGroupID)
	h.seedDeterministicABSJWTSecret(t, ctx)
	h.restart(t, ctx)
	userID := h.ldapUserID(t, ctx)
	profileID := h.databaseQuery(t, ctx, `SELECT p.id FROM user_profiles p JOIN plugin_auth_identities i ON i.user_id = p.user_id WHERE i.plugin_installation_id = %d AND p.is_primary`, h.installID)
	if row := h.databaseQuery(t, ctx, `SELECT i.user_id || '|' || p.id FROM plugin_auth_identities i JOIN user_profiles p ON p.user_id = i.user_id AND p.is_primary WHERE i.plugin_installation_id = %d AND i.external_subject = 'ldap-user'`, h.installID); row != userID+"|"+profileID {
		t.Fatalf("LDAP identity and primary profile principal = %q, want %s|%s", row, userID, profileID)
	}
	pool, err := pgxpool.New(ctx, h.resources.databaseURL)
	if err != nil {
		t.Fatalf("connect ABS session repository: %v", err)
	}
	defer pool.Close()
	const jti = "ldap-retained-abs-jti"
	token, err := abs.IssueAccessToken([]byte(deterministicSecret), userID, profileID, jti, time.Hour)
	if err != nil {
		t.Fatalf("issue ABS access token: %v", err)
	}
	claims, err := abs.ParseToken([]byte(deterministicSecret), token)
	if err != nil || claims.Type != "access" || claims.UserID != userID || claims.ProfileID != profileID || claims.JTI != jti {
		t.Fatalf("issued ABS token claims = %#v err=%v", claims, err)
	}
	store := &audiobooks.ABSSessionStore{Pool: pool}
	if err := store.InsertToken(ctx, abs.ABSToken{ID: jti, UserID: userID, ProfileID: profileID, Type: "access", JTI: jti, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("persist ABS session: %v", err)
	}
	h.assertABSSessionRevocation(t, ctx, jti, userID, profileID, false)
	if stored, err := store.GetTokenByJTI(ctx, jti); err != nil || stored.UserID != userID || stored.ProfileID != profileID || stored.Type != "access" || stored.JTI != jti {
		t.Fatalf("lookup SHA-256 ABS session = %#v err=%v", stored, err)
	}
	const storeJTI = "ldap-store-revoke-jti"
	if err := store.InsertToken(ctx, abs.ABSToken{ID: storeJTI, UserID: userID, ProfileID: profileID, Type: "access", JTI: storeJTI, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("persist direct-revoke ABS session: %v", err)
	}
	if err := store.RevokeTokenByJTI(ctx, storeJTI); err != nil {
		t.Fatalf("revoke SHA-256 ABS session: %v", err)
	}
	h.assertABSSessionRevocation(t, ctx, storeJTI, userID, profileID, true)

	// When
	h.assertABSMe(t, ctx, token, http.StatusOK)
	h.directory.setBaselineMembership(t, ctx, false)
	h.login(t, ctx, "test-password")

	// Then
	h.assertLDAPIdentity(t, ctx, "user", "false")
	h.assertDefaultLDAPAccessGroup(t, ctx)
	h.assertABSSessionRevocation(t, ctx, jti, userID, profileID, true)
	h.assertABSMe(t, ctx, token, http.StatusUnauthorized)
}

func (h *packagedLDAPHarness) assertABSMe(t *testing.T, ctx context.Context, token string, status int) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/me", h.process.absPort), nil)
	if err != nil {
		t.Fatalf("build ABS /api/me request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := h.admin.client.Do(req)
	if err != nil {
		t.Fatalf("request ABS /api/me: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("ABS /api/me=%d, want %d: %s", response.StatusCode, status, responseBody(t, response))
	}
}

func (h *packagedLDAPHarness) assertABSSessionRevocation(t *testing.T, ctx context.Context, jti, userID, profileID string, revoked bool) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT user_id || '|' || profile_id || '|' || token_hash || '|' || token_type || '|' || (revoked_at IS NOT NULL)::text FROM abs_sessions WHERE token_hash = '%s'`, absSessionHash(jti)); row != fmt.Sprintf("%s|%s|%s|access|%t", userID, profileID, absSessionHash(jti), revoked) {
		t.Fatalf("ABS session revocation state = %q, want matching principal/SHA-256 JTI access row revoked=%t", row, revoked)
	}
}

func (h *packagedLDAPHarness) retainedLDAPLifecycleState(t *testing.T, ctx context.Context) string {
	t.Helper()
	return h.databaseQuery(t, ctx, `SELECT json_build_object(
		'installation', (SELECT json_build_object('id', id, 'plugin_id', plugin_id, 'version', version, 'enabled', enabled) FROM plugin_installations WHERE id = %d),
		'encrypted_config', (SELECT config_value FROM plugin_runtime_configs WHERE plugin_installation_id = %d AND config_key = 'ldap'),
		'binding', (SELECT json_agg(json_build_object('capability_id', capability_id, 'enabled', enabled, 'auto_provision', auto_provision, 'authorization_mode', authorization_mode) ORDER BY capability_id) FROM plugin_auth_bindings WHERE plugin_installation_id = %d),
		'mappings', (SELECT json_agg(json_build_object('external_group_id', external_group_id, 'target_role', target_role, 'access_group_id', access_group_id) ORDER BY external_group_id) FROM plugin_auth_group_mappings WHERE plugin_installation_id = %d),
		'auth_state', (SELECT json_agg(json_build_object('user_id', user_id, 'capability_id', capability_id) ORDER BY user_id, capability_id) FROM external_authorization_states WHERE plugin_installation_id = %d),
		'identities', (SELECT json_agg(json_build_object('external_subject', i.external_subject, 'user_id', i.user_id, 'role', u.role, 'access_group_id', u.access_group_id, 'local_password_login_enabled', u.local_password_login_enabled) ORDER BY i.external_subject) FROM plugin_auth_identities i JOIN users u ON u.id = i.user_id WHERE i.plugin_installation_id = %d),
		'prior_audits', (SELECT json_agg(json_build_object('user_id', user_id, 'capability_id', capability_id, 'old_role', old_role, 'new_role', new_role, 'old_access_group_id', old_access_group_id, 'new_access_group_id', new_access_group_id, 'matched_group_ids', matched_group_ids, 'reason', reason) ORDER BY id) FROM external_authorization_audit WHERE plugin_installation_id = %d AND reason <> 'installation_disabled')
	)::text`, h.installID, h.installID, h.installID, h.installID, h.installID, h.installID, h.installID)
}

func (h *packagedLDAPHarness) seedDeterministicABSJWTSecret(t *testing.T, ctx context.Context) {
	t.Helper()
	secret := hex.EncodeToString([]byte(deterministicSecret))
	runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", fmt.Sprintf(`INSERT INTO server_settings (key, value) VALUES ('audiobooks.abs.jwt_secret', '%s') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, secret))
}

func (h *packagedLDAPHarness) assertLDAPInstallationDisableAudit(t *testing.T, ctx context.Context) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT count(*) FROM external_authorization_audit WHERE plugin_installation_id = %d AND reason = 'installation_disabled'`, h.installID); row != "1" {
		t.Fatalf("LDAP installation disable audit rows = %q, want 1", row)
	}
}

func absSessionHash(jti string) string {
	sum := sha256.Sum256([]byte(jti))
	return hex.EncodeToString(sum[:])
}
