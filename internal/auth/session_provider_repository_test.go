package auth

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAuthSessionProviderProvenance(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	userID := insertPluginProviderTestUser(t, ctx, pool, "session-provider")
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'ldap', true)`, installationID); err != nil {
		t.Fatalf("seed auth binding: %v", err)
	}
	repository := NewSessionRepository(pool)
	local := models.LocalSessionProviderKey()
	plugin, err := models.NewPluginSessionProviderKey(installationID, "ldap")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
	}
	expiresAt := time.Now().Add(time.Hour)
	for _, session := range []models.AuthSession{
		{ID: "local-provider-session", UserID: userID, ExpiresAt: expiresAt, ProviderKey: &local},
		{ID: "plugin-provider-session", UserID: userID, ExpiresAt: expiresAt, ProviderKey: &plugin},
	} {
		if err := repository.Create(ctx, session); err != nil {
			t.Fatalf("create %s session: %v", session.ID, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ('legacy-provider-session', $1, $2)`, userID, expiresAt); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	// When
	sessions, err := repository.ListByUser(ctx, userID)

	// Then
	if err != nil {
		t.Fatalf("ListByUser() error = %v", err)
	}
	got := map[string]*models.SessionProviderKey{}
	for _, session := range sessions {
		got[session.ID] = session.ProviderKey
	}
	if got["local-provider-session"] == nil || got["local-provider-session"].String() != "local" {
		t.Fatalf("local session provenance = %v", got["local-provider-session"])
	}
	if got["plugin-provider-session"] == nil || got["plugin-provider-session"].String() != plugin.String() {
		t.Fatalf("plugin session provenance = %v", got["plugin-provider-session"])
	}
	if got["legacy-provider-session"] != nil {
		t.Fatalf("legacy session provenance = %v, want nil", got["legacy-provider-session"])
	}
}

func TestLegacySessionProvenance(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	userID := insertPluginProviderTestUser(t, ctx, pool, "legacy-session")
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ('legacy-session-provenance', $1, NOW() + INTERVAL '1 hour')`, userID); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	repository := NewSessionRepository(pool)

	// When
	valid, err := repository.IsValid(ctx, "legacy-session-provenance")

	// Then
	if err != nil {
		t.Fatalf("IsValid() error = %v", err)
	}
	if !valid {
		t.Fatal("legacy null-provenance session became invalid")
	}
}
