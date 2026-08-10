package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestCompleteOAuthLoginWritesPluginSessionProvenance(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "oauth-provenance")
	seedEnabledAuthBinding(t, ctx, pool, installationID, "oauth provider")
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'oauth-provenance-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed OAuth identity: %v", err)
	}
	provider := newProvenancePluginProvider(pool, installationID, "oauth provider", "oauth-provenance-subject")
	service := NewService(nil, NewJWTService("oauth-provenance", time.Minute, time.Hour), NewSessionRepository(pool), NewUserRepository(pool), nil, nil, nil)
	service.RegisterProvider(LoginProviderInfo{ID: "plugin-oauth", Mode: "oauth", InstallationID: installationID}, provider)

	// When
	_, user, err := service.CompleteOAuthLogin(ctx, OAuthLoginInput{
		InstallationID: installationID,
		CapabilityID:   "oauth provider",
		Response:       &pluginv1.AuthenticateResponse{ExternalSubject: "oauth-provenance-subject"},
	})

	// Then
	if err != nil {
		t.Fatalf("CompleteOAuthLogin() error: %v", err)
	}
	var providerKey string
	if err := pool.QueryRow(ctx, `SELECT provider_key FROM auth_sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&providerKey); err != nil {
		t.Fatalf("query OAuth session provenance: %v", err)
	}
	want, err := models.NewPluginSessionProviderKey(installationID, "oauth provider")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
	}
	if providerKey != want.String() {
		t.Fatalf("OAuth session provider key = %q, want %q", providerKey, want.String())
	}
}

func TestPluginLoginAndDisableLeaveNoActiveProviderSessions(t *testing.T) {
	for _, ordering := range []string{"login before disable", "disable before login", "concurrent"} {
		t.Run(ordering, func(t *testing.T) {
			// Given
			ctx, pool := newPluginProviderDBTest(t)
			installationID := insertPluginProviderTestInstallation(t, ctx, pool)
			userID := insertPluginProviderTestUser(t, ctx, pool, "disable-race")
			const capabilityID = "race provider"
			seedEnabledAuthBinding(t, ctx, pool, installationID, capabilityID)
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'disable-race-subject', $2)`, installationID, userID); err != nil {
				t.Fatalf("seed plugin identity: %v", err)
			}
			provider := newProvenancePluginProvider(pool, installationID, capabilityID, "disable-race-subject")
			sessions := NewSessionRepository(pool)
			service := NewService(nil, NewJWTService("disable-race", time.Minute, time.Hour), sessions, NewUserRepository(pool), nil, nil, nil)
			service.RegisterProvider(LoginProviderInfo{ID: "plugin-race", InstallationID: installationID}, provider)
			store := plugins.NewRuntimeConfigStore(pool)
			disable := func() error {
				return store.UpsertAuthBinding(ctx, userID, plugins.AuthBinding{InstallationID: installationID, CapabilityID: capabilityID, Enabled: false})
			}
			login := func() error {
				_, _, err := service.LoginWithProvider(ctx, "plugin-race", "user", "password", "race", "127.0.0.1")
				return err
			}

			// When
			switch ordering {
			case "login before disable":
				if err := login(); err != nil {
					t.Fatalf("login before disable: %v", err)
				}
				if err := disable(); err != nil {
					t.Fatalf("disable after login: %v", err)
				}
			case "disable before login":
				if err := disable(); err != nil {
					t.Fatalf("disable before login: %v", err)
				}
				if err := login(); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("login after disable error = %v, want ErrInvalidCredentials", err)
				}
			default:
				start := make(chan struct{})
				var group sync.WaitGroup
				var loginErr, disableErr error
				group.Add(2)
				go func() { defer group.Done(); <-start; loginErr = login() }()
				go func() { defer group.Done(); <-start; disableErr = disable() }()
				close(start)
				group.Wait()
				if disableErr != nil {
					t.Fatalf("concurrent disable: %v", disableErr)
				}
				if loginErr != nil && !errors.Is(loginErr, ErrInvalidCredentials) {
					t.Fatalf("concurrent login error = %v", loginErr)
				}
			}

			// Then
			providerKey, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if err != nil {
				t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
			}
			var activeSessions int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE provider_key = $1 AND revoked_at IS NULL`, providerKey.String()).Scan(&activeSessions); err != nil {
				t.Fatalf("count active provider sessions: %v", err)
			}
			if activeSessions != 0 {
				t.Fatalf("active provider sessions after disable = %d, want 0", activeSessions)
			}
		})
	}
}

func TestInstallationDisableLeavesNoActiveCredentialOrOAuthProviderSession(t *testing.T) {
	for _, flow := range []string{"credential", "oauth"} {
		t.Run(flow, func(t *testing.T) {
			// Given
			ctx, pool := newPluginProviderDBTest(t)
			installationID := insertPluginProviderTestInstallation(t, ctx, pool)
			userID := insertPluginProviderTestUser(t, ctx, pool, "installation-disable-"+flow)
			const capabilityID = "race provider"
			const subject = "installation-disable-subject"
			seedEnabledAuthBinding(t, ctx, pool, installationID, capabilityID)
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, subject, userID); err != nil {
				t.Fatalf("seed plugin identity: %v", err)
			}
			provider := newProvenancePluginProvider(pool, installationID, capabilityID, subject)
			complete := func() error {
				session := models.AuthSession{ExpiresAt: time.Now().Add(time.Hour)}
				if flow == "credential" {
					_, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: "user", Password: "password"}, session)
					return err
				}
				_, err := provider.CompleteOAuthAndComplete(ctx, &pluginv1.AuthenticateResponse{ExternalSubject: subject}, session)
				return err
			}

			// When
			start := make(chan struct{})
			var completionErr, disableErr error
			var group sync.WaitGroup
			group.Add(2)
			go func() { defer group.Done(); <-start; completionErr = complete() }()
			go func() {
				defer group.Done()
				<-start
				disableErr = plugins.NewInstallationStore(pool).Disable(ctx, installationID)
			}()
			close(start)
			group.Wait()

			// Then
			if disableErr != nil {
				t.Fatalf("Disable() error = %v", disableErr)
			}
			if completionErr != nil && !errors.Is(completionErr, ErrInvalidCredentials) {
				t.Fatalf("completion error = %v, want nil or ErrInvalidCredentials", completionErr)
			}
			providerKey, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if err != nil {
				t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
			}
			var activeSessions int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE provider_key = $1 AND revoked_at IS NULL`, providerKey.String()).Scan(&activeSessions); err != nil {
				t.Fatalf("count active provider sessions: %v", err)
			}
			if activeSessions != 0 {
				t.Fatalf("active provider sessions after Disable() = %d, want 0", activeSessions)
			}
		})
	}
}

