package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func pluginBuiltinTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func builtinTestHandler(pool *pgxpool.Pool) *PluginHandler {
	return &PluginHandler{
		repositories:  plugins.NewRepositoryStore(pool),
		installations: plugins.NewInstallationStore(pool),
		configs:       plugins.NewRuntimeConfigStore(pool),
	}
}

func seedHandlerBuiltinInstallation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	pluginID := fmt.Sprintf("test.builtin.handler-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '0', '/nonexistent/silo-builtin-test', true, 'manual', 'builtin')
		 RETURNING id`, pluginID).Scan(&id)
	if err != nil {
		t.Fatalf("seed builtin installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	return id
}

func requestWithIDParam(method, target, param string, id int) *http.Request {
	return requestWithIDParamBody(method, target, param, id, nil)
}

func requestWithIDParamBody(method, target, param string, id int, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(param, strconv.Itoa(id))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func requestWithIDParamBodyAdmin(method, target, param string, id int, body io.Reader, actorID int) *http.Request {
	req := requestWithIDParamBody(method, target, param, id, body)
	return req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: actorID, Role: "admin"}))
}

func seedBuiltinTestUser(t *testing.T, pool *pgxpool.Pool, suffix string) int {
	t.Helper()
	var userID int
	label := fmt.Sprintf("builtin-test-%s-%d", suffix, time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (email, username, password_hash, role, enabled, local_password_login_enabled)
		VALUES ($1, $2, 'unused', 'admin', true, true)
		RETURNING id`, label+"@example.invalid", label).Scan(&userID)
	if err != nil {
		t.Fatalf("seed test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

// The manifest-less builtin row must not 500 the user-scoped plugin settings
// list the moment the migration lands (ship-blocker: the web sidebar fetches
// this for every logged-in user).
func TestHandleListUserPluginSettings_SkipsBuiltinRow(t *testing.T) {
	pool := pluginBuiltinTestPool(t)
	builtinID := seedHandlerBuiltinInstallation(t, pool)
	h := builtinTestHandler(pool)

	rec := httptest.NewRecorder()
	h.HandleListUserPluginSettings(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Installations []struct {
			ID int `json:"id"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, inst := range resp.Installations {
		if inst.ID == builtinID {
			t.Errorf("builtin installation %d leaked into user plugin settings", builtinID)
		}
	}
}

// loadUserConfigInstallation must 404 builtin ids.
func TestHandleGetUserPluginSettings_BuiltinIDIs404(t *testing.T) {
	pool := pluginBuiltinTestPool(t)
	builtinID := seedHandlerBuiltinInstallation(t, pool)
	h := builtinTestHandler(pool)

	rec := httptest.NewRecorder()
	h.HandleGetUserPluginSettings(rec, requestWithIDParam(http.MethodGet, "/api/v1/settings/plugins/0", "installation_id", builtinID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// GET /plugins/installations must filter builtin rows server-side, and the
// remaining rows carry the additive kind field.
func TestHandleListInstallations_FiltersBuiltinAndExposesKind(t *testing.T) {
	pool := pluginBuiltinTestPool(t)
	builtinID := seedHandlerBuiltinInstallation(t, pool)
	h := builtinTestHandler(pool)

	rec := httptest.NewRecorder()
	h.HandleListInstallations(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/installations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp []struct {
		ID   int    `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, inst := range resp {
		if inst.ID == builtinID {
			t.Errorf("builtin installation %d leaked into installations list", builtinID)
		}
		if inst.Kind == "" {
			t.Errorf("installation %d missing additive kind field", inst.ID)
		}
	}
}

// Mutating endpoints must reject the builtin row with a clear 4xx.
func TestBuiltinInstallationMutationsRejected(t *testing.T) {
	pool := pluginBuiltinTestPool(t)
	builtinID := seedHandlerBuiltinInstallation(t, pool)
	h := builtinTestHandler(pool)

	cases := []struct {
		name string
		call func(rec *httptest.ResponseRecorder)
	}{
		{"delete", func(rec *httptest.ResponseRecorder) {
			h.HandleDeleteInstallation(rec, requestWithIDParam(http.MethodDelete, "/api/v1/plugins/installations/0", "id", builtinID))
		}},
		{"update", func(rec *httptest.ResponseRecorder) {
			req := requestWithIDParamBody(http.MethodPatch, "/api/v1/plugins/installations/0", "id", builtinID,
				strings.NewReader(`{"enabled": false}`))
			h.HandleUpdateInstallation(rec, req)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want 4xx; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleDeleteInstallation_returnsDependencyCountsWithoutIdentityData(t *testing.T) {
	// Given
	pool := pluginBuiltinTestPool(t)
	ctx := context.Background()
	pluginID := fmt.Sprintf("test.delete.guard-%d", time.Now().UnixNano())
	var installationID, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy) VALUES ($1, '1', $2, true, 'manual') RETURNING id`, pluginID, t.TempDir()).Scan(&installationID); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, username, password_hash, role) VALUES ($1, $2, 'unused', 'user') RETURNING id`, pluginID+"@example.invalid", pluginID).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'never-return-this-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID)
	})

	// When
	rec := httptest.NewRecorder()
	builtinTestHandler(pool).HandleDeleteInstallation(rec, requestWithIDParam(http.MethodDelete, "/api/v1/plugins/installations/0", "id", installationID))

	// Then
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error        string `json:"error"`
		Dependencies struct {
			Identities int `json:"identities"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != "installation_has_dependencies" || response.Dependencies.Identities != 1 {
		t.Fatalf("response = %+v, want dependency conflict with one identity", response)
	}
	if strings.Contains(rec.Body.String(), "never-return-this-subject") {
		t.Fatalf("dependency response exposed identity data: %s", rec.Body.String())
	}
}
