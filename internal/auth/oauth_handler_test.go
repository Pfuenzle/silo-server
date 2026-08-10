package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeOAuthClient struct {
	initResp     *pluginv1.InitAuthorizeResponse
	initErr      error
	exchangeResp *pluginv1.AuthenticateResponse
	exchangeErr  error

	gotInit     *pluginv1.InitAuthorizeRequest
	gotExchange *pluginv1.ExchangeCodeRequest
}

func (f *fakeOAuthClient) InitAuthorize(_ context.Context, in *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	f.gotInit = in
	return f.initResp, f.initErr
}

func (f *fakeOAuthClient) ExchangeCode(_ context.Context, in *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	f.gotExchange = in
	return f.exchangeResp, f.exchangeErr
}

type fakeCompleter struct {
	gotInput OAuthLoginInput
	pair     *TokenPair
	user     *models.User
	err      error
}

func (f *fakeCompleter) CompleteOAuthLogin(_ context.Context, in OAuthLoginInput) (*TokenPair, *models.User, error) {
	f.gotInput = in
	return f.pair, f.user, f.err
}

func newOAuthHandlerForTest(_ *testing.T, fc *fakeOAuthClient, fcomp *fakeCompleter) (*OAuthHandler, *InMemoryOAuthStore) {
	store := NewInMemoryOAuthStore()
	h := NewOAuthHandler(OAuthHandlerDeps{
		Store:       store,
		StateSecret: []byte("test-secret"),
		ResolveClient: func(_ context.Context, _ int) (OAuthClient, string, error) {
			return fc, "whmcs", nil
		},
		LoginCompleter:       fcomp,
		HostBaseURL:          "https://silo.test",
		StateTTL:             10 * time.Minute,
		FrontendCompletePath: "/login/oauth-complete",
	})
	return h, store
}

func withInstallID(r *http.Request, id string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("install_id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

func addOAuthResponseCookies(t *testing.T, response *httptest.ResponseRecorder, request *http.Request) {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("OAuth init response did not set a browser-binding cookie")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
}

func TestOAuthInit_Redirects302WithAuthorizeURL(t *testing.T) {
	authorizeURL := "https://billing.example/oauth/authorize.php?client_id=x"
	pState, _ := structpb.NewStruct(map[string]any{"pkce_verifier": "abc"})
	fc := &fakeOAuthClient{initResp: &pluginv1.InitAuthorizeResponse{AuthorizeUrl: authorizeURL, ProviderState: pState}}
	fcomp := &fakeCompleter{}
	h, store := newOAuthHandlerForTest(t, fc, fcomp)

	r := withInstallID(httptest.NewRequest("POST", "/api/v1/auth/oauth/42/init?next=/me", nil), "42")
	w := httptest.NewRecorder()
	h.HandleInit(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != authorizeURL {
		t.Errorf("Location = %q", got)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("browser-binding cookies = %d, want 1", len(cookies))
	}
	binding := cookies[0]
	if !binding.HttpOnly || !binding.Secure || binding.SameSite != http.SameSiteLaxMode {
		t.Fatalf("browser-binding cookie flags = HttpOnly:%t Secure:%t SameSite:%d", binding.HttpOnly, binding.Secure, binding.SameSite)
	}
	if binding.Path != "/api/v1/auth/oauth/42/callback" {
		t.Fatalf("browser-binding cookie path = %q", binding.Path)
	}

	// Exactly one row inserted; next_url preserved.
	if got := len(store.rows); got != 1 {
		t.Errorf("rows = %d, want 1", got)
	}
	for _, row := range store.rows {
		if row.InstallID != "42" {
			t.Errorf("InstallID = %q", row.InstallID)
		}
		if row.NextURL != "/me" {
			t.Errorf("NextURL = %q", row.NextURL)
		}
		if !strings.Contains(string(row.ProviderState), "pkce_verifier") {
			t.Errorf("ProviderState missing PKCE: %s", row.ProviderState)
		}
	}

	// Plugin received our state.
	if fc.gotInit == nil || fc.gotInit.GetState() == "" {
		t.Error("plugin not invoked or empty state")
	}
}

func TestOAuthInit_NormalizesUnsafeNextURL(t *testing.T) {
	authorizeURL := "https://billing.example/oauth/authorize.php?client_id=x"
	fc := &fakeOAuthClient{initResp: &pluginv1.InitAuthorizeResponse{AuthorizeUrl: authorizeURL}}
	h, store := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := withInstallID(httptest.NewRequest("POST", "/api/v1/auth/oauth/42/init?next=//evil.example/path", nil), "42")
	w := httptest.NewRecorder()
	h.HandleInit(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	for _, row := range store.rows {
		if row.NextURL != "/" {
			t.Fatalf("NextURL = %q, want /", row.NextURL)
		}
	}
}

func TestNormalizeOAuthNext_PreservesOnlySafeRelativeTargets(t *testing.T) {
	tests := []struct {
		name string
		next string
		want string
	}{
		{name: "relative path", next: "/profiles", want: "/profiles"},
		{name: "relative path with query and fragment", next: "/library?tab=new#recent", want: "/library?tab=new#recent"},
		{name: "absolute URL", next: "https://evil.example/steal", want: "/"},
		{name: "scheme-relative URL", next: "//evil.example/steal", want: "/"},
		{name: "backslash authority", next: `/\evil.example/steal`, want: "/"},
		{name: "encoded backslash authority", next: `/%5Cevil.example/steal`, want: "/"},
		{name: "encoded slash authority", next: `/%2Fevil.example/steal`, want: "/"},
		{name: "encoded control", next: `/profiles%0ASet-Cookie:attack`, want: "/"},
		{name: "raw control", next: "/profiles\nattack", want: "/"},
		{name: "malformed encoding", next: "/profiles%zz", want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOAuthNext(tt.next); got != tt.want {
				t.Fatalf("normalizeOAuthNext(%q) = %q, want %q", tt.next, got, tt.want)
			}
		})
	}
}

func TestOAuthInit_RejectsBadInstallID(t *testing.T) {
	h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
	cases := []string{"", "abc", "-1", "0"}
	for _, c := range cases {
		r := withInstallID(httptest.NewRequest("POST", "/init", nil), c)
		w := httptest.NewRecorder()
		h.HandleInit(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("install_id=%q → code %d, want 400", c, w.Code)
		}
	}
}

func TestOAuthInit_PluginErrorReturns502(t *testing.T) {
	fc := &fakeOAuthClient{initErr: errors.New("upstream db dsn leaked")}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})
	r := withInstallID(httptest.NewRequest("POST", "/init", nil), "42")
	w := httptest.NewRecorder()
	h.HandleInit(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", w.Code)
	}
	if strings.Contains(w.Body.String(), "upstream db dsn leaked") {
		t.Errorf("response leaked internal error: %q", w.Body.String())
	}
}

func TestOAuthInit_EmptyAuthorizeURLIs502(t *testing.T) {
	fc := &fakeOAuthClient{initResp: &pluginv1.InitAuthorizeResponse{AuthorizeUrl: ""}}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})
	r := withInstallID(httptest.NewRequest("POST", "/init", nil), "42")
	w := httptest.NewRecorder()
	h.HandleInit(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", w.Code)
	}
}

