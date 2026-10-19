package watchsync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type authorizationCodeProviderStub struct {
	authProviderStub
	key               string
	startSession      AuthorizationCodeSession
	startedState      string
	startedRedirect   string
	startErr          error
	completeErr       error
	completedSession  AuthorizationCodeSession
	completedCode     string
	completions       atomic.Int32
	completionEntered chan<- struct{}
	completionRelease <-chan struct{}
}

func (p *authorizationCodeProviderStub) Key() string { return p.key }

func (p *authorizationCodeProviderStub) StartAuthorizationCodeAuth(_ context.Context, redirectURI, state string) (AuthorizationCodeSession, error) {
	p.startedState, p.startedRedirect = state, redirectURI
	session := p.startSession
	if session.AuthorizeURL == "" {
		session.AuthorizeURL = "https://provider.example/oauth?state=" + state
	}
	return session, p.startErr
}

func (p *authorizationCodeProviderStub) CompleteAuthorizationCodeAuth(_ context.Context, code string, session AuthorizationCodeSession) (TokenSet, ProviderAccount, error) {
	p.completions.Add(1)
	p.completedCode, p.completedSession = code, session
	if p.completionEntered != nil {
		p.completionEntered <- struct{}{}
		<-p.completionRelease
	}
	return TokenSet{AccessToken: testAccessToken, RefreshToken: testRefreshToken},
		ProviderAccount{ID: "anilist-account", Username: testPluginUsername}, p.completeErr
}

func newAuthorizationCodeService(t *testing.T) (*Service, *serviceFakeRepo, *authorizationCodeProviderStub) {
	t.Helper()
	repo := newServiceFakeRepo()
	provider := &authorizationCodeProviderStub{key: testPluginProviderKey}
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, reg)
	service.now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	return service, repo, provider
}

func startAuthorizationCodeSession(t *testing.T, service *Service) AuthorizationCodeSession {
	t.Helper()
	session, err := service.StartAuthorizationCodeAuth(t.Context(), 7, "profile-1", testPluginProviderKey, testAuthorizationRedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestServiceAuthorizationCodePersistsHostScopeAndCompletesOnce(t *testing.T) {
	service, repo, provider := newAuthorizationCodeService(t)
	completedAt := service.now()
	provider.startSession = AuthorizationCodeSession{
		ID: "untrusted-id", State: "untrusted-state", Provider: "untrusted-provider",
		UserID: 99, ProfileID: "untrusted-profile", RedirectURI: "https://untrusted.example/callback",
		ProviderState: "provider-secret-state", ExpiresAt: service.now().Add(time.Hour), CompletedAt: &completedAt,
	}
	session := startAuthorizationCodeSession(t, service)
	if _, err := uuid.Parse(session.ID); err != nil || session.ID != provider.startedState || session.State != session.ID {
		t.Fatalf("host state = %q, provider state = %q, err = %v", session.ID, provider.startedState, err)
	}
	if session.Provider != testPluginProviderKey || session.UserID != 7 || session.ProfileID != "profile-1" ||
		session.RedirectURI != testAuthorizationRedirectURI || provider.startedRedirect != testAuthorizationRedirectURI ||
		!session.ExpiresAt.Equal(service.now().Add(authorizationCodeSessionTTL)) || session.CompletedAt != nil {
		t.Fatalf("host session scope = %#v", session)
	}
	stored := repo.sessions[session.ID]
	if !isAuthorizationCodeSession(stored) || stored.Provider != testPluginProviderKey || stored.UserID != 7 || stored.ProfileID != "profile-1" {
		t.Fatalf("persisted session = %#v", stored)
	}
	if stored.VerificationURL != "" || stored.UserCode != AuthMethodAuthorizationCode {
		t.Fatalf("callback data placed in public device-auth fields: %#v", stored)
	}
	decoded, err := authorizationCodeSessionFromDeviceAuthSession(stored)
	if err != nil || decoded.RedirectURI != testAuthorizationRedirectURI || decoded.ProviderState != "provider-secret-state" {
		t.Fatalf("decoded session = %#v, err = %v", decoded, err)
	}
	public, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(public, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["id"] != session.ID || fields["provider"] != testPluginProviderKey ||
		fields["authorization_url"] != session.AuthorizeURL || fields["expires_at"] == nil ||
		strings.Contains(string(public), "provider-secret-state") || strings.Contains(string(public), testAuthorizationRedirectURI) {
		t.Fatalf("public authorization session = %s", public)
	}
	conn, err := service.CompleteAuthorizationCodeAuth(t.Context(), testPluginProviderKey, session.ID, "provider-code")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Provider != testPluginProviderKey || conn.UserID != 7 || conn.ProfileID != "profile-1" ||
		conn.AccessToken != testAccessToken || conn.ProviderAccountID != "anilist-account" || provider.completions.Load() != 1 {
		t.Fatalf("connection = %#v, completions = %d", conn, provider.completions.Load())
	}
	if provider.completedCode != "provider-code" || provider.completedSession.ProviderState != "provider-secret-state" ||
		provider.completedSession.RedirectURI != testAuthorizationRedirectURI || repo.sessions[session.ID].CompletedAt == nil {
		t.Fatalf("callback did not use persisted state: %#v", provider.completedSession)
	}
	if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), testPluginProviderKey, session.ID, "provider-code"); !errors.Is(err, ErrAuthSessionCompleted) {
		t.Fatalf("replay error = %v", err)
	}
	if provider.completions.Load() != 1 {
		t.Fatal("replay reached provider")
	}
}

func TestServiceAuthorizationCodeBindsProviderAndFlow(t *testing.T) {
	service, repo, provider := newAuthorizationCodeService(t)
	other := &authorizationCodeProviderStub{key: "plugin:5:anilist"}
	if err := service.registry.Register(other); err != nil {
		t.Fatal(err)
	}
	session := startAuthorizationCodeSession(t, service)
	if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), other.Key(), session.ID, "code"); !errors.Is(err, ErrAuthSessionMismatch) {
		t.Fatalf("wrong provider error = %v", err)
	}
	if repo.sessions[session.ID].CompletedAt != nil || other.completions.Load() != 0 {
		t.Fatal("wrong provider consumed session")
	}
	// This stub offers both interfaces without declaring a single auth method,
	// so this exercises the persisted flow marker rather than provider selection.
	if _, err := service.PollDeviceAuth(t.Context(), 7, "profile-1", provider.Key(), session.ID); !errors.Is(err, ErrAuthSessionMismatch) {
		t.Fatalf("device poll of authorization-code session error = %v", err)
	}
	if provider.polled || repo.sessions[session.ID].CompletedAt != nil {
		t.Fatal("device polling consumed authorization-code state")
	}
	deviceID := uuid.NewString()
	repo.sessions[deviceID] = DeviceAuthSession{
		ID: deviceID, Provider: provider.Key(), UserID: 7, ProfileID: "profile-1",
		UserCode: "DEVICE", DeviceCode: "device-state", IntervalSeconds: 5, ExpiresAt: service.now().Add(time.Minute),
	}
	if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), deviceID, "code"); !errors.Is(err, ErrAuthSessionMismatch) {
		t.Fatalf("authorization-code callback for device session error = %v", err)
	}
	if provider.completions.Load() != 0 || repo.sessions[deviceID].CompletedAt != nil {
		t.Fatal("authorization-code callback consumed device state")
	}
	if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "code"); err != nil {
		t.Fatalf("correct callback after mismatches: %v", err)
	}
}

