package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pluginProviderTestClient struct {
	response *pluginv1.AuthenticateResponse
	err      error
}

func (c pluginProviderTestClient) Authenticate(
	context.Context,
	*pluginv1.AuthenticateRequest,
) (*pluginv1.AuthenticateResponse, error) {
	return c.response, c.err
}

func TestPluginProvider_MapsPluginUnauthenticatedToInvalidCredentials(t *testing.T) {
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: 1, AutoProvision: true},
		nil,
		nil,
		nil,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{err: status.Error(codes.Unauthenticated, "invalid credentials")}, nil
		},
	)

	_, err := provider.Authenticate(context.Background(), Credentials{Username: "user", Password: "password"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
}

func (pluginProviderTestClient) InitAuthorize(
	context.Context,
	*pluginv1.InitAuthorizeRequest,
) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (pluginProviderTestClient) ExchangeCode(
	context.Context,
	*pluginv1.ExchangeCodeRequest,
) (*pluginv1.AuthenticateResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestRandomPluginOnlyPasswordFitsBcryptLimit(t *testing.T) {
	password, err := randomPluginOnlyPassword()
	if err != nil {
		t.Fatalf("randomPluginOnlyPassword() error = %v", err)
	}

	if len(password) > 72 {
		t.Fatalf("password length = %d, want <= 72", len(password))
	}
	if _, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost); err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", err)
	}
}

func TestPluginProvider_ExistingIdentityReturnsCurrentOwner(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	ownerID := insertPluginProviderTestUser(t, ctx, pool, "owner")
	subject := "existing-subject"
	_, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3)`,
		installationID,
		subject,
		ownerID,
	)
	if err != nil {
		t.Fatalf("seed plugin identity: %v", err)
	}
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, AutoProvision: true},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject}}, nil
		},
	)

	// When
	user, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	if user.ID != ownerID {
		t.Fatalf("Authenticate() user ID = %d, want existing owner %d", user.ID, ownerID)
	}
}

func TestPluginProvider_RejectsEmptyExternalSubject(t *testing.T) {
	// Given
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: 1, AutoProvision: true},
		nil,
		nil,
		nil,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{}}, nil
		},
	)

	// When
	_, err := provider.Authenticate(context.Background(), Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestPluginProvider_RejectsWhitespaceExternalSubject(t *testing.T) {
	// Given
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: 1, AutoProvision: true},
		nil,
		nil,
		nil,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: " \t "}}, nil
		},
	)

	// When
	_, err := provider.CompleteOAuth(context.Background(), &pluginv1.AuthenticateResponse{ExternalSubject: " \n "})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("CompleteOAuth() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestPluginProvider_RejectsWhitespaceCredentialSubject(t *testing.T) {
	// Given
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: 1, AutoProvision: true},
		nil,
		nil,
		nil,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: " \t "}}, nil
		},
	)

	// When
	_, err := provider.Authenticate(context.Background(), Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestPluginProvider_DisabledBindingRejectsCredentialAndOAuthLoginGenerically(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled)
		VALUES ($1, 'ldap', false)`, installationID); err != nil {
		t.Fatalf("seed disabled auth binding: %v", err)
	}
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, CapabilityID: "ldap", AutoProvision: true},
		nil,
		nil,
		pool,
		func(context.Context) (pluginAuthClient, error) {
			t.Fatalf("disabled binding called plugin client")
			return nil, nil
		},
	)

	// When
	_, credentialErr := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})
	_, oauthErr := provider.CompleteOAuth(ctx, &pluginv1.AuthenticateResponse{ExternalSubject: "subject"})

	// Then
	if !errors.Is(credentialErr, ErrInvalidCredentials) {
		t.Fatalf("credential error = %v, want ErrInvalidCredentials", credentialErr)
	}
	if !errors.Is(oauthErr, ErrInvalidCredentials) {
		t.Fatalf("oauth error = %v, want ErrInvalidCredentials", oauthErr)
	}
}

func TestPluginProvider_DisabledInstallationRejectsCredentialAndOAuthCompletionGenerically(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "disabled-installation")
	const capabilityID = "ldap"
	const subject = "disabled-installation-subject"
	seedEnabledAuthBinding(t, ctx, pool, installationID, capabilityID)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, subject, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, installationID); err != nil {
		t.Fatalf("disable installation: %v", err)
	}
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID},
		NewSessionRepository(pool),
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject}}, nil
		},
	)

	// When
	_, credentialErr := provider.AuthenticateAndComplete(ctx, Credentials{Username: "user", Password: "password"}, models.AuthSession{ExpiresAt: time.Now().Add(time.Hour)})
	_, oauthErr := provider.CompleteOAuthAndComplete(ctx, &pluginv1.AuthenticateResponse{ExternalSubject: subject}, models.AuthSession{ExpiresAt: time.Now().Add(time.Hour)})

	// Then
	if !errors.Is(credentialErr, ErrInvalidCredentials) {
		t.Fatalf("credential error = %v, want ErrInvalidCredentials", credentialErr)
	}
	if !errors.Is(oauthErr, ErrInvalidCredentials) {
		t.Fatalf("oauth error = %v, want ErrInvalidCredentials", oauthErr)
	}
	var activeSessions int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&activeSessions); err != nil {
		t.Fatalf("count active sessions: %v", err)
	}
	if activeSessions != 0 {
		t.Fatalf("active sessions = %d, want 0", activeSessions)
	}
}

func newPluginProviderDBTest(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func insertPluginProviderTestInstallation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var installationID int
	pluginID := fmt.Sprintf("test.auth-plugin-provider-%d", time.Now().UnixNano())
	err := pool.QueryRow(ctx, `
		INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '0', '/nonexistent/auth-plugin-provider')
		RETURNING id`,
		pluginID,
	).Scan(&installationID)
	if err != nil {
		t.Fatalf("seed plugin installation: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderInstallation(t, ctx, pool, installationID)
	})
	return installationID
}

func insertPluginProviderTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) int {
	t.Helper()
	username := fmt.Sprintf("test-auth-plugin-provider-%s-%d", label, time.Now().UnixNano())
	user, err := NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email:    username + "@example.invalid",
		Username: username,
		Password: "plugin-provider-test-password",
		Role:     "user",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, ctx, pool, user.ID)
	})
	return user.ID
}
