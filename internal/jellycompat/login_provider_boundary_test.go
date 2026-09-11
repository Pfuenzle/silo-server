package jellycompat

import (
	"context"
	"os"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestLoginResolver_Resolve_UsesConfiguredProviderAndPreservesProfile(t *testing.T) {
	// Given
	ctx, pool := newJellycompatBoundaryAuthDatabase(t)
	identity := uuid.NewString()
	providerUser, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email: "jelly-boundary-" + identity + "@example.invalid", Username: "ldap-user-" + identity, Password: "unused", Role: "user",
	})
	if err != nil {
		t.Fatalf("create provider user: %v", err)
	}
	installationID := seedJellycompatBoundaryPlugin(t, ctx, pool, providerUser.ID, "boundary-subject")
	service := newJellycompatBoundaryAuthService(pool, installationID, "boundary-subject", auth.ErrAccountNotFound)
	storeProvider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = storeProvider.Close() })
	store, err := storeProvider.ForUser(ctx, providerUser.ID)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "reader"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, providerUser.ID); err != nil {
			t.Errorf("delete provider user: %v", err)
		}
	})
	resolver := NewLoginResolver(
		service,
		storeProvider,
		NewSessionStore(time.Hour, func() time.Time { return time.Unix(100, 0) }),
		func() string { return "compat-token" },
		func() time.Time { return time.Unix(100, 0) },
	)

	// When
	session, err := resolver.Resolve(ctx, "directory-user#reader", "password", "test", "127.0.0.1")

	// Then
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if session.AccountUsername != providerUser.Username || session.ProfileID != "profile-1" {
		t.Fatalf("session account/profile = (%q, %q), want (%q, profile-1)", session.AccountUsername, session.ProfileID, providerUser.Username)
	}
}

func TestLoginResolver_Resolve_PasswordPINUsesConfiguredProvider(t *testing.T) {
	// Given
	ctx, pool := newJellycompatBoundaryAuthDatabase(t)
	identity := uuid.NewString()
	providerUser, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email: "jelly-pin-boundary-" + identity + "@example.invalid", Username: "ldap-pin-user-" + identity, Password: "unused", Role: "user",
	})
	if err != nil {
		t.Fatalf("create provider user: %v", err)
	}
	installationID := seedJellycompatBoundaryPlugin(t, ctx, pool, providerUser.ID, "pin-boundary-subject")
	service := newJellycompatBoundaryAuthService(pool, installationID, "pin-boundary-subject", auth.ErrAccountNotFound)
	storeProvider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = storeProvider.Close() })
	store, err := storeProvider.ForUser(ctx, providerUser.ID)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	pinHash, err := bcrypt.GenerateFromPassword([]byte("1234"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash profile PIN: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "reader", PINHash: string(pinHash)}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, providerUser.ID); err != nil {
			t.Errorf("delete provider user: %v", err)
		}
	})
	resolver := NewLoginResolver(
		service,
		storeProvider,
		NewSessionStore(time.Hour, func() time.Time { return time.Unix(100, 0) }),
		func() string { return "compat-token" },
		func() time.Time { return time.Unix(100, 0) },
	)

	// When
	validSession, validErr := resolver.Resolve(ctx, "directory-user#reader", "password#1234", "test", "127.0.0.1")
	wrongPINSession, wrongPINErr := resolver.Resolve(ctx, "directory-user#reader", "password#9999", "test", "127.0.0.1")
	missingPINSession, missingPINErr := resolver.Resolve(ctx, "directory-user#reader", "password", "test", "127.0.0.1")

	// Then
	if validErr != nil {
		t.Fatalf("valid PIN login error = %v", validErr)
	}
	if validSession.ProfileID != "profile-1" {
		t.Fatalf("valid PIN session profile = %q, want profile-1", validSession.ProfileID)
	}
	if wrongPINErr == nil || wrongPINSession != nil {
		t.Fatalf("wrong PIN login = (%v, %#v), want an error and no session", wrongPINErr, wrongPINSession)
	}
	if missingPINErr == nil || missingPINSession != nil {
		t.Fatalf("missing PIN login = (%v, %#v), want an error and no session", missingPINErr, missingPINSession)
	}
}

func newJellycompatBoundaryAuthDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func newJellycompatBoundaryAuthService(pool *pgxpool.Pool, installationID int, subject string, localErr error) *auth.Service {
	service := auth.NewService(
		boundaryAuthProvider{err: localErr},
		auth.NewJWTService("test-secret-32-bytes-long!!!!!", time.Minute, time.Hour),
		auth.NewSessionRepository(pool),
		auth.NewUserRepository(pool),
		nil,
		boundarySettingsGetter{value: `["local","ldap"]`},
		nil,
	)
	plugin := auth.NewPluginProviderWithClientFactory(
		auth.PluginProviderConfig{InstallationID: installationID, CapabilityID: "ldap"},
		auth.NewSessionRepository(pool), auth.NewUserRepository(pool), pool,
		func(context.Context) (auth.PluginAuthClient, error) {
			return boundaryPluginClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject}}, nil
		},
	)
	service.RegisterProvider(auth.LoginProviderInfo{ID: "ldap", Mode: "credentials"}, plugin)
	return service
}

type boundaryPluginClient struct {
	response *pluginv1.AuthenticateResponse
}

func (c boundaryPluginClient) Authenticate(_ context.Context, request *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	if request.GetPassword() != "password" {
		return nil, auth.ErrInvalidCredentials
	}
	return c.response, nil
}

func (boundaryPluginClient) InitAuthorize(context.Context, *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, auth.ErrInvalidCredentials
}

func (boundaryPluginClient) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, auth.ErrInvalidCredentials
}

func seedJellycompatBoundaryPlugin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int, subject string) int {
	t.Helper()
	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ($1, 'test', '/nonexistent') RETURNING id`, "jellycompat-boundary-"+uuid.NewString()).Scan(&installationID); err != nil {
		t.Fatalf("create plugin installation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'ldap', TRUE)`, installationID); err != nil {
		t.Fatalf("create plugin auth binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, subject, userID); err != nil {
		t.Fatalf("create plugin identity: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID); err != nil {
			t.Errorf("delete plugin installation: %v", err)
		}
	})
	return installationID
}

type boundaryAuthProvider struct {
	user     *models.User
	password string
	err      error
}

func (p boundaryAuthProvider) Authenticate(_ context.Context, credentials auth.Credentials) (*models.User, error) {
	if p.password != "" {
		if credentials.Password != p.password {
			return nil, p.err
		}
		return p.user, nil
	}
	return p.user, p.err
}

func (boundaryAuthProvider) ValidateSession(context.Context, string) (bool, error) {
	return false, nil
}

type boundarySettingsGetter struct{ value string }

func (s boundarySettingsGetter) Get(context.Context, string) (string, error) {
	return s.value, nil
}