func TestServiceAuthorizationCodeRejectsExpiredAndUnknownState(t *testing.T) {
	for _, expiry := range []time.Duration{-time.Second, 0} {
		t.Run(expiry.String(), func(t *testing.T) {
			service, repo, provider := newAuthorizationCodeService(t)
			session := startAuthorizationCodeSession(t, service)
			row := repo.sessions[session.ID]
			row.ExpiresAt = service.now().Add(expiry)
			repo.sessions[session.ID] = row
			if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "code"); !errors.Is(err, ErrAuthSessionExpired) {
				t.Fatalf("expired session error = %v", err)
			}
			if provider.completions.Load() != 0 {
				t.Fatal("expired session reached provider")
			}
		})
	}
	service, _, provider := newAuthorizationCodeService(t)
	for _, state := range []string{"", "secret-code-invalid-state", uuid.NewString(), " " + uuid.NewString()} {
		if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), state, "code"); !errors.Is(err, ErrAuthSessionNotFound) {
			t.Fatalf("unknown state error = %v", err)
		}
	}
	if provider.completions.Load() != 0 {
		t.Fatal("unknown session reached provider")
	}
}

func TestServiceAuthorizationCodeConsumesFailureAndDeniedCallback(t *testing.T) {
	for _, failure := range []string{"exchange", "denied", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			service, repo, provider := newAuthorizationCodeService(t)
			session := startAuthorizationCodeSession(t, service)
			code := "code"
			wantCalls := int32(0)
			switch failure {
			case "exchange":
				provider.completeErr = ErrInvalidCredential
				wantCalls = 1
			case "denied":
				code = ""
			case "corrupt":
				row := repo.sessions[session.ID]
				row.DeviceCode = "invalid-secret-state"
				repo.sessions[session.ID] = row
			}
			if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, code); err == nil || strings.Contains(err.Error(), "invalid-secret-state") {
				t.Fatalf("callback error = %v", err)
			}
			if _, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "retry-code"); !errors.Is(err, ErrAuthSessionCompleted) {
				t.Fatalf("retry error = %v", err)
			}
			if provider.completions.Load() != wantCalls || len(repo.connections) != 0 {
				t.Fatalf("completions = %d, connections = %d", provider.completions.Load(), len(repo.connections))
			}
		})
	}
}

