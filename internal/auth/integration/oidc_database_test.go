//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

type oidcIdentitySnapshot struct {
	IdentityID                string
	UserID                    string
	ProfileID                 string
	Role                      string
	GroupID                   string
	GroupName                 string
	LocalPasswordLoginEnabled string
}

func (h *packagedOIDCHarness) databaseQuery(t *testing.T, ctx context.Context, query string, args ...any) string {
	t.Helper()
	if len(args) > 0 {
		query = fmt.Sprintf(query, args...)
	}
	return strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-F", "|", "-c", query))
}

func (h *packagedOIDCHarness) identitySnapshot(t *testing.T, ctx context.Context) oidcIdentitySnapshot {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT i.external_subject, i.user_id, p.id, u.role, COALESCE(u.access_group_id::text, ''), COALESCE(g.name, ''), u.local_password_login_enabled FROM plugin_auth_identities i JOIN users u ON u.id = i.user_id JOIN user_profiles p ON p.user_id = u.id AND p.is_primary LEFT JOIN access_groups g ON g.id = u.access_group_id WHERE i.plugin_installation_id = %d`, h.installID)
	parts := strings.Split(row, "|")
	if len(parts) != 7 || strings.Contains(row, "\n") {
		t.Fatalf("expected one stable OIDC identity/user/primary profile, got %q", row)
	}
	return oidcIdentitySnapshot{IdentityID: parts[0], UserID: parts[1], ProfileID: parts[2], Role: parts[3], GroupID: parts[4], GroupName: parts[5], LocalPasswordLoginEnabled: parts[6]}
}

func (h *packagedOIDCHarness) assertIdentityUnchanged(t *testing.T, ctx context.Context, before oidcIdentitySnapshot) {
	t.Helper()
	after := h.identitySnapshot(t, ctx)
	if after != before {
		t.Fatalf("OIDC identity changed: before=%+v after=%+v", before, after)
	}
	if !strings.HasSuffix(after.IdentityID, "\x1fintegration-subject") || after.Role != "user" || after.GroupID == "" || after.GroupName == "" || after.LocalPasswordLoginEnabled != "f" {
		t.Fatalf("hostile claims changed default authorization: %+v", after)
	}
}

func (h *packagedOIDCHarness) identityAndSessionCounts(t *testing.T, ctx context.Context) string {
	t.Helper()
	return h.databaseQuery(t, ctx, `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM auth_sessions WHERE provider_key = 'plugin:%d:oidc' AND revoked_at IS NULL)::text`, h.installID, h.installID)
}

func (h *packagedOIDCHarness) firstLoginCounts(t *testing.T, ctx context.Context) string {
	t.Helper()
	return h.databaseQuery(t, ctx, `SELECT
		(SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = %d)::text || '|' ||
		(SELECT count(*) FROM users u WHERE EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id AND i.plugin_installation_id = %d))::text || '|' ||
		(SELECT count(*) FROM user_profiles p WHERE EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = p.user_id AND i.plugin_installation_id = %d))::text || '|' ||
		(SELECT count(*) FROM auth_sessions WHERE provider_key = 'plugin:%d:oidc')::text`, h.installID, h.installID, h.installID, h.installID)
}

func (h *packagedOIDCHarness) assertProviderSession(t *testing.T, ctx context.Context) {
	t.Helper()
	if row := h.identityAndSessionCounts(t, ctx); row != "1|1" {
		t.Fatalf("expected one identity and one active provider session, got %q", row)
	}
}

func (h *packagedOIDCHarness) assertOIDCCardinality(t *testing.T, ctx context.Context) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM users u JOIN plugin_auth_identities i ON i.user_id = u.id WHERE i.plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM user_profiles p JOIN plugin_auth_identities i ON i.user_id = p.user_id WHERE i.plugin_installation_id = %d)::text`, h.installID, h.installID, h.installID)
	if row != "1|1|1" {
		t.Fatalf("OIDC identity/user/profile cardinality = %q, want 1|1|1", row)
	}
}

