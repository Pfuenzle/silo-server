package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type mockAuthProvider struct {
	user *models.User
	err  error
}

func (m *mockAuthProvider) Authenticate(_ context.Context, _ Credentials) (*models.User, error) {
	return m.user, m.err
}

func (m *mockAuthProvider) ValidateSession(_ context.Context, _ string) (bool, error) {
	return false, nil
}

type mockSettingsStore struct {
	values map[string]string
}

func (m *mockSettingsStore) Get(_ context.Context, key string) (string, error) {
	return m.values[key], nil
}

func (m *mockSettingsStore) Set(_ context.Context, key, value string) error {
	if m.values == nil {
		m.values = make(map[string]string)
	}
	m.values[key] = value
	return nil
}

func newTestService(providers map[string]AuthProvider, metadata map[string]LoginProviderInfo, defaultID string, settings map[string]string) *Service {
	return &Service{
		provider:  nil,
		jwt:       NewJWTService("test-secret-32-bytes-long!!!!!", 15*time.Minute, 24*time.Hour),
		providers: providers,
		metadata:  metadata,
		defaultID: defaultID,
		settings:  &mockSettingsStore{values: settings},
	}
}

// Given: policy is ["local"], local returns invalid-credentials
// When:  login without explicit provider
// Then:  stops immediately with ErrInvalidCredentials, does NOT fall back
func TestLoginWithFallback_InvalidPassword_StopsImmediately(t *testing.T) {
	local := &mockAuthProvider{err: ErrInvalidCredentials}
	svc := newTestService(
		map[string]AuthProvider{"local": local},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		map[string]string{SettingKeyCredentialProviderFallback: `["local"]`},
	)

	_, _, err := svc.loginWithFallback(context.Background(),
		MustParseCredentialProviderPolicy(`["local"]`, svc.metadata),
		"testuser", "wrongpass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for invalid password")
	}
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}
}

// Given: policy is ["local"], local returns account-not-found
// When:  login without explicit provider
// Then:  returns ErrInvalidCredentials (all providers exhausted, account unknown)
func TestLoginWithFallback_AllProvidersReturnAccountNotFound_ReturnsInvalidCredentials(t *testing.T) {
	local := &mockAuthProvider{err: ErrAccountNotFound}
	svc := newTestService(
		map[string]AuthProvider{"local": local},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		map[string]string{SettingKeyCredentialProviderFallback: `["local"]`},
	)

	_, _, err := svc.loginWithFallback(context.Background(),
		MustParseCredentialProviderPolicy(`["local"]`, svc.metadata),
		"testuser", "pass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error when all providers return account not found")
	}
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials after all fallback exhausted, got: %v", err)
	}
}

// Given: policy is ["local"], local returns user-disabled
// When:  login without explicit provider
// Then:  stops immediately with ErrUserDisabled (non-fallback-eligible error)
func TestLoginWithFallback_UserDisabled_StopsImmediately(t *testing.T) {
	local := &mockAuthProvider{err: ErrUserDisabled}
	svc := newTestService(
		map[string]AuthProvider{"local": local},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		map[string]string{SettingKeyCredentialProviderFallback: `["local"]`},
	)

	_, _, err := svc.loginWithFallback(context.Background(),
		MustParseCredentialProviderPolicy(`["local"]`, svc.metadata),
		"testuser", "pass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for disabled user")
	}
	if !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("expected ErrUserDisabled, got: %v", err)
	}
}

// Given: policy is ["local"], local returns plugin-config-invalid
// When:  login without explicit provider
// Then:  stops immediately with ErrPluginConfigInvalid, does NOT fall back
func TestLoginWithFallback_PluginConfigInvalid_StopsImmediately(t *testing.T) {
	local := &mockAuthProvider{err: ErrPluginConfigInvalid}
	svc := newTestService(
		map[string]AuthProvider{"local": local},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		map[string]string{SettingKeyCredentialProviderFallback: `["local"]`},
	)

	_, _, err := svc.loginWithFallback(context.Background(),
		MustParseCredentialProviderPolicy(`["local"]`, svc.metadata),
		"testuser", "pass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for plugin config invalid")
	}
	if !errors.Is(err, ErrPluginConfigInvalid) {
		t.Fatalf("expected ErrPluginConfigInvalid, got: %v", err)
	}
}

// Given: empty policy (no providers to try)
// When:  loginWithFallback with empty policy
// Then:  returns ErrInvalidCredentials
func TestLoginWithFallback_EmptyPolicy_ReturnsInvalidCredentials(t *testing.T) {
	svc := newTestService(
		map[string]AuthProvider{"local": &mockAuthProvider{user: &models.User{ID: 1}}},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		nil,
	)

	_, _, err := svc.loginWithFallback(context.Background(),
		CredentialProviderPolicy{},
		"testuser", "pass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for empty policy")
	}
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}
}

// Given: policy is ["oidc","local"] but oidc is OAuth mode
// When:  ParseCredentialProviderPolicy is called
// Then:  returns error because OAuth providers cannot be in fallback order
func TestParseCredentialProviderPolicy_OAuthProvider_Rejected(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"oidc":  {ID: "oidc", Mode: "oauth"},
		"local": {ID: "local", Mode: "credentials"},
	}
	_, err := ParseCredentialProviderPolicy(`["oidc","local"]`, registered)
	if err == nil {
		t.Fatal("expected error for OAuth provider in fallback order")
	}
}

// Given: explicit provider is "local" (bypasses fallback)
// When:  loginWithProvider with explicit provider that returns account-not-found
// Then:  returns ErrAccountNotFound directly (no fallback for explicit selection)
func TestLoginWithProvider_ExplicitProvider_NoFallback(t *testing.T) {
	local := &mockAuthProvider{err: ErrAccountNotFound}
	svc := newTestService(
		map[string]AuthProvider{"local": local},
		map[string]LoginProviderInfo{"local": {ID: "local", Mode: "credentials"}},
		"local",
		map[string]string{SettingKeyCredentialProviderFallback: `["local"]`},
	)

	_, _, err := svc.loginWithProvider(context.Background(), "local", "testuser", "pass", "test", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error when explicit provider returns account not found")
	}
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound (no fallback for explicit), got: %v", err)
	}
}

// Given: policy with OAuth provider mixed in
// When:  ParseCredentialProviderPolicy is called
// Then:  returns error because OAuth providers cannot be in fallback order
func TestLoginWithFallback_OAuthProviderInPolicy_RejectedByPolicy(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"oidc":  {ID: "oidc", Mode: "oauth"},
		"local": {ID: "local", Mode: "credentials"},
	}
	_, err := ParseCredentialProviderPolicy(`["oidc","local"]`, registered)
	if err == nil {
		t.Fatal("expected policy validation to reject OAuth provider")
	}
}

// Given: policy references "nonexistent" which is not registered
// When:  ParseCredentialProviderPolicy is called
// Then:  returns error because unknown providers are rejected at parse time
func TestParseCredentialProviderPolicy_UnknownProvider_RejectedAtParse(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"local": {ID: "local", Mode: "credentials"},
	}
	_, err := ParseCredentialProviderPolicy(`["nonexistent","local"]`, registered)
	if err == nil {
		t.Fatal("expected policy validation to reject unknown provider")
	}
}
