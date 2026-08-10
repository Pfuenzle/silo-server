//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedLDAP_RetainedAuthorizationLifecycle(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	h.assertRuntimeConfigEncryptedEnvelope(t, ctx)
	initial := h.login(t, ctx, "test-password")
	h.assertLDAPIdentity(t, ctx, "user", "false")
	h.assertLDAPProviderSession(t, ctx, 1)
	h.assertLocalCredentialsRejected(t, ctx)

	adminGroupID := h.admin.createLDAPAccessGroup(t, ctx)
	h.admin.replaceLDAPMappings(t, ctx, h.installID, []map[string]any{{"external_group_id": "baseline", "target_role": "admin", "access_group_id": adminGroupID}})
	h.assertRejected(t, ctx, initial)
	admin := h.login(t, ctx, "test-password")
	h.assertLDAPIdentity(t, ctx, "admin", "false")
	h.assertLDAPAccessGroup(t, ctx, adminGroupID)
	h.seedLegacyCredentials(t, ctx, h.ldapUserID(t, ctx))

	// When
	h.directory.setBaselineMembership(t, ctx, false)
	least := h.login(t, ctx, "test-password")

	// Then
	h.assertRejected(t, ctx, admin)
	h.assertLDAPIdentity(t, ctx, "user", "false")
	h.assertDefaultLDAPAccessGroup(t, ctx)
	h.assertLegacyCredentialsRevoked(t, ctx, h.ldapUserID(t, ctx))
	h.assertRevokedLegacyHTTPSurfaces(t, ctx)
	h.assertMe(t, ctx, least.AccessToken, http.StatusOK)
	h.assertLDAPProviderSession(t, ctx, 1)
	h.assertLDAPAuditTrail(t, ctx, adminGroupID)
	h.admin.assertBreakGlassAdmin(t, ctx, "LDAP membership demotion")
	retained := h.retainedLDAPLifecycleState(t, ctx)

	t.Run("disable replacement re-enable preserves installation", func(t *testing.T) {
		h.admin.setInstallationEnabled(t, ctx, h.installID, false)
		h.assertRejected(t, ctx, least)
		h.assertLDAPInstallationDisableAudit(t, ctx)
		h.admin.assertBreakGlassAdmin(t, ctx, "LDAP disable")
		h.restart(t, ctx)
		h.assertLDAPProviderAbsent(t, ctx)
		h.admin.assertBreakGlassAdmin(t, ctx, "LDAP disabled restart")

		downgrade := h.releaseLDAPArtifact(t, ctx, "1.2.2")
		if installation := h.admin.uploadPlugin(t, ctx, downgrade); installation.ID != h.installID || installation.Version != "1.2.2" {
			t.Fatalf("disabled LDAP package replacement = %+v, want installation=%d version=1.2.2", installation, h.installID)
		}
		h.restart(t, ctx)
		h.assertLDAPProviderAbsent(t, ctx)

		h.admin.setInstallationEnabled(t, ctx, h.installID, true)
		upgrade := h.releaseLDAPArtifact(t, ctx, "1.2.3")
		if installation := h.admin.uploadPlugin(t, ctx, upgrade); installation.ID != h.installID || installation.Version != "1.2.3" {
			t.Fatalf("enabled LDAP package replacement = %+v, want installation=%d version=1.2.3", installation, h.installID)
		}
		h.restart(t, ctx)
		h.assertProvider(t, ctx)
		if restored := h.retainedLDAPLifecycleState(t, ctx); restored != retained {
			t.Fatalf("LDAP lifecycle state after disable/package replacement/re-enable changed: before=%s after=%s", retained, restored)
		}
		h.directory.setBaselineMembership(t, ctx, true)
		fresh := h.login(t, ctx, "test-password")
		h.assertLDAPIdentity(t, ctx, "admin", "false")
		h.assertLDAPAccessGroup(t, ctx, adminGroupID)
		h.assertLDAPProviderSession(t, ctx, 1)
		h.assertExactLDAPCardinality(t, ctx)
		h.assertMe(t, ctx, fresh.AccessToken, http.StatusOK)
		h.admin.assertBreakGlassAdmin(t, ctx, "LDAP re-enable and package replacement")
	})
}

func (h *packagedLDAPHarness) restart(t *testing.T, ctx context.Context) {
	t.Helper()
	h.requestRestart(t, ctx, h.process)
	h.process = h.launch(t, ctx)
	h.admin.rebind(h.process.baseURL)
}

func (h *packagedLDAPHarness) releaseLDAPArtifact(t *testing.T, ctx context.Context, version string) string {
	t.Helper()
	release := filepath.Join(h.temp, "ldap-release-"+version)
	runCommand(t, ctx, filepath.Join(h.root, "..", "silo-plugin-auth-ldap"), "sh", "scripts/release.sh", version, release)
	artifact := filepath.Join(release, "plugin-linux-amd64")
	verifyArtifactChecksum(t, artifact, filepath.Join(release, "checksums.txt"))
	return artifact
}

