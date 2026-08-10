package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestOAuthFixture_SyntheticLDAPFailure_ReturnsGenericBodyAndStructuredLog(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{"pkce_verifier": "abc"})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://ldap-idp.example/authorize", ProviderState: pState},
		exchangeErr: errors.New("LDAP bind failed: CN=svc_ldap,OU=Service Accounts,DC=corp,DC=example password=SuperSecret123! token=eyJ..."),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", handler.HandleCallback)

	initReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init?next=/library", nil)
	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, initReq)

	if initRec.Code != http.StatusFound {
		t.Fatalf("init status = %d, want 302", initRec.Code)
	}

	state := ""
	for _, cookie := range initRec.Result().Cookies() {
		_ = cookie
	}
	// Extract state from the init response by re-reading the fake client.
	if fc.gotInit == nil {
		t.Fatal("plugin not invoked")
	}
	state = fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/42/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", cbRec.Code)
	}
	location := cbRec.Header().Get("Location")
	if !strings.Contains(location, "error=oauth_failed") {
		t.Errorf("redirect missing error=oauth_failed: %s", location)
	}

	bodyStr := location
	for _, forbidden := range []string{
		"SuperSecret123!", "CN=svc_ldap", "DC=corp", "DC=example",
		"eyJ", "token=", "password=", "LDAP bind failed",
	} {
		if strings.Contains(bodyStr, forbidden) {
			t.Errorf("redirect URL leaked sensitive term %q: %s", forbidden, bodyStr)
		}
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "SuperSecret123!") {
		t.Errorf("log leaked LDAP password: %s", logOutput)
	}
	if strings.Contains(logOutput, "CN=svc_ldap") {
		t.Errorf("log leaked LDAP DN: %s", logOutput)
	}
	if strings.Contains(logOutput, "DC=corp") {
		t.Errorf("log leaked LDAP domain: %s", logOutput)
	}
	if !strings.Contains(logOutput, "reason=exchange_failed") {
		t.Errorf("log missing reason code: %s", logOutput)
	}
	if !strings.Contains(logOutput, "installation_id=42") {
		t.Errorf("log missing installation_id: %s", logOutput)
	}
	if !strings.Contains(logOutput, "component=oauth") {
		t.Errorf("log missing component: %s", logOutput)
	}
}

func TestOAuthFixture_SyntheticOIDCExchangeFailure_ReturnsGenericBodyAndStructuredLog(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{"nonce": "abc"})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://oidc.example/auth", ProviderState: pState},
		exchangeErr: errors.New("token exchange failed: client_secret=s3cr3t&code=auth_code_with_secret&access_token=eyJhbGci..."),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", handler.HandleCallback)

	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/99/init", nil))
	state := fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/99/callback?code=code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("status = %d", cbRec.Code)
	}

	location := cbRec.Header().Get("Location")
	for _, forbidden := range []string{
		"s3cr3t", "client_secret", "access_token=eyJ", "auth_code_with_secret",
	} {
		if strings.Contains(location, forbidden) {
			t.Errorf("redirect leaked OIDC secret %q: %s", forbidden, location)
		}
	}

	logOutput := logBuf.String()
	for _, forbidden := range []string{
		"s3cr3t", "client_secret", "access_token=eyJ", "auth_code_with_secret",
	} {
		if strings.Contains(logOutput, forbidden) {
			t.Errorf("log leaked OIDC secret %q: %s", forbidden, logOutput)
		}
	}
	if !strings.Contains(logOutput, "reason=exchange_failed") {
		t.Errorf("log missing reason code: %s", logOutput)
	}
	if !strings.Contains(logOutput, "installation_id=99") {
		t.Errorf("log missing installation_id: %s", logOutput)
	}
}

func TestOAuthFixture_InitPluginUnavailable_ReturnsGenericAndLogsPluginUnavailable(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fc := &fakeOAuthClient{
		initErr: errors.New("connection refused: dial tcp 10.0.0.5:50051: connect: connection refused"),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/7/init", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "50051") || strings.Contains(body, "connection refused") {
		t.Errorf("response leaked internal detail: %s", body)
	}
	if strings.Contains(body, "plugin") || strings.Contains(body, "init_authorize") {
		t.Errorf("response leaked internal terminology: %s", body)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "reason=plugin_unavailable") {
		t.Errorf("log missing reason code: %s", logOutput)
	}
	if !strings.Contains(logOutput, "installation_id=7") {
		t.Errorf("log missing installation_id: %s", logOutput)
	}
	if strings.Contains(logOutput, "10.0.0.5") {
		t.Errorf("log leaked internal IP: %s", logOutput)
	}
}