func TestPluginLoginAndMappingRemovalLeaveNoElevatedSession(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "mapping-race")
	const capabilityID = "ldap"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, $2, true, 'external_groups_v1')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed authoritative binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'mapping-race-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed plugin identity: %v", err)
	}
	mappings := plugins.NewAuthGroupMappingStore(pool)
	admin := "admin"
	if _, err := mappings.Replace(ctx, installationID, []plugins.AuthGroupMappingInput{{ExternalGroupID: "admins", TargetRole: &admin}}); err != nil {
		t.Fatalf("seed admin mapping: %v", err)
	}
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "mapping-race-subject",
		Claims:          mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}}),
	})
	sessions := NewSessionRepository(pool)
	service := NewService(nil, NewJWTService("mapping-race", time.Minute, time.Hour), sessions, NewUserRepository(pool), nil, nil, nil)
	service.RegisterProvider(LoginProviderInfo{ID: "plugin-mapping-race", InstallationID: installationID}, provider)
	start := make(chan struct{})
	var wait sync.WaitGroup
	var loginErr, replaceErr error
	wait.Add(2)

	// When
	go func() {
		defer wait.Done()
		<-start
		_, _, loginErr = service.LoginWithProvider(ctx, "plugin-mapping-race", "user", "password", "race", "127.0.0.1")
	}()
	go func() {
		defer wait.Done()
		<-start
		_, replaceErr = mappings.Replace(ctx, installationID, nil)
	}()
	close(start)
	wait.Wait()

	// Then
	if loginErr != nil {
		t.Fatalf("concurrent login: %v", loginErr)
	}
	if replaceErr != nil {
		t.Fatalf("concurrent mapping removal: %v", replaceErr)
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); err != nil {
		t.Fatalf("load user after concurrent mapping removal: %v", err)
	}
	if role != "user" {
		t.Fatalf("role after concurrent mapping removal = %q, want user", role)
	}
}

func seedEnabledAuthBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, installationID int, capabilityID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, $2, true)`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled auth binding: %v", err)
	}
}

func newProvenancePluginProvider(pool *pgxpool.Pool, installationID int, capabilityID, subject string) *PluginProvider {
	return NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID},
		NewSessionRepository(pool),
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject}}, nil
		},
	)
}
