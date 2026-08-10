package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestOAuthInit_SanitizesNestedPluginErrorInLog(t *testing.T) {
	// Given
	const sentinel = "oauth-secret-token-code-state"
	logs := captureOAuthLogs(t)

	client := &fakeOAuthClient{initErr: errors.Join(errors.New("transport failure"), errors.New(sentinel))}
	handler, _ := newOAuthHandlerForTest(t, client, &fakeCompleter{})
	req := withInstallID(httptest.NewRequest(http.MethodPost, "/init", nil), "42")
	recorder := httptest.NewRecorder()

	// When
	handler.HandleInit(recorder, req)

	// Then
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	assertSanitizedOAuthLog(t, logs.String(), sentinel, "oauth init_authorize failed", "plugin_unavailable")
}

func TestOAuthCallback_SanitizesNestedPluginErrorInLog(t *testing.T) {
	// Given
	const sentinel = "oauth-secret-token-code-state"
	logs := captureOAuthLogs(t)
	providerState, err := structpb.NewStruct(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeOAuthClient{
		initResp:    &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://issuer.example/authorize", ProviderState: providerState},
		exchangeErr: errors.Join(errors.New("upstream failure"), errors.New(sentinel)),
	}
	handler, _ := newOAuthHandlerForTest(t, client, &fakeCompleter{})
	initResponse := httptest.NewRecorder()
	handler.HandleInit(initResponse, withInstallID(httptest.NewRequest(http.MethodPost, "/init", nil), "42"))
	recorder := httptest.NewRecorder()

	// When
	callback := withInstallID(httptest.NewRequest(http.MethodGet, "/callback?code=opaque-code&state="+url.QueryEscape(client.gotInit.GetState()), nil), "42")
	addOAuthResponseCookies(t, initResponse, callback)
	handler.HandleCallback(recorder, callback)

	// Then
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	assertSanitizedOAuthLog(t, logs.String(), sentinel, "oauth exchange_code failed", "exchange_failed")
}

func captureOAuthLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &logs
}

func assertSanitizedOAuthLog(t *testing.T, logs, sentinel, _, reason string) {
	t.Helper()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log leaked sensitive upstream error: %s", logs)
	}
	for _, want := range []string{"auth failure", "installation_id=42", "reason=" + reason} {
		if !strings.Contains(logs, want) {
			t.Errorf("log = %q, missing %q", logs, want)
		}
	}
}
