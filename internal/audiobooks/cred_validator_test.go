package audiobooks

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSiloCredValidator_Validate_UsesConfiguredProviderFallback(t *testing.T) {
	// Given
	ctx, pool := newBoundaryAuthDatabase(t)
	identity := uuid.NewString()
	providerUser, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email: "ldap-boundary-" + identity + "@example.invalid", Username: "ldap-user-" + identity, Password: "unused", Role: "user",
	})
	if err != nil {
		t.Fatalf("create provider user: %v", err)
	}
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, $3, TRUE)`, profileID, providerUser.ID, "ldap-profile"); err != nil {
		t.Fatalf("create provider profile: %v", err)
	}
	installationID := seedBoundaryPlugin(t, ctx, pool, providerUser.ID)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, providerUser.ID); err != nil {
			t.Errorf("delete provider user: %v", err)
		}
	})
	plugin := auth.NewPluginProviderWithClientFactory(
		auth.PluginProviderConfig{InstallationID: installationID, CapabilityID: "ldap"},
		auth.NewSessionRepository(pool), auth.NewUserRepository(pool), pool,
		func(context.Context) (auth.PluginAuthClient, error) {
			return boundaryPluginClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "boundary-subject"}}, nil
		},
	)
	service := newBoundaryAuthService(pool, boundaryAuthProvider{err: auth.ErrAccountNotFound}, plugin, `["ldap","local"]`)

	// When
	userID, _, displayName, err := (&SiloCredValidator{Auth: service}).Validate(ctx, "directory-user", "password")

	// Then
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if userID != strconv.Itoa(providerUser.ID) {
		t.Fatalf("Validate() userID = %q, want provider user %d", userID, providerUser.ID)
	}
	if displayName != providerUser.Username {
		t.Fatalf("Validate() displayName = %q, want provider user %q", displayName, providerUser.Username)
	}
}

type boundaryAuthProvider struct {
	user *models.User
	err  error
}

func (p boundaryAuthProvider) Authenticate(context.Context, auth.Credentials) (*models.User, error) {
	return p.user, p.err
}

func (boundaryAuthProvider) ValidateSession(context.Context, string) (bool, error) {
	return false, nil
}

type boundarySettingsGetter struct{ value string }

func (s boundarySettingsGetter) Get(context.Context, string) (string, error) { return s.value, nil }

func newBoundaryAuthDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func newBoundaryAuthService(pool *pgxpool.Pool, local, ldap auth.AuthProvider, policy string) *auth.Service {
	service := auth.NewService(
		local,
		auth.NewJWTService("test-secret-32-bytes-long!!!!!", time.Minute, time.Hour),
		auth.NewSessionRepository(pool),
		auth.NewUserRepository(pool),
		nil,
		boundarySettingsGetter{value: policy},
		nil,
	)
	service.RegisterProvider(auth.LoginProviderInfo{ID: "ldap", Mode: "credentials"}, ldap)
	return service
}

type boundaryPluginClient struct {
	response *pluginv1.AuthenticateResponse
}

func (c boundaryPluginClient) Authenticate(context.Context, *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	return c.response, nil
}

func (boundaryPluginClient) InitAuthorize(context.Context, *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, auth.ErrInvalidCredentials
}

func (boundaryPluginClient) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, auth.ErrInvalidCredentials
}

func seedBoundaryPlugin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int) int {
	t.Helper()
	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ($1, 'test', '/nonexistent') RETURNING id`, "boundary-auth-"+uuid.NewString()).Scan(&installationID); err != nil {
		t.Fatalf("create plugin installation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'ldap', TRUE)`, installationID); err != nil {
		t.Fatalf("create plugin auth binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'boundary-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("create plugin identity: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID); err != nil {
			t.Errorf("delete plugin installation: %v", err)
		}
	})
	return installationID
}

func TestSplitUserProfile(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantUser    string
		wantProfile string
	}{
		{
			name:        "plain username",
			input:       "alice",
			wantUser:    "alice",
			wantProfile: "",
		},
		{
			name:        "user#profile",
			input:       "alice#kids",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trims surrounding whitespace",
			input:       "  alice#kids  ",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trims inner whitespace around hash",
			input:       "alice # kids",
			wantUser:    "alice",
			wantProfile: "kids",
		},
		{
			name:        "trailing hash with no profile collapses to plain user",
			input:       "alice#",
			wantUser:    "alice",
			wantProfile: "",
		},
		{
			name:        "empty input stays empty",
			input:       "",
			wantUser:    "",
			wantProfile: "",
		},
		{
			name: "multiple hashes — split on the LAST one so a profile name " +
				"that legitimately starts with a hash (rare but possible) " +
				"doesn't get misinterpreted as the user portion",
			input:       "alice#kids#beta",
			wantUser:    "alice#kids",
			wantProfile: "beta",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotUser, gotProfile := splitUserProfile(tc.input)
			if gotUser != tc.wantUser {
				t.Errorf("user = %q, want %q", gotUser, tc.wantUser)
			}
			if gotProfile != tc.wantProfile {
				t.Errorf("profile = %q, want %q", gotProfile, tc.wantProfile)
			}
		})
	}
}
