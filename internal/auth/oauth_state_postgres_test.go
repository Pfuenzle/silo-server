package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestOAuthStateEncryptedCrossInstance(t *testing.T) {
	// Given
	ctx, pool := newOAuthStatePostgresFixture(t)
	providerState, err := structpb.NewStruct(map[string]any{
		"pkce_verifier": "pkce-verifier-must-not-reach-postgres",
		"nonce":         "oidc-nonce-must-not-reach-postgres",
	})
	if err != nil {
		t.Fatalf("new provider state: %v", err)
	}
	client := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: providerState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "external-subject"},
	}
	completer := &fakeCompleter{pair: &TokenPair{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 60}, user: &models.User{ID: 1}}
	stateSecret := []byte("test-state-secret")
	stateCipher := newOAuthStateCipher(t)
	initStore := NewPGOAuthStore(pool, stateSecret, stateCipher)
	callbackStore := NewPGOAuthStore(pool, stateSecret, stateCipher)
	initHandler := newPostgresOAuthHandler(postgresOAuthHandlerDeps{initStore, client, completer, stateSecret})
	callbackHandler := newPostgresOAuthHandler(postgresOAuthHandlerDeps{callbackStore, client, completer, stateSecret})

	// When
	initRequest := withInstallID(httptest.NewRequest(http.MethodPost, "/init?next=/oauth-state-test", nil), "42")
	initResponse := httptest.NewRecorder()
	initHandler.HandleInit(initResponse, initRequest)
	if initResponse.Code != http.StatusFound {
		t.Fatalf("init status = %d, body = %s", initResponse.Code, initResponse.Body.String())
	}
	state := client.gotInit.GetState()

	var rawProviderState, rawCiphertext string
	if err := pool.QueryRow(ctx, `SELECT provider_state::text, provider_state_ciphertext FROM oauth_session WHERE state = $1`, state).Scan(&rawProviderState, &rawCiphertext); err != nil {
		t.Fatalf("read raw provider state: %v", err)
	}
	callbackRequest := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=code&state="+url.QueryEscape(state), nil), "42")
	callbackResponse := httptest.NewRecorder()
	callbackHandler.HandleCallback(callbackResponse, callbackRequest)

	// Then
	if strings.Contains(rawProviderState, "pkce-verifier-must-not-reach-postgres") || strings.Contains(rawProviderState, "oidc-nonce-must-not-reach-postgres") || strings.Contains(rawCiphertext, "pkce-verifier-must-not-reach-postgres") || strings.Contains(rawCiphertext, "oidc-nonce-must-not-reach-postgres") {
		t.Fatal("raw OAuth state storage exposes provider secrets")
	}
	if !strings.HasPrefix(rawCiphertext, "enc:v1:") {
		t.Fatalf("provider state ciphertext = %q, want encrypted envelope", rawCiphertext)
	}
	if client.gotExchange == nil {
		t.Fatal("callback handler did not exchange state written by independent init handler")
	}
}