func TestOAuthFixture_CompletionFailure_ReturnsGenericAndLogsReason(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/auth", ProviderState: pState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "ws-1"},
	}
	fcomp := &fakeCompleter{
		err: errors.New("INSERT INTO users failed: duplicate key value violates unique constraint \"users_email_key\""),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, fcomp)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", handler.HandleCallback)

	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/55/init?next=/profiles", nil))
	state := fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/55/callback?code=code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("status = %d", cbRec.Code)
	}

	location := cbRec.Header().Get("Location")
	if !strings.Contains(location, "error=oauth_failed") {
		t.Errorf("redirect missing error=oauth_failed: %s", location)
	}
	if !strings.Contains(location, "redirect=%2Fprofiles") {
		t.Errorf("redirect missing next: %s", location)
	}
	for _, forbidden := range []string{
		"duplicate key", "users_email_key", "INSERT INTO",
	} {
		if strings.Contains(location, forbidden) {
			t.Errorf("redirect leaked DB detail %q: %s", forbidden, location)
		}
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "duplicate key") {
		t.Errorf("log leaked DB detail: %s", logOutput)
	}
	if !strings.Contains(logOutput, "reason=auth_internal_error") {
		t.Errorf("log missing reason code: %s", logOutput)
	}
}

func TestOAuthFixture_CorrelationIDPropagatedInLog(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fc := &fakeOAuthClient{
		initErr: errors.New("unavailable"),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil)
	r.ServeHTTP(rec, req)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "request_id=") {
		t.Errorf("log missing request_id (correlation ID): %s", logOutput)
	}
}

func TestOAuthFixture_RecordingHandlerExactKeySet(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fc := &fakeOAuthClient{initErr: errors.New("unavailable")}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", h.HandleInit)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil))

	if len(handler.records) != 1 {
		t.Fatalf("expected 1 log event, got %d", len(handler.records))
	}

	r0 := handler.records[0]
	gotKeys := make(map[string]bool)
	for _, k := range r0.keys {
		gotKeys[k] = true
	}

	for _, want := range []string{"component", "reason", "status_class", "duration_ms", "installation_id", "request_id"} {
		if !gotKeys[want] {
			t.Errorf("log missing required key %q; got keys %v", want, r0.keys)
		}
	}

	forbidden := []string{
		"username", "password", "email", "external_subject", "asserted_username",
		"ldap_dn", "upstream_body", "sql", "stack", "access_token", "refresh_token",
		"code", "state", "oauth_code", "claims", "provider_response", "user_agent", "ip_address",
	}
	for _, term := range forbidden {
		if gotKeys[term] {
			t.Errorf("log has forbidden key %q", term)
		}
	}

	if r0.values["reason"] != "plugin_unavailable" {
		t.Errorf("reason = %q, want plugin_unavailable", r0.values["reason"])
	}
	if r0.values["component"] != "oauth" {
		t.Errorf("component = %q, want oauth", r0.values["component"])
	}
	if r0.values["installation_id"] != "42" {
		t.Errorf("installation_id = %q, want 42", r0.values["installation_id"])
	}
}

func TestOAuthFixture_InitBadInstallID_ReturnsGenericBadRequest(t *testing.T) {
	handler, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
	cases := []string{"", "abc", "-1", "0"}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r := withInstallID(httptest.NewRequest(http.MethodPost, "/init", nil), c)
		handler.HandleInit(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("install_id=%q → status %d, want 400", c, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "install_id") || strings.Contains(body, "invalid") {
			t.Errorf("install_id=%q → body leaked detail: %s", c, body)
		}
	}
}

func TestOAuthFixture_FailureResponsesIncludeCorrelationID(t *testing.T) {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", func(w http.ResponseWriter, r *http.Request) {
		h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
		h.HandleInit(w, r)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/bad_id/init", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Errorf("400 response missing X-Request-Id header")
	}
}

func TestOAuthFixture_InitPluginUnavailable_CorrelationIDHeader(t *testing.T) {
	fc := &fakeOAuthClient{initErr: errors.New("connection refused")}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/7/init", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Errorf("502 response missing X-Request-Id header")
	}
}

func TestOAuthFixture_CallbackExchangeFailure_CorrelationIDHeader(t *testing.T) {
	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/auth", ProviderState: pState},
		exchangeErr: errors.New("exchange failed"),
	}
	handler, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", handler.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", handler.HandleCallback)

	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil))
	state := fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/42/callback?code=code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", cbRec.Code)
	}
	if got := cbRec.Header().Get("X-Request-Id"); got == "" {
		t.Errorf("302 redirect missing X-Request-Id header")
	}
}

