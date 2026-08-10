package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	oauthRetryLoginPath = "/login"
	oauthRetrySafeNext  = "/library?tab=new"
)

func TestOAuthCallback_FailureUsesOnlySanitizedStoredNext(t *testing.T) {
	tests := []struct {
		name string
		next string
		want string
	}{
		{name: "safe relative path", next: oauthRetrySafeNext, want: oauthRetrySafeNext},
		{name: "absolute URL", next: "https://evil.example/steal", want: "/"},
		{name: "scheme-relative URL", next: "//evil.example/steal", want: "/"},
		{name: "backslash authority", next: `/\evil.example/steal`, want: "/"},
		{name: "raw control", next: "/profiles\nattack", want: "/"},
		{name: "encoded backslash authority", next: `/%5Cevil.example/steal`, want: "/"},
		{name: "encoded slash authority", next: `/%2Fevil.example/steal`, want: "/"},
		{name: "encoded control", next: `/profiles%0Aattack`, want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			providerState, err := structpb.NewStruct(map[string]any{"pkce_verifier": "private-verifier"})
			if err != nil {
				t.Fatalf("new provider state: %v", err)
			}
			client := &fakeOAuthClient{
				initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: providerState},
				exchangeErr: errors.New("exchange failed with private token"),
			}
			handler, _ := newOAuthHandlerForTest(t, client, &fakeCompleter{})
			initRequest := withInstallID(httptest.NewRequest(http.MethodPost, "/init?next="+url.QueryEscape(tt.next), nil), "42")
			initResponse := httptest.NewRecorder()
			handler.HandleInit(initResponse, initRequest)
			state := client.gotInit.GetState()

			// When
			callback := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=private-code&state="+url.QueryEscape(state)+"&next=%2Fcaller", nil), "42")
			addOAuthResponseCookies(t, initResponse, callback)
			response := httptest.NewRecorder()
			handler.HandleCallback(response, callback)

			// Then
			location := response.Header().Get("Location")
			parsed, err := url.Parse(location)
			if err != nil {
				t.Fatalf("parse failure redirect: %v", err)
			}
			if response.Code != http.StatusFound || parsed.Path != oauthRetryLoginPath {
				t.Fatalf("callback response = %d %q, want login redirect", response.Code, location)
			}
			if got := parsed.Query().Get("redirect"); got != tt.want {
				t.Errorf("redirect = %q, want %q in %q", got, tt.want, location)
			}
			if got := parsed.Query().Get("error"); got != "oauth_failed" {
				t.Errorf("error = %q, want oauth_failed", got)
			}
			for _, secret := range []string{"private-code", "private-verifier", "private token", state, "/caller"} {
				if strings.Contains(location, secret) {
					t.Errorf("failure redirect leaked %q: %q", secret, location)
				}
			}
		})
	}
}