func TestOAuthCallback_HappyPath_DeliversOneTimeCompletionCode(t *testing.T) {
	pState, _ := structpb.NewStruct(map[string]any{"pkce_verifier": "abc"})
	fc := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://a", ProviderState: pState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "ws-1", Email: "u@x.com", DisplayName: "U"},
	}
	fcomp := &fakeCompleter{
		pair: &TokenPair{AccessToken: "acc.tok", RefreshToken: "ref.tok", ExpiresIn: 900},
		user: &models.User{ID: 7},
	}
	h, _ := newOAuthHandlerForTest(t, fc, fcomp)

	// Init first to populate the store + capture the signed state.
	rInit := withInstallID(httptest.NewRequest("POST", "/api/v1/auth/oauth/42/init?next=/me", nil), "42")
	wInit := httptest.NewRecorder()
	h.HandleInit(wInit, rInit)
	if wInit.Code != http.StatusFound {
		t.Fatalf("init code = %d body=%s", wInit.Code, wInit.Body.String())
	}
	state := fc.gotInit.GetState()

	// Callback with that state.
	rCb := withInstallID(httptest.NewRequest("GET", "/api/v1/auth/oauth/42/callback?code=auth-code&state="+url.QueryEscape(state), nil), "42")
	addOAuthResponseCookies(t, wInit, rCb)
	wCb := httptest.NewRecorder()
	h.HandleCallback(wCb, rCb)

	if wCb.Code != http.StatusFound {
		t.Fatalf("callback code = %d body = %s", wCb.Code, wCb.Body.String())
	}
	loc := wCb.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://silo.test/login/oauth-complete?") {
		t.Fatalf("redirect = %q", loc)
	}
	if strings.Contains(loc, "acc.tok") || strings.Contains(loc, "ref.tok") {
		t.Fatalf("redirect leaked tokens: %q", loc)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	code := parsed.Query().Get("code")
	if code == "" {
		t.Fatalf("completion code missing from redirect: %q", loc)
	}
	if got := parsed.Query().Get("next"); got != "/me" {
		t.Fatalf("completion redirect next = %q, want /me", got)
	}
	if parsed.Query().Has("state") {
		t.Fatalf("completion redirect leaked OAuth state: %q", loc)
	}
	if got := wCb.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("callback cache-control = %q, want no-store", got)
	}
	if got := wCb.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("callback referrer-policy = %q, want no-referrer", got)
	}
	cleared := false
	for _, cookie := range wCb.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, oauthBrowserCookiePrefix) && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("callback did not clear browser-binding cookie")
	}

	rComplete := httptest.NewRequest("POST", "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"`+code+`"}`))
	wComplete := httptest.NewRecorder()
	h.HandleComplete(wComplete, rComplete)
	if wComplete.Code != http.StatusOK {
		t.Fatalf("complete code = %d body=%s", wComplete.Code, wComplete.Body.String())
	}
	if got := wComplete.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("complete cache-control = %q, want no-store", got)
	}
	var completed OAuthCompleteResponse
	if err := json.NewDecoder(wComplete.Body).Decode(&completed); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	if completed.AccessToken != "acc.tok" {
		t.Errorf("access_token = %q", completed.AccessToken)
	}
	if completed.RefreshToken != "ref.tok" {
		t.Errorf("refresh_token = %q", completed.RefreshToken)
	}
	if completed.NextURL != "/me" {
		t.Errorf("next = %q", completed.NextURL)
	}

	// Completer received the right inputs.
	if fcomp.gotInput.InstallationID != 42 {
		t.Errorf("InstallationID = %d", fcomp.gotInput.InstallationID)
	}
	if fcomp.gotInput.Response.GetExternalSubject() != "ws-1" {
		t.Errorf("ExternalSubject = %q", fcomp.gotInput.Response.GetExternalSubject())
	}
	if fcomp.gotInput.CapabilityID != "whmcs" {
		t.Errorf("CapabilityID = %q", fcomp.gotInput.CapabilityID)
	}
}

