package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestExternalAuthorization_PromoteDemoteAndAudit(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "external-authorization")
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("cleanup sessions: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM external_authorization_audit WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("cleanup authorization audit: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM external_authorization_states WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("cleanup authorization state: %v", err)
		}
	})
	const capabilityID = "ldap"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, $2, true, 'external_groups_v1')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed authoritative binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'external-authorization-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'admins', 'admin')`, installationID); err != nil {
		t.Fatalf("seed admin mapping: %v", err)
	}
	sessions := NewSessionRepository(pool)
	pluginKey, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
	}
	for _, key := range []*models.SessionProviderKey{nil, &pluginKey} {
		if err := sessions.Create(ctx, models.AuthSession{UserID: userID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: key}); err != nil {
			t.Fatalf("create old session: %v", err)
		}
	}
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AuthMode: "credentials"}, sessions, NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "external-authorization-subject", Claims: mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}})}}, nil
	})
	jwt := NewJWTService("external-authorization", time.Minute, time.Hour)
	service := NewService(nil, jwt, sessions, NewUserRepository(pool), nil, nil, nil)
	service.RegisterProvider(LoginProviderInfo{ID: "external-authorization", InstallationID: installationID}, provider)

	// When
	pair, user, err := service.LoginWithProvider(ctx, "external-authorization", "ignored", "ignored", "test", "127.0.0.1")

	// Then
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("role = %q, want admin", user.Role)
	}
	claims, err := jwt.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("validate promoted access token: %v", err)
	}
	freshSessionID := claims.SessionID
	if _, err := pool.Exec(ctx, `INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at) VALUES ($1, 'compat', ($2::bigint)::text, '{}', NOW() + INTERVAL '1 hour')`, uuid.NewString(), userID); err != nil {
		t.Fatalf("seed jellycompat playback session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jellycompat_sessions (token, username, account_username, profile_id, profile_name, pseudo_user_id, streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at) VALUES ($1, 'external', 'external', 'profile', 'Profile', $2, $3, 'access', 'refresh', NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 hour')`, uuid.NewString(), uuid.New(), userID); err != nil {
		t.Fatalf("seed jellycompat session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'external-authorization')`, userID, "external-authorization-token"); err != nil {
		t.Fatalf("seed ABS session: %v", err)
	}

	// When
	if _, err := plugins.NewAuthGroupMappingStore(pool).Replace(ctx, installationID, nil); err != nil {
		t.Fatalf("remove mappings: %v", err)
	}

	// Then
	valid, err := sessions.IsValid(ctx, freshSessionID)
	if err != nil || valid {
		t.Fatalf("removed-mapping session valid=%t err=%v, want false without error", valid, err)
	}
	var role string
	var accessGroupID int64
	if err := pool.QueryRow(ctx, `SELECT role, access_group_id FROM users WHERE id = $1`, userID).Scan(&role, &accessGroupID); err != nil {
		t.Fatalf("load demoted user: %v", err)
	}
	var defaultGroupID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM access_groups WHERE is_default`).Scan(&defaultGroupID); err != nil {
		t.Fatalf("load default group: %v", err)
	}
	if role != "user" || accessGroupID != defaultGroupID {
		t.Fatalf("demoted user = role %q group %d, want user/default %d", role, accessGroupID, defaultGroupID)
	}
	assertExternalAuthorizationRevocation(t, ctx, pool, userID)
	if _, err := service.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh() error = %v, want ErrSessionRevoked", err)
	}

	// When
	leastPrivilegeSessionID := uuid.NewString()
	user, err = provider.AuthenticateAndComplete(ctx, Credentials{Username: "ignored", Password: "ignored"}, models.AuthSession{
		ID: leastPrivilegeSessionID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &pluginKey,
	})

	// Then
	if err != nil || user.Role != "user" {
		t.Fatalf("least-privilege login user=%#v err=%v", user, err)
	}
	valid, err = sessions.IsValid(ctx, leastPrivilegeSessionID)
	if err != nil || !valid {
		t.Fatalf("least-privilege session valid=%t err=%v", valid, err)
	}
	rows, err := pool.Query(ctx, `SELECT reason FROM external_authorization_audit WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		t.Fatalf("load ordered audit: %v", err)
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatalf("scan audit reason: %v", err)
		}
		reasons = append(reasons, reason)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit reasons: %v", err)
	}
	wantReasons := []string{"ldap_login", "mapping_changed", "ldap_login"}
	if len(reasons) != len(wantReasons) {
		t.Fatalf("audit reasons = %#v, want %#v", reasons, wantReasons)
	}
	for index := range wantReasons {
		if reasons[index] != wantReasons[index] {
			t.Fatalf("audit reason[%d] = %q, want %q", index, reasons[index], wantReasons[index])
		}
	}
}

func assertExternalAuthorizationRevocation(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID int) {
	t.Helper()
	checks := []struct {
		name  string
		query string
	}{
		{name: "active auth sessions", query: `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL`},
		{name: "jellycompat playback sessions", query: `SELECT COUNT(*) FROM jellycompat_playback_sessions WHERE user_id = ($1::bigint)::text`},
		{name: "jellycompat sessions", query: `SELECT COUNT(*) FROM jellycompat_sessions WHERE streamapp_user_id = $1`},
		{name: "active ABS sessions", query: `SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`},
	}
	for _, check := range checks {
		var count int
		if err := pool.QueryRow(ctx, check.query, userID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after demotion = %d, %v; want zero", check.name, count, err)
		}
	}
}
