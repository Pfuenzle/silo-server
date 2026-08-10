package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func setupTrustedLinkRouteWithAuth(t *testing.T, pool *pgxpool.Pool, canonicalizer Canonicalizer) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	trustedLinkHandler := NewTrustedLinkHandler(pool, canonicalizer)

	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Route("/plugins", func(r chi.Router) {
			r.Route("/installations", func(r chi.Router) {
				r.Get("/{id}/linked-identities/{user_id}", trustedLinkHandler.HandleListLinkedIdentities)
			})
		})
		r.Post("/canonicalization/preview", trustedLinkHandler.HandlePreviewCanonicalization)
		r.Post("/canonicalization/execute", trustedLinkHandler.HandleExecuteCanonicalization)
	})
	return r
}

func TestRoute_CanonicalizationPreview_Unauthenticated_Returns401(t *testing.T) {
	pool := trustedLinkTestPool(t)
	r := setupTrustedLinkRouteWithAuth(t, pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationPreview{}, nil
		},
	})

	body := `{"source_user_id":1,"target_user_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_CanonicalizationExecute_Unauthenticated_Returns401(t *testing.T) {
	pool := trustedLinkTestPool(t)
	r := setupTrustedLinkRouteWithAuth(t, pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationReceipt{}, nil
		},
	})

	body := `{"token":"some-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_LinkedIdentities_Unauthenticated_Returns401(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, nil)

	r := chi.NewRouter()
	r.Get("/api/v1/admin/plugins/installations/{id}/linked-identities/{user_id}", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleListLinkedIdentities(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/installations/1/linked-identities/1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_CanonicalizationPreview_NonAdmin_Returns403(t *testing.T) {
	pool := trustedLinkTestPool(t)
	r := chi.NewRouter()
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationPreview{}, nil
		},
	})

	r.Post("/api/v1/admin/canonicalization/preview", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if claims.Role != "admin" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		handler.HandlePreviewCanonicalization(w, r)
	})

	body := `{"source_user_id":1,"target_user_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 99, Role: "user"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_CanonicalizationPreview_AdminSuccess_ReturnsTokenAndDependencies(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, actor auth.CanonicalizationOperator, sourceID, targetID int) (auth.CanonicalizationPreview, error) {
			if !actor.IsAdmin {
				t.Fatal("expected admin actor")
			}
			return auth.CanonicalizationPreview{
				Token:               "route-test-token",
				SourceFingerprint:   "src-fp",
				TargetFingerprint:   "tgt-fp",
				IdentityFingerprint: "id-fp",
				Dependencies:        map[string]int{"user_favorites": 5},
				FavoritesSupported:  5,
				InterestsSupported:  2,
				Blockers:            []string{},
				ExpiresAt:           time.Now().Add(5 * time.Minute),
			}, nil
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/preview", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandlePreviewCanonicalization(w, r)
	})

	body := `{"source_user_id":10,"target_user_id":20}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	req.Header.Set("X-Request-Id", "admin-preview-correlation")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp canonicalizationPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token != "route-test-token" {
		t.Fatalf("token = %q, want route-test-token", resp.Token)
	}
	if resp.Dependencies["user_favorites"] != 5 {
		t.Fatalf("favorites dependency = %d", resp.Dependencies["user_favorites"])
	}
}

