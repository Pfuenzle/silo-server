package plugins

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDisableAuthBindingRevokesProviderSessions(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	store := NewRuntimeConfigStore(pool)
	provider, err := models.NewPluginSessionProviderKey(installationID, "ldap")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
	}
	userID := seedAuthBindingRevocationUser(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('binding-revocation', $1, 'Binding revocation', true)`, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'binding-revocation-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	seedAuthBindingRevocationSession(t, pool, userID, "provider", provider.String())
	seedAuthBindingRevocationSession(t, pool, userID, "local", "local")
	seedAuthBindingRevocationSession(t, pool, userID, "legacy", "")
	if err := store.UpsertAuthBinding(ctx, userID, AuthBinding{InstallationID: installationID, CapabilityID: "ldap", Enabled: true}); err != nil {
		t.Fatalf("enable binding: %v", err)
	}

	// When
	err = store.UpsertAuthBinding(ctx, userID, AuthBinding{InstallationID: installationID, CapabilityID: "ldap", Enabled: false})

	// Then
	if err != nil {
		t.Fatalf("disable binding: %v", err)
	}
	for _, check := range []struct {
		id      string
		revoked bool
	}{
		{id: "provider", revoked: true},
		{id: "local", revoked: false},
		{id: "legacy", revoked: false},
	} {
		var revokedAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, check.id).Scan(&revokedAt); err != nil {
			t.Fatalf("load %s session: %v", check.id, err)
		}
		if (revokedAt != nil) != check.revoked {
			t.Fatalf("%s revoked = %t, want %t", check.id, revokedAt != nil, check.revoked)
		}
	}
	var users, profiles, identities int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE id = $1), (SELECT COUNT(*) FROM user_profiles WHERE user_id = $1), (SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $2 AND user_id = $1)`, userID, installationID).Scan(&users, &profiles, &identities); err != nil {
		t.Fatalf("count durable auth rows: %v", err)
	}
	if users != 1 || profiles != 1 || identities != 1 {
		t.Fatalf("durable auth rows after disable: users=%d profiles=%d identities=%d", users, profiles, identities)
	}
}

func seedAuthBindingRevocationUser(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var userID int
	label := fmt.Sprintf("binding-revocation-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (email, username, password_hash, role)
		VALUES ($1, $2, 'unused', 'user')
		RETURNING id`, label+"@example.invalid", label).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("cleanup user %d: %v", userID, err)
		}
	})
	return userID
}

func seedAuthBindingRevocationSession(t *testing.T, pool *pgxpool.Pool, userID int, id, providerKey string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO auth_sessions (id, user_id, expires_at, provider_key)
		VALUES ($1, $2, NOW() + INTERVAL '1 hour', NULLIF($3, ''))`, id, userID, providerKey)
	if err != nil {
		t.Fatalf("seed %s session: %v", id, err)
	}
}