func TestOAuthCallback_RejectsStateWithoutInitiatingBrowserCookie(t *testing.T) {
	// Given
	providerState, err := structpb.NewStruct(map[string]any{"pkce_verifier": "abc"})
	if err != nil {
		t.Fatalf("new provider state: %v", err)
	}
	client := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: providerState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "attacker"},
	}
	handler, _ := newOAuthHandlerForTest(t, client, &fakeCompleter{
		pair: &TokenPair{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 900},
		user: &models.User{ID: 7},
	})
	initResponse := httptest.NewRecorder()
	handler.HandleInit(initResponse, withInstallID(httptest.NewRequest(http.MethodPost, "/init?next=/profiles", nil), "42"))
	state := client.gotInit.GetState()

	// When
	callback := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=code&state="+url.QueryEscape(state), nil), "42")
	response := httptest.NewRecorder()
	handler.HandleCallback(response, callback)

	// Then
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFound)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Path != "/login" || location.Query().Get("error") != "oauth_failed" {
		t.Fatalf("redirect = %q, want generic OAuth failure", location)
	}
	if location.Query().Has("redirect") {
		t.Fatalf("unbound browser received trusted redirect: %q", location)
	}
	if client.gotExchange != nil {
		t.Fatal("ExchangeCode called without browser binding")
	}
	cleared := false
	for _, cookie := range response.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, oauthBrowserCookiePrefix) && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("rejected callback did not clear browser-binding cookie")
	}
}

func TestOAuthCallback_RejectsTamperedState(t *testing.T) {
	h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
	r := withInstallID(httptest.NewRequest("GET", "/cb?code=x&state=tampered.junk&next=%2Fadmin", nil), "42")
	w := httptest.NewRecorder()
	h.HandleCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("code = %d", w.Code)
	}
	location := w.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if parsed.Query().Get("error") != "oauth_failed" {
		t.Errorf("Location = %q", location)
	}
	if parsed.Query().Has("next") || parsed.Query().Has("redirect") {
		t.Errorf("untrusted callback next influenced redirect: %q", location)
	}
}

func TestOAuthCallback_RejectsMissingCodeOrState(t *testing.T) {
	h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
	r := withInstallID(httptest.NewRequest("GET", "/cb?next=%2Fadmin", nil), "42")
	w := httptest.NewRecorder()
	h.HandleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("missing state accepted caller next: %q", got)
	}
}