func TestRoute_CanonicalizationExecute_StaleToken_Returns409(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, auth.ErrCanonicalizationStale
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/execute", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleExecuteCanonicalization(w, r)
	})

	body := `{"token":"stale-route-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

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
		t.Fatalf("error = %q, want stale_preview", errResp.Error)
	}
}

func TestRoute_CanonicalizationExecute_Blocked_Returns422(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, auth.ErrCanonicalizationBlocked
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/execute", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleExecuteCanonicalization(w, r)
	})

	body := `{"token":"blocked-route-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_CanonicalizationExecute_Forbidden_Returns403(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			return auth.CanonicalizationReceipt{}, auth.ErrCanonicalizationForbidden
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/execute", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleExecuteCanonicalization(w, r)
	})

	body := `{"token":"forbidden-route-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_LinkedIdentities_AdminSuccess_ReturnsRedactedFingerprints(t *testing.T) {
	pool := trustedLinkTestPool(t)
	installationID := seedTrustedLinkInstallation(t, pool)
	userID := seedTrustedLinkUser(t, pool, "route-identities")
	seedTrustedLinkIdentity(t, pool, installationID, userID, "raw-route-subject-xyz")

	handler := NewTrustedLinkHandler(pool, nil)
	r := chi.NewRouter()
	r.Get("/api/v1/admin/plugins/installations/{id}/linked-identities/{user_id}", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleListLinkedIdentities(w, r)
	})

	url := fmt.Sprintf("/api/v1/admin/plugins/installations/%d/linked-identities/%d", installationID, userID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw-route-subject-xyz") {
		t.Fatalf("raw subject leaked in route response: %s", body)
	}
	var identities []linkedIdentityJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &identities); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(identities) != 1 {
		t.Fatalf("count = %d, want 1", len(identities))
	}
	if identities[0].SubjectFingerprint == "" || identities[0].SubjectFingerprint == "raw-route-subject-xyz" {
		t.Fatalf("fingerprint = %q, expected redacted SHA-256", identities[0].SubjectFingerprint)
	}
}

func TestRoute_CanonicalizationExecute_IdempotencyReplay_ReturnsSameReceipt(t *testing.T) {
	pool := trustedLinkTestPool(t)
	callCount := 0
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		executeFn: func(_ context.Context, _ auth.CanonicalizationOperator, _ string) (auth.CanonicalizationReceipt, error) {
			callCount++
			return auth.CanonicalizationReceipt{SourceDeleted: true, IdentityOwner: 42, FavoritesTransferred: 3, InterestsTransferred: 5}, nil
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/execute", func(w http.ResponseWriter, r *http.Request) {
		claims := apimw.GetClaims(r.Context())
		if claims == nil || claims.Role != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.HandleExecuteCanonicalization(w, r)
	})

	body := `{"token":"replay-route-token"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/execute", strings.NewReader(body))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
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

func TestRoute_CanonicalizationPreview_SameSourceTarget_Returns400(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			t.Fatal("should not reach canonicalizer")
			return auth.CanonicalizationPreview{}, nil
		},
	})

	r := chi.NewRouter()
	r.Post("/api/v1/admin/canonicalization/preview", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePreviewCanonicalization(w, r)
	})

	body := `{"source_user_id":5,"target_user_id":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoute_CorrelationHeaders_PropagatedOnSuccessAndFailure(t *testing.T) {
	pool := trustedLinkTestPool(t)
	handler := NewTrustedLinkHandler(pool, &fakeCanonicalizer{
		previewFn: func(_ context.Context, _ auth.CanonicalizationOperator, _, _ int) (auth.CanonicalizationPreview, error) {
			return auth.CanonicalizationPreview{
				Token:     "corr-test",
				ExpiresAt: time.Now().Add(5 * time.Minute),
			}, nil
		},
	})

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rid := r.Header.Get("X-Request-Id"); rid != "" {
				w.Header().Set("X-Request-Id", rid)
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Post("/api/v1/admin/canonicalization/preview", handler.HandlePreviewCanonicalization)

	body := `{"source_user_id":1,"target_user_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"}))
	req.Header.Set("X-Request-Id", "corr-abc-123")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Request-Id"); got != "corr-abc-123" {
		t.Fatalf("X-Request-Id = %q, want corr-abc-123", got)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/canonicalization/preview", strings.NewReader(`{}`))
	req2.Header.Set("X-Request-Id", "corr-fail-456")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code == http.StatusOK {
		t.Fatal("expected failure for unauthenticated request")
	}
	if got := rec2.Header().Get("X-Request-Id"); got != "corr-fail-456" {
		t.Fatalf("X-Request-Id on failure = %q, want corr-fail-456", got)
	}
}
