package watchsync

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const testAuthorizationRedirectURI = "https://silo.example/api/v2/watch-providers/plugin:4:anilist/auth/callback"

func authorizationCodePluginProvider(t *testing.T, client WatchSyncPluginClient) *PluginProvider {
	t.Helper()
	return testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_AUTHORIZATION_CODE},
		ExportWatched: true,
	})
}

func authorizationCodePluginClient() *fakeWatchSyncPluginClient {
	return &fakeWatchSyncPluginClient{
		initAuthorizeResponse: &pluginv1.WatchSyncInitAuthorizeResponse{AuthorizationUrl: "https://anilist.co/api/v2/oauth/authorize"},
		exchangeCodeResponse: &pluginv1.WatchSyncCredentialResponse{
			Credentials: &pluginv1.WatchSyncCredentials{
				AccessToken: testAccessToken, TokenType: testBearerTokenType,
				SecretAttributes: map[string]string{"proof": "credential-proof"},
			},
			Account: &pluginv1.WatchSyncAccount{ExternalSubject: "anilist-user", Username: testPluginUsername},
		},
	}
}

func TestPluginProviderAuthorizationCodeRoundTripsOpaqueState(t *testing.T) {
	for _, state := range [][]byte{nil, {0, 255, 1, 2, 3}} {
		t.Run(base64.RawURLEncoding.EncodeToString(state), func(t *testing.T) {
			client := authorizationCodePluginClient()
			client.initAuthorizeResponse.ProviderState = state
			provider := authorizationCodePluginProvider(t, client)
			provider.resolveConfig = func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
				return &pluginv1.WatchSyncProviderConfig{
					Values:       map[string]string{"anilist.client_id": "app-id"},
					SecretValues: map[string]string{"anilist.client_secret": "app-secret"},
				}, nil
			}
			if provider.AuthMethod() != AuthMethodAuthorizationCode {
				t.Fatalf("auth method = %q", provider.AuthMethod())
			}
			session, err := provider.StartAuthorizationCodeAuth(t.Context(), testAuthorizationRedirectURI, "host-state")
			if err != nil {
				t.Fatal(err)
			}
			request := client.initAuthorizeRequest
			if request.GetCapabilityId() != testPluginCapabilityID || request.GetState() != "host-state" ||
				request.GetRedirectUri() != testAuthorizationRedirectURI || request.GetProviderConfig().GetValues()["anilist.client_id"] != "app-id" {
				t.Fatalf("init authorization request = %#v", request)
			}
			if session.ProviderState != base64.RawURLEncoding.EncodeToString(state) || session.RedirectURI != testAuthorizationRedirectURI {
				t.Fatalf("session = %#v", session)
			}
			tokens, account, err := provider.CompleteAuthorizationCodeAuth(t.Context(), "provider-code", session)
			if err != nil {
				t.Fatal(err)
			}
			exchange := client.exchangeCodeRequest
			if exchange.GetCapabilityId() != testPluginCapabilityID || exchange.GetRedirectUri() != testAuthorizationRedirectURI ||
				exchange.GetAuthorizationCode() != "provider-code" || string(exchange.GetProviderState()) != string(state) ||
				exchange.GetProviderConfig().GetSecretValues()["anilist.client_secret"] != "app-secret" {
				t.Fatalf("code exchange request = %#v", exchange)
			}
			if tokens.AccessToken != testAccessToken || tokens.SecretAttributes["proof"] != "credential-proof" || account.ID != "anilist-user" {
				t.Fatalf("tokens = %#v, account = %#v", tokens, account)
			}
		})
	}
}