func TestOAuthCallback_InstallMismatchRedirectsToLoginError(t *testing.T) {
	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{initResp: &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://a", ProviderState: pState}}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	// Init for install 42.
	rInit := withInstallID(httptest.NewRequest("POST", "/init", nil), "42")
	wInit := httptest.NewRecorder()
	h.HandleInit(wInit, rInit)
	state := fc.gotInit.GetState()

	// Callback against install 99 with state signed for 42 → mismatch.
	rCb := withInstallID(httptest.NewRequest("GET", "/cb?code=c&state="+url.QueryEscape(state), nil), "99")
	wCb := httptest.NewRecorder()
	h.HandleCallback(wCb, rCb)
	if wCb.Code != http.StatusFound {
		t.Errorf("code = %d", wCb.Code)
	}
	location := wCb.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if parsed.Query().Get("error") != "oauth_failed" || parsed.Query().Has("reason") || parsed.Query().Has("redirect") {
		t.Errorf("untrusted install mismatch redirect = %q", location)
	}
}

func TestOAuthCallback_ExchangeFailRedirectsToLoginError(t *testing.T) {
	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://a", ProviderState: pState},
		exchangeErr: errors.New("token endpoint returned 400 with secret context"),
	}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	rInit := withInstallID(httptest.NewRequest("POST", "/init?next=%2Fprofiles", nil), "42")
	wInit := httptest.NewRecorder()
	h.HandleInit(wInit, rInit)
	state := fc.gotInit.GetState()

	rCb := withInstallID(httptest.NewRequest("GET", "/cb?code=c&state="+url.QueryEscape(state), nil), "42")
	addOAuthResponseCookies(t, wInit, rCb)
	wCb := httptest.NewRecorder()
	h.HandleCallback(wCb, rCb)
	if wCb.Code != http.StatusFound {
		t.Errorf("code = %d", wCb.Code)
	}
	location := wCb.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if got := parsed.Query().Get("error"); got != "oauth_failed" {
		t.Errorf("error = %q in %q", got, location)
	}
	if got := parsed.Query().Get("redirect"); got != "/profiles" {
		t.Errorf("redirect = %q in %q", got, location)
	}
	if parsed.Query().Has("reason") || strings.Contains(location, "token endpoint") || strings.Contains(location, "secret context") {
		t.Errorf("Location leaked failure detail: %q", location)
	}
}

func TestOAuthCallback_CompleteLoginErrorRedirectsWithGenericReason(t *testing.T) {
	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://a", ProviderState: pState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "ws-1"},
	}
	fcomp := &fakeCompleter{err: errors.New("insert users failed: private db detail")}
	h, _ := newOAuthHandlerForTest(t, fc, fcomp)

	rInit := withInstallID(httptest.NewRequest("POST", "/init?next=%2Flibrary%3Ftab%3Dnew", nil), "42")
	wInit := httptest.NewRecorder()
	h.HandleInit(wInit, rInit)
	state := fc.gotInit.GetState()

	rCb := withInstallID(httptest.NewRequest("GET", "/cb?code=c&state="+url.QueryEscape(state), nil), "42")
	addOAuthResponseCookies(t, wInit, rCb)
	wCb := httptest.NewRecorder()
	h.HandleCallback(wCb, rCb)
	if wCb.Code != http.StatusFound {
		t.Errorf("code = %d", wCb.Code)
	}
	location := wCb.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if got := parsed.Query().Get("redirect"); got != "/library?tab=new" {
		t.Errorf("redirect = %q in %q", got, location)
	}
	if parsed.Query().Has("reason") || strings.Contains(location, "private db detail") {
		t.Errorf("Location leaked failure detail: %q", location)
	}
}

func TestClientIPUsesResolvedContextAndPreservesIPv6(t *testing.T) {
	tests := []struct {
		name       string
		contextIP  string
		remoteAddr string
		want       string
	}{
		{
			name:       "middleware context wins",
			contextIP:  "2001:db8::7",
			remoteAddr: "10.0.0.1:1234",
			want:       "2001:db8::7",
		},
		{
			name:       "bracketed ipv6 with port",
			remoteAddr: "[::1]:8080",
			want:       "::1",
		},
		{
			name:       "bare ipv6 from middleware remote addr",
			remoteAddr: "::1",
			want:       "::1",
		},
		{
			name:       "ipv4 with port",
			remoteAddr: "192.0.2.10:8080",
			want:       "192.0.2.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.contextIP != "" {
				r = r.WithContext(clientip.SetContext(r.Context(), tt.contextIP))
			}
			if got := clientIP(r); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