func TestOAuthStateTamperRejected(t *testing.T) {
	// Given
	ctx, pool := newOAuthStatePostgresFixture(t)
	client, completer, state, handler := newPersistedOAuthCallbackFixture(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE oauth_session SET provider_state_ciphertext = 'enc:v1:tampered' WHERE state = $1`, state); err != nil {
		t.Fatalf("tamper provider state: %v", err)
	}
	before := oauthCompletionCount(t, ctx, pool, "/oauth-state-test")

	// When
	request := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=code&state="+url.QueryEscape(state), nil), "42")
	response := httptest.NewRecorder()
	handler.HandleCallback(response, request)

	// Then
	if location := response.Header().Get("Location"); !strings.Contains(location, "reason=session_expired") {
		t.Fatalf("redirect = %q, want session_expired", location)
	}
	if client.gotExchange != nil || completer.gotInput.Response != nil {
		t.Fatal("tampered provider state reached exchange or login completion")
	}
	if after := oauthCompletionCount(t, ctx, pool, "/oauth-state-test"); after != before {
		t.Fatalf("tamper failure wrote oauth completion: before=%d after=%d", before, after)
	}
}

func TestOAuthLegacyStateConsumedOnce(t *testing.T) {
	// Given
	ctx, pool := newOAuthStatePostgresFixture(t)
	client := &fakeOAuthClient{exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "external-subject"}}
	completer := &fakeCompleter{pair: &TokenPair{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 60}, user: &models.User{ID: 1}}
	stateSecret := []byte("test-state-secret")
	state := SignState(stateSecret, StatePayload{Nonce: "nonce", InstallID: "42", ExpiresAt: time.Now().Add(time.Minute)})
	if _, err := pool.Exec(ctx, `
		INSERT INTO oauth_session (state, install_id, redirect_uri, provider_state, next_url, expires_at)
		VALUES ($1, '42', 'https://silo.test/callback', '{"pkce_verifier":"legacy"}', '/oauth-state-test', now() + interval '1 minute')`, state); err != nil {
		t.Fatalf("insert legacy state: %v", err)
	}
	handler := newPostgresOAuthHandler(postgresOAuthHandlerDeps{NewPGOAuthStore(pool, stateSecret, newOAuthStateCipher(t)), client, completer, stateSecret})
	request := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=code&state="+url.QueryEscape(state), nil), "42")

	// When
	firstResponse := httptest.NewRecorder()
	handler.HandleCallback(firstResponse, request)
	secondResponse := httptest.NewRecorder()
	handler.HandleCallback(secondResponse, request)

	// Then
	if client.gotExchange == nil || !strings.Contains(firstResponse.Header().Get("Location"), "/login/oauth-complete?") {
		t.Fatal("unexpired legacy state did not complete once")
	}
	if location := secondResponse.Header().Get("Location"); !strings.Contains(location, "reason=session_expired") {
		t.Fatalf("replay redirect = %q, want session_expired", location)
	}
}

func newOAuthStatePostgresFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect OAuth state test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

type postgresOAuthHandlerDeps struct {
	store       OAuthStore
	client      *fakeOAuthClient
	completer   *fakeCompleter
	stateSecret []byte
}

func newPostgresOAuthHandler(deps postgresOAuthHandlerDeps) *OAuthHandler {
	return NewOAuthHandler(OAuthHandlerDeps{
		Store:       deps.store,
		StateSecret: deps.stateSecret,
		ResolveClient: func(context.Context, int) (OAuthClient, string, error) {
			return deps.client, "oidc", nil
		},
		LoginCompleter: deps.completer,
		HostBaseURL:    "https://silo.test",
		StateTTL:       time.Minute,
	})
}

func newPersistedOAuthCallbackFixture(t *testing.T, pool *pgxpool.Pool) (*fakeOAuthClient, *fakeCompleter, string, *OAuthHandler) {
	t.Helper()
	providerState, err := structpb.NewStruct(map[string]any{"pkce_verifier": "verifier"})
	if err != nil {
		t.Fatalf("new provider state: %v", err)
	}
	client := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: providerState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "external-subject"},
	}
	completer := &fakeCompleter{pair: &TokenPair{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 60}, user: &models.User{ID: 1}}
	stateSecret := []byte("test-state-secret")
	store := NewPGOAuthStore(pool, stateSecret, newOAuthStateCipher(t))
	handler := newPostgresOAuthHandler(postgresOAuthHandlerDeps{store, client, completer, stateSecret})
	initRequest := withInstallID(httptest.NewRequest(http.MethodPost, "/init?next=/oauth-state-test", nil), "42")
	initResponse := httptest.NewRecorder()
	handler.HandleInit(initResponse, initRequest)
	if initResponse.Code != http.StatusFound {
		t.Fatalf("init status = %d, body = %s", initResponse.Code, initResponse.Body.String())
	}
	return client, completer, client.gotInit.GetState(), handler
}

func oauthCompletionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nextURL string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oauth_completion WHERE next_url = $1`, nextURL).Scan(&count); err != nil {
		t.Fatalf("count OAuth completions: %v", err)
	}
	return count
}

func newOAuthStateCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new OAuth state cipher: %v", err)
	}
	return cipher
}