func (h *packagedOIDCHarness) assertOAuthStateEncrypted(t *testing.T, ctx context.Context) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT provider_state::text, COALESCE(provider_state_ciphertext, '') FROM oauth_session ORDER BY created_at DESC LIMIT 1`)
	parts := strings.Split(row, "|")
	if len(parts) != 2 || parts[1] == "" || parts[0] != "{}" || !strings.Contains(parts[1], "enc:") {
		t.Fatalf("OAuth provider state is not encrypted at rest: %q", row)
	}
	h.assertSecretAbsent(t, "oauth_session", row)
}

func (h *packagedOIDCHarness) assertRuntimeConfigEncryptedEnvelope(t *testing.T, ctx context.Context, installationID int) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT config_value::text FROM plugin_runtime_configs WHERE plugin_installation_id = %d AND config_key = 'oidc'`, installationID)
	if !strings.Contains(row, `"__silo_encrypted_runtime_config_v1"`) || !strings.Contains(row, `enc:`) {
		t.Fatalf("runtime config is not an encrypted envelope: %q", row)
	}
	h.assertSecretAbsent(t, "plugin runtime config envelope", row)
}

func (h *packagedOIDCHarness) assertDefaultAccessGroup(t *testing.T, ctx context.Context) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT u.access_group_id::text || '|' || g.is_default::text FROM users u JOIN plugin_auth_identities i ON i.user_id = u.id JOIN access_groups g ON g.id = u.access_group_id WHERE i.plugin_installation_id = %d`, h.installID)
	if parts := strings.Split(row, "|"); len(parts) != 2 || parts[0] == "" || parts[1] != "true" {
		t.Fatalf("OIDC user does not hold the default access group: %q", row)
	}
}

func (h *packagedOIDCHarness) assertCompletionEncrypted(t *testing.T, ctx context.Context) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT code_hash, token_ciphertext FROM oauth_completion ORDER BY created_at DESC LIMIT 1`)
	parts := strings.Split(row, "|")
	if len(parts) != 2 || len(parts[0]) != 64 || parts[1] == "" {
		t.Fatalf("OAuth completion lacks hashed code or encrypted tokens: %q", row)
	}
	// PGOAuthStore.encryptCompletionTokens stores base64url(nonce||GCM-sealed JSON), not secret.Cipher's enc:v1 envelope.
	sealed, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sealed) < 28 {
		t.Fatalf("OAuth completion lacks hashed code or encrypted tokens: %q", row)
	}
	h.assertSecretAbsent(t, "oauth_completion", row)
}

func (h *packagedOIDCHarness) assertCompletionConsumed(t *testing.T, ctx context.Context) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT count(*) FROM oauth_completion`); row != "0" {
		t.Fatalf("OAuth completion was not consumed exactly once: count=%s", row)
	}
}

func (h *packagedOIDCHarness) assertSecretAbsent(t *testing.T, location, value string) {
	t.Helper()
	for _, secret := range []string{"integration-client-secret", "integration-code", "access", "integration-subject", "OIDC User", "oidc@example.test"} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked sensitive upstream value %q", location, secret)
		}
	}
}

func (h *packagedOIDCHarness) assertNoSecretsInDurableStores(t *testing.T, ctx context.Context) {
	t.Helper()
	for name, query := range map[string]string{
		"plugin runtime config": `SELECT config_value::text FROM plugin_runtime_configs WHERE plugin_installation_id = ` + fmt.Sprint(h.installID),
		"oauth session":         `SELECT provider_state::text || '|' || COALESCE(provider_state_ciphertext, '') FROM oauth_session`,
		"oauth completion":      `SELECT code_hash || '|' || token_ciphertext FROM oauth_completion`,
	} {
		h.assertSecretAbsent(t, name, h.databaseQuery(t, ctx, query))
	}
}