func TestOAuthFixture_DiagnosticRecordsActualStatusAndDuration(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fc := &fakeOAuthClient{initErr: errors.New("unavailable")}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", h.HandleInit)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil))

	if len(handler.records) != 1 {
		t.Fatalf("expected 1 log event, got %d", len(handler.records))
	}

	r0 := handler.records[0]
	if r0.values["status_class"] != "5xx" {
		t.Errorf("status_class = %q, want 5xx (actual BadGateway status)", r0.values["status_class"])
	}
	if r0.values["duration_ms"] == "" {
		t.Errorf("duration_ms is empty")
	}
}

func TestOAuthFixture_CallbackDiagnosticRecordsFailureStatusClass(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/auth", ProviderState: pState},
		exchangeErr: errors.New("exchange failed"),
	}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", h.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", h.HandleCallback)

	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil))
	state := fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/42/callback?code=code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if len(handler.records) == 0 {
		t.Fatal("expected at least 1 log event from callback failure")
	}

	last := handler.records[len(handler.records)-1]
	if last.values["status_class"] != "5xx" {
		t.Errorf("callback status_class = %q, want 5xx (exchange_failed → 502)", last.values["status_class"])
	}
	if last.values["reason"] != "exchange_failed" {
		t.Errorf("reason = %q, want exchange_failed", last.values["reason"])
	}
}

func TestOAuthFixture_LinkingUnsupported_RecordsDistinctReason(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/auth", ProviderState: pState},
		exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "ws-1"},
	}
	fcomp := &fakeCompleter{pair: &TokenPair{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900}, user: &models.User{ID: 7}}
	h, store := newOAuthHandlerForTest(t, fc, fcomp)

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
		ProviderState: []byte(`{}`),
		NextURL:       "/me",
		ExpiresAt:     time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("insert linking session: %v", err)
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", h.HandleCallback)

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/42/callback?code=code&state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(&http.Cookie{Name: oauthBrowserCookiePrefix + "nonce", Value: "nonce"})
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", cbRec.Code)
	}

	if len(handler.records) == 0 {
		t.Fatal("expected log event from linking rejection")
	}
	last := handler.records[len(handler.records)-1]
	if last.values["reason"] != "linking_unsupported" {
		t.Errorf("reason = %q, want linking_unsupported", last.values["reason"])
	}
	if last.values["status_class"] != "4xx" {
		t.Errorf("status_class = %q, want 4xx (linking_unsupported → 403)", last.values["status_class"])
	}
}

func TestOAuthFixture_OAuthLogViaDiagnosticHasStatusAndDuration(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pState, _ := structpb.NewStruct(map[string]any{})
	fc := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/auth", ProviderState: pState},
		exchangeErr: errors.New("token exchange failed with secrets"),
	}
	h, _ := newOAuthHandlerForTest(t, fc, &fakeCompleter{})

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Post("/api/v1/auth/oauth/{install_id}/init", h.HandleInit)
	r.Get("/api/v1/auth/oauth/{install_id}/callback", h.HandleCallback)

	initRec := httptest.NewRecorder()
	r.ServeHTTP(initRec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/42/init", nil))
	state := fc.gotInit.GetState()

	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/42/callback?code=code&state="+url.QueryEscape(state), nil)
	addOAuthResponseCookies(t, initRec, cbReq)
	cbRec := httptest.NewRecorder()
	r.ServeHTTP(cbRec, cbReq)

	if len(handler.records) == 0 {
		t.Fatal("expected at least 1 log event from callback failure")
	}

	last := handler.records[len(handler.records)-1]
	gotKeys := make(map[string]bool)
	for _, k := range last.keys {
		gotKeys[k] = true
	}
	if !gotKeys["status_class"] {
		t.Errorf("log missing status_class")
	}
	if !gotKeys["duration_ms"] {
		t.Errorf("log missing duration_ms")
	}
	if !gotKeys["reason"] {
		t.Errorf("log missing reason")
	}
	if last.values["status_class"] != "5xx" {
		t.Errorf("status_class = %q, want 5xx (exchange_failed → 502)", last.values["status_class"])
	}
	if last.values["reason"] != "exchange_failed" {
		t.Errorf("reason = %q, want exchange_failed", last.values["reason"])
	}
}

type recordingSlogEntry struct {
	keys   []string
	values map[string]string
	level  slog.Level
	msg    string
}

type recordingSlogHandler struct {
	records []recordingSlogEntry
}

func (h *recordingSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	entry := recordingSlogEntry{
		level:  r.Level,
		msg:    r.Message,
		values: make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		entry.keys = append(entry.keys, a.Key)
		entry.values[a.Key] = a.Value.String()
		return true
	})
	h.records = append(h.records, entry)
	return nil
}

func (h *recordingSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingSlogHandler) WithGroup(_ string) slog.Handler      { return h }

