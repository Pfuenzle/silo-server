package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestOAuthLinkingRejected(t *testing.T) {
	// Given
	providerState, err := structpb.NewStruct(map[string]any{"pkce_verifier": "secret-verifier"})
	if err != nil {
		t.Fatalf("new provider state: %v", err)
	}
	client := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: providerState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "external-subject"},
	}
	completer := &fakeCompleter{}
	handler, store := newOAuthHandlerForTest(t, client, completer)
	state := SignState([]byte("test-secret"), StatePayload{
		Nonce:     "nonce",
		InstallID: "42",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err := store.Insert(t.Context(), OAuthSession{
		State:         state,
		InstallID:     "42",
		RedirectURI:   "https://silo.test/api/v1/auth/oauth/42/callback",
		LinkingUserID: "7",
		ProviderState: []byte(`{"pkce_verifier":"secret-verifier"}`),
		NextURL:       "/me",
		ExpiresAt:     time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("insert linking session: %v", err)
	}

	// When
	request := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=code&state="+url.QueryEscape(state), nil), "42")
	request.AddCookie(&http.Cookie{Name: oauthBrowserCookiePrefix + "nonce", Value: "nonce"})
	response := httptest.NewRecorder()
	handler.HandleCallback(response, request)

	// Then
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFound)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Path != "/login" || location.Query().Get("error") != "oauth_failed" || location.Query().Get("redirect") != "/me" {
		t.Fatalf("redirect = %q, want generic OAuth failure with trusted next", location)
	}
	if location.Query().Has("reason") || len(location.Query()) != 2 {
		t.Fatalf("redirect leaked linking detail: %q", location)
	}
	if client.gotExchange != nil {
		t.Fatal("ExchangeCode was called for unsupported linking")
	}
	if completer.gotInput.Response != nil {
		t.Fatal("CompleteOAuthLogin was called for unsupported linking")
	}
	if _, exists := store.rows[state]; !exists {
		t.Fatal("linking rejection consumed oauth state")
	}
}

func TestOAuthLinkingRejectedByService(t *testing.T) {
	// Given
	service := &Service{}

	// When
	_, _, err := service.CompleteOAuthLogin(t.Context(), OAuthLoginInput{LinkingUserID: 7})

	// Then
	if !errors.Is(err, ErrOAuthLinkingUnsupported) {
		t.Fatalf("error = %v, want ErrOAuthLinkingUnsupported", err)
	}
}