func TestPluginProviderAuthorizationCodeRejectsAmbiguousAndUnsupportedAuth(t *testing.T) {
	for _, methods := range [][]pluginv1.WatchSyncAuthMethod{
		nil,
		{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_UNSPECIFIED},
		{pluginv1.WatchSyncAuthMethod(999)},
		{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY, pluginv1.WatchSyncAuthMethod(999)},
		{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_AUTHORIZATION_CODE, pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
		{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_AUTHORIZATION_CODE, pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
	} {
		if _, err := supportedWatchSyncAuthMethod(&pluginv1.WatchSyncProviderDescriptor{AuthMethods: methods}); err == nil {
			t.Fatalf("accepted authentication methods %v", methods)
		}
	}
	client := authorizationCodePluginClient()
	provider := testPluginProvider(t, client)
	if _, err := provider.StartAuthorizationCodeAuth(t.Context(), testAuthorizationRedirectURI, "state"); err == nil {
		t.Fatal("API-key provider started authorization-code auth")
	}
	if _, _, err := provider.CompleteAuthorizationCodeAuth(t.Context(), "code", AuthorizationCodeSession{}); err == nil {
		t.Fatal("API-key provider exchanged an authorization code")
	}
	if client.initAuthorizeRequest != nil || client.exchangeCodeRequest != nil {
		t.Fatal("unsupported authentication reached the plugin")
	}
}

func TestPluginProviderAuthorizationCodeSanitizesFaults(t *testing.T) {
	const code = "authorization-code-secret"
	const state = "opaque-flow-secret"
	const appSecret = "admin-client-secret"
	const nestedSecret = "nested-admin-secret"
	for _, operation := range []string{"start", "complete", "rpc"} {
		t.Run(operation, func(t *testing.T) {
			client := authorizationCodePluginClient()
			client.initAuthorizeResponse.ProviderState = []byte(state)
			provider := authorizationCodePluginProvider(t, client)
			provider.resolveConfig = func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
				return &pluginv1.WatchSyncProviderConfig{SecretValues: map[string]string{
					"app.client_secret": appSecret, "app.nested": `{"password":"` + nestedSecret + `"}`,
				}}, nil
			}
			message := strings.Join([]string{code, state, base64.RawURLEncoding.EncodeToString([]byte(state)), appSecret, nestedSecret, testAccessToken, "credential-proof"}, " ")
			var err error
			if operation == "start" {
				client.initAuthorizeResponse.Fault = &pluginv1.WatchSyncFault{
					Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
					SafeMessage: state + " " + appSecret + " " + nestedSecret,
				}
				_, err = provider.StartAuthorizationCodeAuth(t.Context(), testAuthorizationRedirectURI, state)
				var invalid InvalidConnectionInputError
				if !errors.As(err, &invalid) {
					t.Fatalf("error = %#v, want invalid input", err)
				}
			} else {
				if operation == "rpc" {
					client.exchangeCodeErr = errors.New(message)
				} else {
					client.exchangeCodeResponse.Fault = &pluginv1.WatchSyncFault{
						Code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL, SafeMessage: message,
					}
				}
				_, _, err = provider.CompleteAuthorizationCodeAuth(t.Context(), code, AuthorizationCodeSession{
					RedirectURI: testAuthorizationRedirectURI, State: state,
					ProviderState: base64.RawURLEncoding.EncodeToString([]byte(state)),
				})
				if operation == "complete" && !IsInvalidCredentialError(err) {
					t.Fatalf("error = %#v, want invalid credential", err)
				}
				if operation == "rpc" && !IsRetryableProviderError(err) {
					t.Fatalf("error = %#v, want retryable RPC failure", err)
				}
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, secret := range []string{code, state, base64.RawURLEncoding.EncodeToString([]byte(state)), appSecret, nestedSecret, testAccessToken, "credential-proof"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed secret %q: %v", secret, err)
				}
			}
		})
	}
}

func TestPluginProviderAuthorizationCodeRedactsNestedPKCEVerifier(t *testing.T) {
	const verifier = "secret-pkce-verifier"
	state := []byte(`{"flow":{"code_verifier":"` + verifier + `"}}`)
	client := authorizationCodePluginClient()
	provider := authorizationCodePluginProvider(t, client)
	client.initAuthorizeResponse.ProviderState = state
	client.initAuthorizeResponse.Fault = &pluginv1.WatchSyncFault{
		Code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST, SafeMessage: "rejected " + verifier,
	}
	_, startErr := provider.StartAuthorizationCodeAuth(t.Context(), testAuthorizationRedirectURI, "host-state")
	client.exchangeCodeResponse.Fault = &pluginv1.WatchSyncFault{
		Code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL, SafeMessage: "rejected " + verifier,
	}
	_, _, exchangeErr := provider.CompleteAuthorizationCodeAuth(t.Context(), "code", AuthorizationCodeSession{
		RedirectURI: testAuthorizationRedirectURI, ProviderState: base64.RawURLEncoding.EncodeToString(state),
	})
	for _, err := range []error{startErr, exchangeErr} {
		if err == nil || strings.Contains(err.Error(), verifier) || !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("PKCE verifier error = %v", err)
		}
	}
}

func TestPluginProviderAuthorizationCodeRejectsInvalidResponses(t *testing.T) {
	for _, invalidURL := range []string{"", "/relative", "javascript:alert(1)", "https://user:password@example.com/oauth"} {
		client := authorizationCodePluginClient()
		client.initAuthorizeResponse.AuthorizationUrl = invalidURL
		if _, err := authorizationCodePluginProvider(t, client).StartAuthorizationCodeAuth(t.Context(), testAuthorizationRedirectURI, "state"); err == nil {
			t.Fatalf("accepted authorization URL %q", invalidURL)
		}
	}
	for _, incomplete := range []*pluginv1.WatchSyncCredentialResponse{
		nil,
		{Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testAccessToken}},
		{Account: &pluginv1.WatchSyncAccount{ExternalSubject: "account"}},
	} {
		client := authorizationCodePluginClient()
		client.exchangeCodeResponse = incomplete
		if _, _, err := authorizationCodePluginProvider(t, client).CompleteAuthorizationCodeAuth(t.Context(), "code", AuthorizationCodeSession{RedirectURI: testAuthorizationRedirectURI}); err == nil {
			t.Fatalf("accepted incomplete credentials %#v", incomplete)
		}
	}
}
