//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type adminAPI struct {
	baseURL string
	client  *http.Client
	token   string
}

type installationResponse struct {
	ID          int    `json:"id"`
	InstallPath string `json:"install_path"`
	Version     string `json:"version"`
}

const (
	pluginUploadChunkSize = 512 * 1024
	breakGlassUsername    = "breakglass"
	breakGlassPassword    = "correct-horse-battery-staple"
)

func newAdminAPI(baseURL string) *adminAPI {
	return &adminAPI{baseURL: baseURL, client: &http.Client{Timeout: integrationHTTPTimeout}}
}

func (a *adminAPI) rebind(baseURL string) {
	a.baseURL = baseURL
}

func (a *adminAPI) createBreakGlassAdmin(t *testing.T, ctx context.Context) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/setup", "", map[string]any{
		"username": breakGlassUsername, "email": "breakglass@example.test", "password": breakGlassPassword, "create_default_profile": true, "default_profile_name": "Break Glass",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		t.Fatalf("create break-glass admin: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	decodeJSON(t, response.Body, &payload)
	if payload.AccessToken == "" {
		t.Fatal("break-glass admin response omitted access token")
	}
	a.token = payload.AccessToken
}

func (a *adminAPI) loginBreakGlassAdmin(t *testing.T, ctx context.Context, password string) string {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": breakGlassUsername, "password": password})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("break-glass login: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	var payload oauthPair
	decodeJSON(t, response.Body, &payload)
	if payload.AccessToken == "" {
		t.Fatal("break-glass login response omitted access token")
	}
	return payload.AccessToken
}

func (a *adminAPI) assertBreakGlassAdmin(t *testing.T, ctx context.Context, stage string) {
	t.Helper()
	token := a.loginBreakGlassAdmin(t, ctx, breakGlassPassword)
	response := a.request(t, ctx, http.MethodGet, "/api/v1/auth/me", token, nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("break-glass /me at %s: status %d: %s", stage, response.StatusCode, responseBody(t, response))
	}
	invalid := a.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": breakGlassUsername, "password": "wrong-password"})
	defer invalid.Body.Close()
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("break-glass negative login at %s: status %d", stage, invalid.StatusCode)
	}
}

func (a *adminAPI) uploadPlugin(t *testing.T, ctx context.Context, archive string) installationResponse {
	t.Helper()
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read packaged OIDC artifact: %v", err)
	}
	var create *http.Response
	for attempt := 0; ; attempt++ {
		create = a.requestJSON(t, ctx, http.MethodPost, "/api/v1/admin/plugins/uploads/chunked", a.token, map[string]any{"filename": filepath.Base(archive), "size_bytes": len(data), "chunk_size": pluginUploadChunkSize})
		if create.StatusCode == http.StatusCreated || create.StatusCode != http.StatusInternalServerError || attempt == 2 {
			break
		}
		_ = create.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	defer create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create plugin upload: status %d: %s", create.StatusCode, responseBody(t, create))
	}
	var session struct {
		UploadID string `json:"upload_id"`
	}
	decodeJSON(t, create.Body, &session)
	if session.UploadID == "" {
		t.Fatal("plugin upload response omitted upload_id")
	}
	for offset, index := 0, 0; offset < len(data); offset, index = offset+pluginUploadChunkSize, index+1 {
		end := min(offset+pluginUploadChunkSize, len(data))
		for attempt := 0; ; attempt++ {
			chunk := a.request(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/uploads/chunked/%s/chunks/%d", session.UploadID, index), a.token, bytes.NewReader(data[offset:end]), "application/octet-stream")
			if chunk.StatusCode == http.StatusOK {
				_ = chunk.Body.Close()
				break
			}
			body := responseBody(t, chunk)
			_ = chunk.Body.Close()
			if chunk.StatusCode != http.StatusInternalServerError || attempt == 2 {
				t.Fatalf("upload plugin chunk %d: status %d: %s", index, chunk.StatusCode, body)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	complete := a.request(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/admin/plugins/uploads/chunked/%s/complete", session.UploadID), a.token, nil, "application/json")
	defer complete.Body.Close()
	if complete.StatusCode != http.StatusCreated {
		t.Fatalf("complete plugin upload: status %d: %s", complete.StatusCode, responseBody(t, complete))
	}
	var installation installationResponse
	decodeJSON(t, complete.Body, &installation)
	if installation.ID == 0 || installation.InstallPath == "" {
		t.Fatalf("invalid packaged installation response: %+v", installation)
	}
	return installation
}

func (a *adminAPI) configureOIDC(t *testing.T, ctx context.Context, installationID int, issuer string) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d/config", installationID), a.token, map[string]any{"key": "oidc", "value": map[string]any{"issuer_url": issuer, "client_id": "client-id", "client_secret": "integration-client-secret", "connect_timeout_seconds": 5, "request_timeout_seconds": 5, "allow_private_networks": true}})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("configure OIDC: status %d: %s", response.StatusCode, responseBody(t, response))
	}
}

func (a *adminAPI) enableOIDC(t *testing.T, ctx context.Context, installationID int) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d/auth-binding", installationID), a.token, map[string]any{"capability_id": "oidc", "enabled": true, "auto_provision": true, "authorization_mode": "none"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Silo-Restart-Required") != "true" {
		t.Fatalf("enable OIDC binding: status %d restart=%q: %s", response.StatusCode, response.Header.Get("X-Silo-Restart-Required"), responseBody(t, response))
	}
}

func (a *adminAPI) setInstallationEnabled(t *testing.T, ctx context.Context, installationID int, enabled bool) installationResponse {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d", installationID), a.token, map[string]bool{"enabled": enabled})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set plugin enabled=%t: status %d: %s", enabled, response.StatusCode, responseBody(t, response))
	}
	var installation installationResponse
	decodeJSON(t, response.Body, &installation)
	if installation.ID != installationID {
		t.Fatalf("enabled installation id=%d, want %d", installation.ID, installationID)
	}
	return installation
}

func (a *adminAPI) requestJSON(t *testing.T, ctx context.Context, method, path, token string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s %s: %v", method, path, err)
	}
	return a.request(t, ctx, method, path, token, bytes.NewReader(encoded), "application/json")
}

func (a *adminAPI) request(t *testing.T, ctx context.Context, method, path, token string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Content-Type", contentType)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.client.Do(request)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, request.URL, err)
	}
	return response
}

func decodeJSON(t *testing.T, body io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(target); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
}

func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "<failed to read response body>"
	}
	return string(data)
}