func TestServiceAuthorizationCodeConcurrentCallbacksAcrossServices(t *testing.T) {
	service, repo, provider := newAuthorizationCodeService(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	provider.completionEntered, provider.completionRelease = entered, release
	session := startAuthorizationCodeSession(t, service)
	otherService := NewService(repo, service.registry)
	otherService.now = service.now
	first := make(chan error, 1)
	go func() {
		_, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "code")
		first <- err
	}()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	_, replayErr := otherService.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "code")
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(replayErr, ErrAuthSessionCompleted) || provider.completions.Load() != 1 {
		t.Fatalf("concurrent callback error = %v, completions = %d", replayErr, provider.completions.Load())
	}
}

func TestServiceAuthorizationCodeValidatesScopeMethodAndLifetime(t *testing.T) {
	for _, input := range []struct {
		userID            int
		profile, redirect string
	}{
		{0, "profile-1", testAuthorizationRedirectURI},
		{7, "", testAuthorizationRedirectURI},
		{7, "profile-1", ""},
		{7, "profile-1", "/relative"},
		{7, "profile-1", "https://user:secret@example.com/callback"},
		{7, "profile-1", "https://example.com/callback#fragment"},
	} {
		service, repo, provider := newAuthorizationCodeService(t)
		_, err := service.StartAuthorizationCodeAuth(t.Context(), input.userID, input.profile, provider.Key(), input.redirect)
		var invalid InvalidConnectionInputError
		if !errors.As(err, &invalid) || provider.startedState != "" || len(repo.sessions) != 0 {
			t.Fatalf("invalid input result = %v, started = %q", err, provider.startedState)
		}
	}
	service, repo, provider := newAuthorizationCodeService(t)
	provider.startSession.ExpiresAt = service.now().Add(time.Minute)
	session := startAuthorizationCodeSession(t, service)
	if !session.ExpiresAt.Equal(provider.startSession.ExpiresAt) {
		t.Fatal("provider's shorter expiry was extended")
	}
	provider.startSession.ExpiresAt = service.now()
	if _, err := service.StartAuthorizationCodeAuth(t.Context(), 7, "profile-1", provider.Key(), testAuthorizationRedirectURI); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("already expired challenge error = %v", err)
	}
	if len(repo.sessions) != 1 {
		t.Fatal("persisted already expired challenge")
	}
	plugin := testPluginProvider(t, authorizationCodePluginClient())
	if err := service.registry.Register(&pluginWithAlternateKey{PluginProvider: plugin}); err != nil {
		t.Fatal(err)
	}
	_, err := service.StartAuthorizationCodeAuth(t.Context(), 7, "profile-1", "plugin:6:api-key", testAuthorizationRedirectURI)
	var capability ProviderCapabilityError
	if !errors.As(err, &capability) {
		t.Fatalf("wrong authentication method error = %v", err)
	}
}

type pluginWithAlternateKey struct{ *PluginProvider }

func (*pluginWithAlternateKey) Key() string { return "plugin:6:api-key" }

func TestServiceAuthorizationCodeInvokesPluginBridge(t *testing.T) {
	repo := newServiceFakeRepo()
	client := authorizationCodePluginClient()
	provider := authorizationCodePluginProvider(t, client)
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, reg)
	session := startAuthorizationCodeSession(t, service)
	if client.initAuthorizeRequest.GetState() != session.ID || client.initAuthorizeRequest.GetRedirectUri() != testAuthorizationRedirectURI {
		t.Fatalf("plugin authorization request = %#v", client.initAuthorizeRequest)
	}
	if _, err := service.PollDeviceAuth(t.Context(), 7, "profile-1", provider.Key(), session.ID); err == nil {
		t.Fatal("authorization-code provider accepted device polling")
	}
	if client.devicePollRequest != nil {
		t.Fatal("device poll reached authorization-code plugin")
	}
	conn, err := service.CompleteAuthorizationCodeAuth(t.Context(), provider.Key(), session.ID, "provider-code")
	if err != nil {
		t.Fatal(err)
	}
	if client.exchangeCodeRequest.GetAuthorizationCode() != "provider-code" ||
		client.exchangeCodeRequest.GetRedirectUri() != testAuthorizationRedirectURI || len(client.exchangeCodeRequest.GetProviderState()) != 0 ||
		conn.ProviderAccountID != "anilist-user" || conn.AccessToken != testAccessToken {
		t.Fatalf("plugin code request = %#v, connection = %#v", client.exchangeCodeRequest, conn)
	}
}