func TestOAuthFixture_CompleteErrorResponsesIncludeCorrelationHeader(t *testing.T) {
	t.Run("nil_completion_store", func(t *testing.T) {
		h := NewOAuthHandler(OAuthHandlerDeps{
			Store:           &nonCompletingOAuthStore{},
			StateSecret:     []byte("test-secret"),
			CompletionStore: nil,
		})
		r := chi.NewRouter()
		r.Use(chimw.RequestID)
		r.Post("/api/v1/auth/oauth/complete", h.HandleComplete)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"test"}`))
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
		if got := rec.Header().Get("X-Request-Id"); got == "" {
			t.Error("missing X-Request-Id correlation header")
		}
	})

	t.Run("empty_code", func(t *testing.T) {
		h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
		r := chi.NewRouter()
		r.Use(chimw.RequestID)
		r.Post("/api/v1/auth/oauth/complete", h.HandleComplete)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":""}`))
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if got := rec.Header().Get("X-Request-Id"); got == "" {
			t.Error("missing X-Request-Id correlation header")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
		r := chi.NewRouter()
		r.Use(chimw.RequestID)
		r.Post("/api/v1/auth/oauth/complete", h.HandleComplete)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`not-json`))
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if got := rec.Header().Get("X-Request-Id"); got == "" {
			t.Error("missing X-Request-Id correlation header")
		}
	})

	t.Run("expired_code", func(t *testing.T) {
		h, _ := newOAuthHandlerForTest(t, &fakeOAuthClient{}, &fakeCompleter{})
		r := chi.NewRouter()
		r.Use(chimw.RequestID)
		r.Post("/api/v1/auth/oauth/complete", h.HandleComplete)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"nonexistent"}`))
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("X-Request-Id"); got == "" {
			t.Error("missing X-Request-Id correlation header")
		}
	})
}

func TestAPIKeyLastUsedTracker_DoesNotLogRawError(t *testing.T) {
	var mu sync.Mutex
	var logBuf bytes.Buffer
	written := make(chan struct{}, 1)
	handler := &notifyingHandler{
		inner: slog.NewTextHandler(&muBuf{mu: &mu, buf: &logBuf}, &slog.HandlerOptions{Level: slog.LevelDebug}),
		done:  written,
		once:  &sync.Once{},
	}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	updater := &failingLastUsedUpdater{err: errors.New("connection refused: dial tcp 10.0.0.5:5432")}
	tracker := NewAPIKeyLastUsedTracker(updater, nil)
	tracker.Touch(42)

	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for log write")
	}

	mu.Lock()
	logOutput := logBuf.String()
	mu.Unlock()

	if strings.Contains(logOutput, "connection refused") {
		t.Errorf("log leaked raw error: %s", logOutput)
	}
	if strings.Contains(logOutput, "10.0.0.5") {
		t.Errorf("log leaked internal IP: %s", logOutput)
	}
	if strings.Contains(logOutput, "5432") {
		t.Errorf("log leaked port: %s", logOutput)
	}
	if !strings.Contains(logOutput, "api key last-used update failed") {
		t.Errorf("log missing safe message: %s", logOutput)
	}
}

type muBuf struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (b *muBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

type notifyingHandler struct {
	inner slog.Handler
	done  chan struct{}
	once  *sync.Once
}

func (h *notifyingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *notifyingHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)
	h.once.Do(func() { close(h.done) })
	return err
}

func (h *notifyingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &notifyingHandler{inner: h.inner.WithAttrs(attrs), done: h.done, once: h.once}
}

func (h *notifyingHandler) WithGroup(name string) slog.Handler {
	return &notifyingHandler{inner: h.inner.WithGroup(name), done: h.done, once: h.once}
}

type failingLastUsedUpdater struct{ err error }

func (u *failingLastUsedUpdater) UpdateLastUsed(_ context.Context, _ int64) error {
	return u.err
}

type nonCompletingOAuthStore struct{ rows map[string]OAuthSession }

func (s *nonCompletingOAuthStore) Insert(_ context.Context, sess OAuthSession) error {
	if s.rows == nil {
		s.rows = make(map[string]OAuthSession)
	}
	s.rows[sess.State] = sess
	return nil
}
func (s *nonCompletingOAuthStore) Get(_ context.Context, state string) (OAuthSession, error) {
	if sess, ok := s.rows[state]; ok {
		return sess, nil
	}
	return OAuthSession{}, ErrOAuthSessionNotFound
}
func (s *nonCompletingOAuthStore) GetAndDelete(ctx context.Context, state string) (OAuthSession, error) {
	sess, err := s.Get(ctx, state)
	if err != nil {
		return sess, err
	}
	delete(s.rows, state)
	return sess, nil
}
func (s *nonCompletingOAuthStore) DeleteExpired(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}
