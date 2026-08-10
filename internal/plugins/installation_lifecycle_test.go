package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestInstallationStoreDelete_returnsDependencyConflict_whenAuthDataExists(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ('empty-delete-lifecycle', '0', '/nonexistent/empty-delete-lifecycle') RETURNING id`).Scan(&installationID); err != nil {
		t.Fatalf("seed empty installation: %v", err)
	}
	userID := seedAuthBindingRevocationUser(t, pool)
	provider, err := models.NewPluginSessionProviderKey(installationID, "ldap")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'delete-guard', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at, provider_key) VALUES ('delete-guard', $1, $2, $3)`, userID, time.Now().Add(time.Hour), provider.String()); err != nil {
		t.Fatalf("seed provider session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'delete-guard', 'user')`, installationID); err != nil {
		t.Fatalf("seed group mapping: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'ldap', 'user', 'user', '{}', 'delete_guard', gen_random_uuid())`, userID, installationID); err != nil {
		t.Fatalf("seed authorization audit: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, cleanup := range []struct {
			query string
			arg   int
		}{
			{`DELETE FROM external_authorization_audit WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM external_authorization_states WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM auth_sessions WHERE user_id = $1`, userID},
			{`DELETE FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM plugin_auth_identities WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM plugin_installations WHERE id = $1`, installationID},
		} {
			if _, err := pool.Exec(cleanupCtx, cleanup.query, cleanup.arg); err != nil {
				t.Errorf("cleanup lifecycle fixture: %v", err)
			}
		}
	})

	// When
	err = NewInstallationStore(pool).Delete(ctx, installationID)

	// Then
	var conflict *InstallationDependencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Delete() error = %v, want InstallationDependencyConflictError", err)
	}
	if conflict.Identities != 1 || conflict.ProviderSessions != 1 || conflict.GroupMappings != 1 || conflict.AuthorizationAudits != 1 || conflict.IdentityLinkAudits != 0 || conflict.ProviderPolicyAudits != 0 {
		t.Fatalf("Delete() counts = %+v, want one of each", conflict)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_installations WHERE id = $1`, installationID).Scan(&rows); err != nil {
		t.Fatalf("count installation: %v", err)
	}
	if rows != 1 {
		t.Fatalf("installation rows = %d, want 1", rows)
	}
}

func TestInstallationStoreDelete_allowsEmptyInstallation(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ('empty-delete-lifecycle', '0', '/nonexistent/empty-delete-lifecycle') RETURNING id`).Scan(&installationID); err != nil {
		t.Fatalf("seed empty installation: %v", err)
	}

	// When
	err := NewInstallationStore(pool).Delete(ctx, installationID)

	// Then
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestInstallationStoreDisable_revokesProviderSessionsAndDemotesExternalUsers(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	provider, err := models.NewPluginSessionProviderKey(installationID, "ldap")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET role = 'admin', access_group_id = NULL WHERE id = $1`, userID); err != nil {
		t.Fatalf("promote external user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) VALUES ($1, $2, 'ldap')`, userID, installationID); err != nil {
		t.Fatalf("seed external authorization: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at, provider_key) VALUES ('disable-installation', $1, $2, $3)`, userID, time.Now().Add(time.Hour), provider.String()); err != nil {
		t.Fatalf("seed provider session: %v", err)
	}
	t.Cleanup(func() {
		for _, cleanup := range []struct {
			query string
			arg   int
		}{
			{`DELETE FROM external_authorization_audit WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM external_authorization_states WHERE plugin_installation_id = $1`, installationID},
			{`DELETE FROM auth_sessions WHERE user_id = $1`, userID},
		} {
			if _, err := pool.Exec(context.Background(), cleanup.query, cleanup.arg); err != nil {
				t.Errorf("cleanup lifecycle fixture: %v", err)
			}
		}
	})

	// When
	err = NewInstallationStore(pool).Disable(ctx, installationID)

	// Then
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM plugin_installations WHERE id = $1`, installationID).Scan(&enabled); err != nil {
		t.Fatalf("load installation: %v", err)
	}
	if enabled {
		t.Fatal("installation remains enabled")
	}
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = 'disable-installation'`).Scan(&revokedAt); err != nil {
		t.Fatalf("load session: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("provider session was not revoked")
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); err != nil {
		t.Fatalf("load user: %v", err)
	}
	if role != "user" {
		t.Fatalf("user role = %q, want user", role)
	}
	if err := NewInstallationStore(pool).Disable(ctx, installationID); err != nil {
		t.Fatalf("second Disable() error = %v", err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM external_authorization_audit WHERE plugin_installation_id = $1 AND reason = 'installation_disabled'`, installationID).Scan(&audits); err != nil {
		t.Fatalf("count disable audits: %v", err)
	}
	if audits != 1 {
		t.Fatalf("disable audits = %d, want 1 after repeat", audits)
	}
}

func TestInstallationStoreVersionRoundTrip_preservesAuthAndRuntimeData(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('version-round-trip', $1, 'Version Round Trip', true)`, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if err := NewRuntimeConfigStore(pool).PutGlobalConfig(ctx, installationID, "settings", map[string]any{"endpoint": "https://example.invalid"}); err != nil {
		t.Fatalf("seed runtime config: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'ldap', true)`, installationID); err != nil {
		t.Fatalf("seed auth binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'version-round-trip', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'version-round-trip', 'user')`, installationID); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'ldap', 'user', 'user', '{}', 'version_round_trip', gen_random_uuid())`, userID, installationID); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM external_authorization_audit WHERE plugin_installation_id = $1`, installationID); err != nil {
			t.Errorf("cleanup version-round-trip audit: %v", err)
		}
	})

	// When
	upgrade := "2.0.0"
	if err := NewInstallationStore(pool).Update(ctx, installationID, UpdateInstallationInput{Version: &upgrade}); err != nil {
		t.Fatalf("upgrade version: %v", err)
	}
	downgrade := "1.0.0"
	if err := NewInstallationStore(pool).Update(ctx, installationID, UpdateInstallationInput{Version: &downgrade}); err != nil {
		t.Fatalf("downgrade version: %v", err)
	}

	// Then
	var storedID int
	var version string
	var configs, bindings, identities, mappings, audits, users, profiles int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT id FROM plugin_installations WHERE id = $1),
			(SELECT version FROM plugin_installations WHERE id = $1),
			(SELECT COUNT(*) FROM plugin_runtime_configs WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM plugin_auth_bindings WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM external_authorization_audit WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM users WHERE id = $2),
			(SELECT COUNT(*) FROM user_profiles WHERE user_id = $2)`, installationID, userID,
	).Scan(&storedID, &version, &configs, &bindings, &identities, &mappings, &audits, &users, &profiles); err != nil {
		t.Fatalf("query durable version data: %v", err)
	}
	if storedID != installationID || version != downgrade {
		t.Fatalf("installation after round trip = id:%d version:%q, want id:%d version:%q", storedID, version, installationID, downgrade)
	}
	if configs != 1 || bindings != 1 || identities != 1 || mappings != 1 || audits != 1 || users != 1 || profiles != 1 {
		t.Fatalf("durable rows after version round trip = configs:%d bindings:%d identities:%d mappings:%d audits:%d users:%d profiles:%d, want one each", configs, bindings, identities, mappings, audits, users, profiles)
	}
}
