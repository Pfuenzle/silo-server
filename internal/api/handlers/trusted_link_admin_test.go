package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func trustedLinkTestPool(t *testing.T) *pgxpool.Pool {
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

func seedTrustedLinkInstallation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	pluginID := fmt.Sprintf("test.trusted-link-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled)
		 VALUES ($1, '0', '/nonexistent/trusted-link-test', true)
		 RETURNING id`, pluginID).Scan(&id)
	if err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	return id
}

func seedTrustedLinkUser(t *testing.T, pool *pgxpool.Pool, suffix string) int {
	t.Helper()
	var userID int
	label := fmt.Sprintf("trusted-link-%s-%d", suffix, time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (email, username, password_hash, role, enabled, local_password_login_enabled)
		VALUES ($1, $2, 'unused', 'user', true, true)
		RETURNING id`, label+"@example.invalid", label).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func seedTrustedLinkIdentity(t *testing.T, pool *pgxpool.Pool, installationID, userID int, subject string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3)`, installationID, subject, userID)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
}

func TestAuthBinding_TrustedLinkMode_RoundTrips_whenPersisted(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "actor")
	store := plugins.NewRuntimeConfigStore(pool)
	trustedExisting := plugins.AuthBindingTrustedLinkModeTrustedExisting
	want := plugins.AuthBinding{
		InstallationID:    installationID,
		CapabilityID:      "ldap",
		Enabled:           true,
		DisplayOrder:      1,
		AutoProvision:     true,
		DefaultLogin:      false,
		AuthorizationMode: plugins.AuthBindingAuthorizationModeExternalGroupsV1,
		TrustedLinkMode:   trustedExisting,
	}
	if err := store.UpsertAuthBinding(context.Background(), actorID, want); err != nil {
		t.Fatalf("UpsertAuthBinding() error = %v", err)
	}
	got, err := store.GetAuthBinding(context.Background(), installationID, want.CapabilityID)
	if err != nil {
		t.Fatalf("GetAuthBinding() error = %v", err)
	}
	if got.TrustedLinkMode != trustedExisting {
		t.Fatalf("TrustedLinkMode = %q, want %q", got.TrustedLinkMode, trustedExisting)
	}
	if got.EffectiveTrustedLinkMode() != trustedExisting {
		t.Fatalf("EffectiveTrustedLinkMode() = %q, want %q", got.EffectiveTrustedLinkMode(), trustedExisting)
	}
}

func TestAuthBinding_DefaultLogin_AtMostOneGlobal(t *testing.T) {
	pool := trustedLinkTestPool(t)
	id1 := seedTrustedLinkInstallation(t, pool)
	id2 := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "default-actor")
	store := plugins.NewRuntimeConfigStore(pool)
	ctx := context.Background()

	binding1 := plugins.AuthBinding{
		InstallationID: id1, CapabilityID: "ldap", Enabled: true, DefaultLogin: true,
	}
	if err := store.UpsertAuthBinding(ctx, actorID, binding1); err != nil {
		t.Fatalf("first default login: %v", err)
	}
	binding2 := plugins.AuthBinding{
		InstallationID: id2, CapabilityID: "oidc", Enabled: true, DefaultLogin: true,
	}
	err := store.UpsertAuthBinding(ctx, actorID, binding2)
	if !errors.Is(err, plugins.ErrAuthBindingDefaultLoginConflict) {
		t.Fatalf("second default login: err = %v, want ErrAuthBindingDefaultLoginConflict", err)
	}
}

func TestAuthBinding_DefaultLogin_MustBeEnabled(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "must-enabled")
	store := plugins.NewRuntimeConfigStore(pool)

	err := store.UpsertAuthBinding(context.Background(), actorID, plugins.AuthBinding{
		InstallationID: installationID, CapabilityID: "ldap", Enabled: false, DefaultLogin: true,
	})
	if !errors.Is(err, plugins.ErrAuthBindingDefaultLoginNotEnabled) {
		t.Fatalf("disabled default login: err = %v, want ErrAuthBindingDefaultLoginNotEnabled", err)
	}
}

func TestAuthBinding_TrustedLinkMode_RejectsInvalidMode(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "invalid-mode")
	store := plugins.NewRuntimeConfigStore(pool)

	err := store.UpsertAuthBinding(context.Background(), actorID, plugins.AuthBinding{
		InstallationID:  installationID,
		CapabilityID:    "ldap",
		Enabled:         true,
		TrustedLinkMode: "invalid_mode",
	})
	if !errors.Is(err, plugins.ErrAuthBindingTrustedLinkModeInvalid) {
		t.Fatalf("invalid mode: err = %v, want ErrAuthBindingTrustedLinkModeInvalid", err)
	}
}

func TestAuthBinding_PolicyChange_RevokesProviderSessions(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	store := plugins.NewRuntimeConfigStore(pool)
	ctx := context.Background()
	userID := seedTrustedLinkUser(t, pool, "policy")
	provider, _ := models.NewPluginSessionProviderKey(installationID, "ldap")
	_, _ = pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at, provider_key) VALUES ('policy-sess', $1, NOW() + INTERVAL '1 hour', $2)`, userID, provider.String())

	if err := store.UpsertAuthBinding(ctx, userID, plugins.AuthBinding{
		InstallationID: installationID, CapabilityID: "ldap", Enabled: true,
		TrustedLinkMode: plugins.AuthBindingTrustedLinkModeDisabled,
	}); err != nil {
		t.Fatalf("initial binding: %v", err)
	}

	if err := store.UpsertAuthBinding(ctx, userID, plugins.AuthBinding{
		InstallationID: installationID, CapabilityID: "ldap", Enabled: true,
		TrustedLinkMode: plugins.AuthBindingTrustedLinkModeTrustedExisting,
	}); err != nil {
		t.Fatalf("change trusted link mode: %v", err)
	}

	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = 'policy-sess'`).Scan(&revokedAt); err != nil {
		t.Fatalf("check session: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("provider session was not revoked after trusted link mode change")
	}
}

func TestHandlePutAuthBinding_TrustedLinkMode_OmittedPreservesExisting(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "omit-actor")
	handler := builtinTestHandler(pool)
	ctx := context.Background()

	if err := handler.configs.UpsertAuthBinding(ctx, actorID, plugins.AuthBinding{
		InstallationID: installationID, CapabilityID: "ldap", Enabled: true,
		TrustedLinkMode: plugins.AuthBindingTrustedLinkModeTrustedExisting,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	body := `{"capability_id":"ldap","enabled":true}`
	rec := httptest.NewRecorder()
	handler.HandlePutAuthBinding(rec, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", installationID, strings.NewReader(body), actorID))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	got, err := handler.configs.GetAuthBinding(ctx, installationID, "ldap")
	if err != nil {
		t.Fatalf("GetAuthBinding: %v", err)
	}
	if got.TrustedLinkMode != plugins.AuthBindingTrustedLinkModeTrustedExisting {
		t.Fatalf("TrustedLinkMode = %q, want %q (should be preserved when omitted)", got.TrustedLinkMode, plugins.AuthBindingTrustedLinkModeTrustedExisting)
	}
}

func TestHandlePutAuthBinding_TrustedLinkMode_ExplicitOverride(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "explicit-actor")
	handler := builtinTestHandler(pool)

	body := `{"capability_id":"ldap","enabled":true,"trusted_link_mode":"trusted_existing"}`
	rec := httptest.NewRecorder()
	handler.HandlePutAuthBinding(rec, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", installationID, strings.NewReader(body), actorID))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePutAuthBinding_DefaultLoginConflict_Returns409(t *testing.T) {
	pool := trustedLinkTestPool(t)
	id1 := seedTrustedLinkInstallation(t, pool)
	id2 := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "conflict-actor")
	handler := builtinTestHandler(pool)

	body1 := `{"capability_id":"ldap","enabled":true,"default_login":true}`
	rec1 := httptest.NewRecorder()
	handler.HandlePutAuthBinding(rec1, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", id1, strings.NewReader(body1), actorID))
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first binding status = %d, body=%s", rec1.Code, rec1.Body.String())
	}

	body2 := `{"capability_id":"oidc","enabled":true,"default_login":true}`
	rec2 := httptest.NewRecorder()
	handler.HandlePutAuthBinding(rec2, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", id2, strings.NewReader(body2), actorID))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second default status = %d, want 409, body=%s", rec2.Code, rec2.Body.String())
	}
	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error != "default_login_conflict" {
		t.Fatalf("error code = %q, want %q", errResp.Error, "default_login_conflict")
	}
}

func TestHandlePutAuthBinding_MalformedTrustedLinkMode_Returns400(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	actorID := seedTrustedLinkUser(t, pool, "malformed-actor")
	handler := builtinTestHandler(pool)

	body := `{"capability_id":"ldap","enabled":true,"trusted_link_mode":"invalid_mode"}`
	rec := httptest.NewRecorder()
	handler.HandlePutAuthBinding(rec, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", installationID, strings.NewReader(body), actorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleListLinkedIdentities_ReturnsRedactedFingerprint(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	userID := seedTrustedLinkUser(t, pool, "identities")
	seedTrustedLinkIdentity(t, pool, installationID, userID, "raw-external-subject-12345")

	handler := NewTrustedLinkHandler(pool, nil)
	rec := httptest.NewRecorder()
	handler.HandleListLinkedIdentities(rec, requestWithIDParam(http.MethodGet, "/api/v1/admin/plugins/installations/0/linked-identities/0", "user_id", userID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var identities []linkedIdentityJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &identities); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(identities) != 1 {
		t.Fatalf("count = %d, want 1", len(identities))
	}
	if identities[0].SubjectFingerprint == "" {
		t.Fatal("subject_fingerprint is empty")
	}
	if identities[0].SubjectFingerprint == "raw-external-subject-12345" {
		t.Fatal("raw subject leaked through fingerprint")
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw-external-subject-12345") {
		t.Fatalf("raw subject leaked in response body: %s", body)
	}
	if identities[0].LinkMethod != "external" {
		t.Fatalf("link_method = %q, want %q", identities[0].LinkMethod, "external")
	}
}

func TestHandleListLinkedIdentities_EmptyReturnsEmptyArray(t *testing.T) {
	pool := trustedLinkTestPool(t)
	userID := seedTrustedLinkUser(t, pool, "empty")

	handler := NewTrustedLinkHandler(pool, nil)
	rec := httptest.NewRecorder()
	handler.HandleListLinkedIdentities(rec, requestWithIDParam(http.MethodGet, "/api/v1/admin/plugins/installations/0/linked-identities/0", "user_id", userID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}

type fakeCanonicalizer struct {
	previewFn func(ctx context.Context, actor auth.CanonicalizationOperator, sourceID, targetID int) (auth.CanonicalizationPreview, error)
	executeFn func(ctx context.Context, actor auth.CanonicalizationOperator, previewToken string) (auth.CanonicalizationReceipt, error)
}

func (f *fakeCanonicalizer) Preview(ctx context.Context, actor auth.CanonicalizationOperator, sourceID, targetID int) (auth.CanonicalizationPreview, error) {
	return f.previewFn(ctx, actor, sourceID, targetID)
}

func (f *fakeCanonicalizer) Execute(ctx context.Context, actor auth.CanonicalizationOperator, previewToken string) (auth.CanonicalizationReceipt, error) {
	return f.executeFn(ctx, actor, previewToken)
}

func TestHandlePreviewCanonicalization_Unauthorized_Returns401(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationPreview{}, nil
		},
	})
	rec := httptest.NewRecorder()
	body := `{"source_user_id":1,"target_user_id":2}`
	handler.HandlePreviewCanonicalization(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleExecuteCanonicalization_Unauthorized_Returns401(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationReceipt{}, nil
		},
	})
	rec := httptest.NewRecorder()
	body := `{"token":"some-token"}`
	handler.HandleExecuteCanonicalization(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleExecuteCanonicalization_StaleToken_Returns409(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, auth.ErrCanonicalizationStale
		},
	})

	body := `{"token":"stale-token"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandleExecuteCanonicalization(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error != "stale_preview" {
		t.Fatalf("error = %q, want %q", errResp.Error, "stale_preview")
	}
}

func TestHandleExecuteCanonicalization_Blocked_Returns422(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, fmt.Errorf("source_account_mode: %w", auth.ErrCanonicalizationBlocked)
		},
	})

	body := `{"token":"blocked-token"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandleExecuteCanonicalization(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleExecuteCanonicalization_Forbidden_Returns403(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, auth.ErrCanonicalizationForbidden
		},
	})

	body := `{"token":"forbidden-token"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandleExecuteCanonicalization(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleExecuteCanonicalization_IdempotencyReplay_ReturnsSameReceipt(t *testing.T) {
	pool := trustedLinkTestPool(t)
	callCount := 0
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			callCount++
			return auth.CanonicalizationReceipt{SourceDeleted: true, IdentityOwner: 42, FavoritesTransferred: 3, InterestsTransferred: 5}, nil
		},
	})

	body := `{"token":"replay-token"}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
		handler.HandleExecuteCanonicalization(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, body=%s", i, rec.Code, rec.Body.String())
		}
		var resp canonicalizationExecuteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("call %d: decode: %v", i, err)
		}
		if !resp.SourceDeleted || resp.IdentityOwner != 42 || resp.FavoritesTransferred != 3 || resp.InterestsTransferred != 5 {
			t.Fatalf("call %d: unexpected receipt: %+v", i, resp)
		}
	}
}

func TestHandlePreviewCanonicalization_HappyPath_ReturnsTokenAndDependencies(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, sourceID, targetID int) (auth.CanonicalizationPreview, error) {
			return auth.CanonicalizationPreview{
				Token:               "test-token-abc",
				SourceFingerprint:   "src-fp",
				TargetFingerprint:   "tgt-fp",
				IdentityFingerprint: "id-fp",
				Dependencies:        map[string]int{"user_favorites": 10, "profile_series_interest": 5, "plugin_auth_identities": 1},
				FavoritesSupported:  10,
				InterestsSupported:  5,
				Blockers:            []string{},
				ExpiresAt:           time.Now().Add(5 * time.Minute),
			}, nil
		},
	})

	body := `{"source_user_id":1,"target_user_id":2}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandlePreviewCanonicalization(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp canonicalizationPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token != "test-token-abc" {
		t.Fatalf("token = %q", resp.Token)
	}
	if resp.Dependencies["user_favorites"] != 10 {
		t.Fatalf("favorites dependency = %d", resp.Dependencies["user_favorites"])
	}
	if resp.FavoritesSupported != 10 {
		t.Fatalf("favorites_supported = %d", resp.FavoritesSupported)
	}
}

func TestHandlePreviewCanonicalization_SameSourceTarget_Returns400(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationPreview{}, nil
		},
	})

	body := `{"source_user_id":1,"target_user_id":1}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandlePreviewCanonicalization(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleExecuteCanonicalization_EmptyToken_Returns400(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationReceipt{}, nil
		},
	})

	body := `{"token":""}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	handler.HandleExecuteCanonicalization(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}
