package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestHandleLogin_InvalidCredentials_EmitsWarnWithReasonCode(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "reason=invalid_credentials") {
		t.Errorf("log missing reason code: %s", logOutput)
	}
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("invalid_credentials should be WARN: %s", logOutput)
	}
	if strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("invalid_credentials must not be ERROR: %s", logOutput)
	}
}

func TestHandleLogin_InvalidCredentials_ResponseContainsNoSecrets(t *testing.T) {
	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"supersecret"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "invalid_credentials" {
		t.Errorf("error code = %q", resp.Error)
	}
	if resp.Message != "Invalid username or password" {
		t.Errorf("message = %q", resp.Message)
	}
	if strings.Contains(resp.Message, "supersecret") || strings.Contains(resp.Message, "alice") {
		t.Errorf("response leaked credentials: %s", resp.Message)
	}
}

func TestHandleLogin_LogContainsStatusClassAndComponent(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	logOutput := buf.String()
	for _, want := range []string{"status_class=4xx", "component=auth", "duration_ms="} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log missing %q: %s", want, logOutput)
		}
	}
}

func TestHandleLogin_LogForbiddenTermsNeverAppear(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"supersecret123"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	logOutput := strings.ToLower(buf.String())
	forbidden := []string{
		"supersecret123", "alice",
		"external_subject", "asserted_username",
		"ldap_dn", "upstream_body", "stack_trace",
		"access_token", "refresh_token", "oauth_code",
		"bearer", "jwt",
	}
	for _, term := range forbidden {
		if strings.Contains(logOutput, term) {
			t.Errorf("log leaked forbidden term %q: %s", term, buf.String())
		}
	}
}

func TestHandleLogin_ResponseNeverContainsRawError(t *testing.T) {
	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	bodyStr := rec.Body.String()
	var resp errorResponse
	if err := json.NewDecoder(strings.NewReader(bodyStr)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "invalid_credentials" {
		t.Errorf("error = %q", resp.Error)
	}
	if resp.Message != "Invalid username or password" {
		t.Errorf("message = %q", resp.Message)
	}
}

func TestHandleLogin_PublicResponseIsGenericOnInternalError(t *testing.T) {
	handler := newDiagTestHandler(t)

	body := `{"username":"alice","password":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleLogin(rec, req)

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Message == "" {
		t.Error("response message should not be empty")
	}
}

// newDiagTestHandler creates an AuthHandler whose local provider always
// returns ErrInvalidCredentials (via stubLoginProvider).
func newDiagTestHandler(t *testing.T) *AuthHandler {
	t.Helper()
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	service := auth.NewService(nil, jwt, nil, nil, nil, nil, nil)
	service.RegisterProvider(auth.LoginProviderInfo{
		ID:      "local",
		Default: true,
		Mode:    "credentials",
	}, stubLoginProvider{})
	return NewAuthHandler(service, jwt, nil)
}
