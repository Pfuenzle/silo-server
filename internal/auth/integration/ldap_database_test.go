//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func (h *packagedLDAPHarness) databaseQuery(t *testing.T, ctx context.Context, query string, args ...any) string {
	t.Helper()
	if len(args) > 0 {
		query = fmt.Sprintf(query, args...)
	}
	return strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-F", "|", "-c", query))
}

func (h *packagedLDAPHarness) assertRuntimeConfigEncryptedEnvelope(t *testing.T, ctx context.Context) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT config_value::text FROM plugin_runtime_configs WHERE plugin_installation_id = %d AND config_key = 'ldap'`, h.installID)
	if !strings.Contains(row, `"__silo_encrypted_runtime_config_v1"`) || !strings.Contains(row, "enc:") || strings.Contains(row, "service-password") {
		t.Fatalf("LDAP runtime config is not encrypted at rest: %q", row)
	}
}

func (h *packagedLDAPHarness) ldapUserID(t *testing.T, ctx context.Context) string {
	t.Helper()
	userID := h.databaseQuery(t, ctx, `SELECT user_id FROM plugin_auth_identities WHERE plugin_installation_id = %d AND external_subject = 'ldap-user'`, h.installID)
	if userID == "" || strings.Contains(userID, "\n") {
		t.Fatalf("LDAP identity user id = %q", userID)
	}
	return userID
}

func (h *packagedLDAPHarness) ldapCounts(t *testing.T, ctx context.Context) string {
	t.Helper()
	return strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-c", `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = `+itoa(h.installID)+`)::text || '|' || (SELECT count(*) FROM auth_sessions WHERE provider_key = 'plugin:`+itoa(h.installID)+`:ldap' AND revoked_at IS NULL)::text || '|' || (SELECT count(*) FROM external_authorization_audit WHERE plugin_installation_id = `+itoa(h.installID)+`)::text`))
}

func (h *packagedLDAPHarness) ldapSnapshot(t *testing.T, ctx context.Context) string {
	t.Helper()
	return strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-c", `SELECT
		(SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = `+itoa(h.installID)+`)::text || '|' ||
		(SELECT count(*) FROM users u WHERE EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id AND i.plugin_installation_id = `+itoa(h.installID)+`))::text || '|' ||
		(SELECT count(*) FROM user_profiles p WHERE EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = p.user_id AND i.plugin_installation_id = `+itoa(h.installID)+`))::text || '|' ||
		(SELECT count(*) FROM auth_sessions WHERE provider_key = 'plugin:`+itoa(h.installID)+`:ldap')::text || '|' ||
		(SELECT count(*) FROM external_authorization_audit WHERE plugin_installation_id = `+itoa(h.installID)+`)::text || '|' ||
		(SELECT count(*) FROM external_authorization_states WHERE plugin_installation_id = `+itoa(h.installID)+`)::text`))
}

func (h *packagedLDAPHarness) assertLDAPIdentity(t *testing.T, ctx context.Context, role, localLogin string) {
	t.Helper()
	row := strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-F", "|", "-c", `SELECT i.external_subject, u.role, u.local_password_login_enabled::text, count(p.id) FROM plugin_auth_identities i JOIN users u ON u.id = i.user_id LEFT JOIN user_profiles p ON p.user_id = u.id AND p.is_primary WHERE i.plugin_installation_id = `+itoa(h.installID)+` GROUP BY i.external_subject, u.role, u.local_password_login_enabled`))
	parts := strings.Split(row, "|")
	if len(parts) != 4 || parts[0] != "ldap-user" || parts[1] != role || parts[2] != localLogin || parts[3] != "1" {
		t.Fatalf("LDAP identity/user/primary profile/provenance = %q", row)
	}
}

func (h *packagedLDAPHarness) assertLDAPAccessGroup(t *testing.T, ctx context.Context, accessGroupID int64) {
	t.Helper()
	row := strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-c", `SELECT u.access_group_id::text FROM users u JOIN plugin_auth_identities i ON i.user_id = u.id WHERE i.plugin_installation_id = `+itoa(h.installID)))
	if row != strconv.FormatInt(accessGroupID, 10) {
		t.Fatalf("LDAP access group=%q, want %d", row, accessGroupID)
	}
}

func (h *packagedLDAPHarness) assertMe(t *testing.T, ctx context.Context, token string, status int) {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/me", token, nil, "")
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("LDAP /me=%d, want %d", response.StatusCode, status)
	}
}

func (h *packagedLDAPHarness) assertRejected(t *testing.T, ctx context.Context, pair ldapLoginPair) {
	t.Helper()
	h.assertMe(t, ctx, pair.AccessToken, http.StatusUnauthorized)
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"refresh_token": pair.RefreshToken})
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked LDAP refresh=%d, want 401", response.StatusCode)
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