func (h *packagedLDAPHarness) assertLDAPProviderSession(t *testing.T, ctx context.Context, sessions int) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM auth_sessions WHERE provider_key = 'plugin:%d:ldap' AND revoked_at IS NULL)::text`, h.installID, h.installID); row != fmt.Sprintf("1|%d", sessions) {
		t.Fatalf("LDAP identity and active provider sessions = %q, want 1|%d", row, sessions)
	}
}

func (h *packagedLDAPHarness) assertLocalCredentialsRejected(t *testing.T, ctx context.Context) {
	t.Helper()
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": "local", "username": "ldap-user", "password": "test-password"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("external LDAP account local credential login=%d, want 401", response.StatusCode)
	}
}

func (h *packagedLDAPHarness) seedLegacyCredentials(t *testing.T, ctx context.Context, userID string) {
	t.Helper()
	query := `INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at) VALUES ('ldap-playback', 'ldap-playback-token', (` + userID + `::bigint)::text, '{}', NOW() + INTERVAL '1 hour'); INSERT INTO jellycompat_sessions (token, username, account_username, profile_id, profile_name, pseudo_user_id, streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at) VALUES ('ldap-compat-token', 'ldap-user', 'ldap-user', 'ldap-profile', 'LDAP User', '00000000-0000-0000-0000-000000000001', ` + userID + `, 'ldap-access', 'ldap-refresh', NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 hour')`
	runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", query)
}

func (h *packagedLDAPHarness) assertDefaultLDAPAccessGroup(t *testing.T, ctx context.Context) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT u.role || '|' || g.is_default::text FROM users u JOIN plugin_auth_identities i ON i.user_id = u.id JOIN access_groups g ON g.id = u.access_group_id WHERE i.plugin_installation_id = %d`, h.installID); row != "user|true" {
		t.Fatalf("demoted LDAP authorization = %q, want user|true", row)
	}
}

func (h *packagedLDAPHarness) assertLegacyCredentialsRevoked(t *testing.T, ctx context.Context, userID string) {
	t.Helper()
	row := h.databaseQuery(t, ctx, `SELECT (SELECT count(*) FROM auth_sessions WHERE user_id = %s AND revoked_at IS NULL)::text || '|' || (SELECT count(*) FROM jellycompat_playback_sessions WHERE user_id = (%s::bigint)::text)::text || '|' || (SELECT count(*) FROM jellycompat_sessions WHERE streamapp_user_id = %s)::text`, userID, userID, userID)
	if row != "1|0|0" {
		t.Fatalf("demoted LDAP legacy credentials = %q, want fresh provider session and no Jellyfin compatibility rows", row)
	}
}

func (h *packagedLDAPHarness) assertRevokedLegacyHTTPSurfaces(t *testing.T, ctx context.Context) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/Items/missing/PlaybackInfo", h.process.jellyPort), nil)
	if err != nil {
		t.Fatalf("build revoked Jellyfin compatibility request: %v", err)
	}
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Token="ldap-compat-token"`)
	response, err := h.admin.client.Do(req)
	if err != nil {
		t.Fatalf("request revoked Jellyfin compatibility surface: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked Jellyfin compatibility surface=%d body=%q, want 401", response.StatusCode, responseBody(t, response))
	}
}

func (h *packagedLDAPHarness) assertLDAPAuditTrail(t *testing.T, ctx context.Context, adminGroupID int64) {
	t.Helper()
	rows := h.databaseQuery(t, ctx, `SELECT old_role || '>' || new_role || '|' || COALESCE(old_access_group_id::text, '') || '>' || COALESCE(new_access_group_id::text, '') || '|' || matched_group_ids::text || '|' || capability_id || '|' || plugin_installation_id::text || '|' || correlation_id::text || '|' || reason FROM external_authorization_audit WHERE plugin_installation_id = %d ORDER BY id`, h.installID)
	parts := strings.Split(rows, "\n")
	want := []string{
		"user>user|1>1|{baseline}|ldap_login",
		"user>user|1>1|{}|mapping_changed",
		fmt.Sprintf("user>admin|1>%d|{baseline}|ldap_login", adminGroupID),
		fmt.Sprintf("admin>user|%d>1|{}|ldap_login", adminGroupID),
	}
	if len(parts) != len(want) {
		t.Fatalf("LDAP ordered audit rows = %q", rows)
	}
	for index, row := range parts {
		fields := strings.Split(row, "|")
		if len(fields) != 7 || fields[3] != "ldap" || fields[4] != fmt.Sprint(h.installID) || fields[5] == "" || fields[5] == "0" {
			t.Fatalf("LDAP audit durability/provenance row = %q", row)
		}
		if got := strings.Join([]string{fields[0], fields[1], fields[2], fields[6]}, "|"); got != want[index] {
			t.Fatalf("LDAP audit[%d] = %q, want %q", index, got, want[index])
		}
	}
}

func (h *packagedLDAPHarness) assertLDAPProviderAbsent(t *testing.T, ctx context.Context) {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/providers", "", nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(responseBody(t, response), fmt.Sprintf("plugin:%d:ldap", h.installID)) {
		t.Fatal("disabled LDAP provider remained visible")
	}
}

func (h *packagedLDAPHarness) assertExactLDAPCardinality(t *testing.T, ctx context.Context) {
	t.Helper()
	if row := h.databaseQuery(t, ctx, `SELECT (SELECT count(*) FROM plugin_auth_identities WHERE plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM users u JOIN plugin_auth_identities i ON i.user_id = u.id WHERE i.plugin_installation_id = %d)::text || '|' || (SELECT count(*) FROM user_profiles p JOIN plugin_auth_identities i ON i.user_id = p.user_id AND p.is_primary WHERE i.plugin_installation_id = %d)::text`, h.installID, h.installID, h.installID); row != "1|1|1" {
		t.Fatalf("LDAP identity/user/primary profile cardinality = %q, want 1|1|1", row)
	}
}
