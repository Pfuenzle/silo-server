package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestExternalAuthorization_ModeRemovalDemotesAuditsAndRevokes(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "mode-removal")
	const capabilityID = "ldap"
	if _, err := pool.Exec(ctx, `UPDATE users SET role = 'admin', access_group_id = NULL WHERE id = $1`, userID); err != nil {
		t.Fatalf("promote external user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, $2, true, 'external_groups_v1')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed authoritative binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) VALUES ($1, $2, $3)`, userID, installationID, capabilityID); err != nil {
		t.Fatalf("seed externally managed user: %v", err)
	}
	providerKey, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
	}
	sessions := NewSessionRepository(pool)
	for _, key := range []*models.SessionProviderKey{nil, &providerKey} {
		if err := sessions.Create(ctx, models.AuthSession{UserID: userID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: key}); err != nil {
			t.Fatalf("seed auth session: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at) VALUES ($1, 'compat', ($2::bigint)::text, '{}', NOW() + INTERVAL '1 hour')`, uuid.NewString(), userID); err != nil {
		t.Fatalf("seed Jellycompat playback session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jellycompat_sessions (token, username, account_username, profile_id, profile_name, pseudo_user_id, streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at) VALUES ($1, 'mode-removal', 'mode-removal', 'profile', 'Profile', $2, $3, 'access', 'refresh', NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 hour')`, uuid.NewString(), uuid.New(), userID); err != nil {
		t.Fatalf("seed Jellycompat session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, 'mode-removal-token', 'mode-removal')`, userID); err != nil {
		t.Fatalf("seed ABS session: %v", err)
	}

	// When
	err = plugins.NewRuntimeConfigStore(pool).UpsertAuthBinding(ctx, userID, plugins.AuthBinding{
		InstallationID:    installationID,
		CapabilityID:      capabilityID,
		Enabled:           true,
		AuthorizationMode: plugins.AuthBindingAuthorizationModeNone,
	})

	// Then
	if err != nil {
		t.Fatalf("remove authorization mode: %v", err)
	}
	var role string
	var accessGroupID int64
	if err := pool.QueryRow(ctx, `SELECT role, access_group_id FROM users WHERE id = $1`, userID).Scan(&role, &accessGroupID); err != nil {
		t.Fatalf("load demoted user: %v", err)
	}
	var defaultGroupID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM access_groups WHERE is_default`).Scan(&defaultGroupID); err != nil {
		t.Fatalf("load default access group: %v", err)
	}
	if role != "user" || accessGroupID != defaultGroupID {
		t.Fatalf("mode removal user = role %q group %d, want user/default %d", role, accessGroupID, defaultGroupID)
	}
	assertExternalAuthorizationRevocation(t, ctx, pool, userID)
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM external_authorization_audit WHERE user_id = $1 ORDER BY id DESC LIMIT 1`, userID).Scan(&reason); err != nil {
		t.Fatalf("load mode-removal audit: %v", err)
	}
	if reason != "authorization_mode_removed" {
		t.Fatalf("mode-removal audit reason = %q, want authorization_mode_removed", reason)
	}
}
