package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type Canonicalizer interface {
	Preview(ctx context.Context, actor auth.CanonicalizationOperator, sourceID, targetID int) (auth.CanonicalizationPreview, error)
	Execute(ctx context.Context, actor auth.CanonicalizationOperator, previewToken string) (auth.CanonicalizationReceipt, error)
}

type TrustedLinkHandler struct {
	pool          *pgxpool.Pool
	canonicalizer Canonicalizer
}

func NewTrustedLinkHandler(pool *pgxpool.Pool, canonicalizer Canonicalizer) *TrustedLinkHandler {
	return &TrustedLinkHandler{pool: pool, canonicalizer: canonicalizer}
}

type linkedIdentityJSON struct {
	InstallationID      int       `json:"installation_id"`
	ProviderDisplayName string    `json:"provider_display_name"`
	CapabilityID        string    `json:"capability_id"`
	LinkMethod          string    `json:"link_method"`
	LinkedAt            time.Time `json:"linked_at"`
	SubjectFingerprint  string    `json:"subject_fingerprint"`
}

type canonicalizationPreviewRequest struct {
	SourceUserID int `json:"source_user_id"`
	TargetUserID int `json:"target_user_id"`
}

type canonicalizationPreviewResponse struct {
	Token               string         `json:"token"`
	SourceFingerprint   string         `json:"source_fingerprint"`
	TargetFingerprint   string         `json:"target_fingerprint"`
	IdentityFingerprint string         `json:"identity_fingerprint"`
	Dependencies        map[string]int `json:"dependencies"`
	FavoritesSupported  int            `json:"favorites_supported"`
	InterestsSupported  int            `json:"interests_supported"`
	Blockers            []string       `json:"blockers"`
	ExpiresAt           time.Time      `json:"expires_at"`
}

type canonicalizationExecuteRequest struct {
	Token string `json:"token"`
}

type canonicalizationExecuteResponse struct {
	SourceDeleted        bool `json:"source_deleted"`
	IdentityOwner        int  `json:"identity_owner"`
	FavoritesTransferred int  `json:"favorites_transferred"`
	InterestsTransferred int  `json:"interests_transferred"`
}

func (h *TrustedLinkHandler) HandleListLinkedIdentities(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(chi.URLParam(r, "user_id"))
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT pai.plugin_installation_id, COALESCE(pab.capability_id, ''), COALESCE(pi.plugin_id, ''), pai.external_subject, pai.created_at
		FROM plugin_auth_identities pai
		LEFT JOIN plugin_auth_bindings pab ON pab.plugin_installation_id = pai.plugin_installation_id
		LEFT JOIN plugin_installations pi ON pi.id = pai.plugin_installation_id
		WHERE pai.user_id = $1
		ORDER BY pai.plugin_installation_id ASC
	`, userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "listing linked identities", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list linked identities")
		return
	}
	defer rows.Close()

	var identities []linkedIdentityJSON
	for rows.Next() {
		var (
			installID int
			capID     string
			pluginID  string
			subject   string
			linkedAt  time.Time
		)
		if err := rows.Scan(&installID, &capID, &pluginID, &subject, &linkedAt); err != nil {
			slog.ErrorContext(r.Context(), "scanning linked identity", "component", "api", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan linked identity")
			return
		}
		displayName := pluginID
		if capID != "" {
			displayName = pluginID + "/" + capID
		}
		identities = append(identities, linkedIdentityJSON{
			InstallationID:      installID,
			ProviderDisplayName: displayName,
			CapabilityID:        capID,
			LinkMethod:          "external",
			LinkedAt:            linkedAt,
			SubjectFingerprint:  fingerprintSubject(subject),
		})
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(r.Context(), "iterating linked identities", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to iterate linked identities")
		return
	}
	if identities == nil {
		identities = []linkedIdentityJSON{}
	}
	writeJSON(w, http.StatusOK, identities)
}

func (h *TrustedLinkHandler) HandlePreviewCanonicalization(w http.ResponseWriter, r *http.Request) {
	if h.canonicalizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Canonicalization is not configured")
		return
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if claims.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
		return
	}

	var req canonicalizationPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.SourceUserID <= 0 || req.TargetUserID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "source_user_id and target_user_id are required")
		return
	}
	if req.SourceUserID == req.TargetUserID {
		writeError(w, http.StatusBadRequest, "bad_request", "source_user_id and target_user_id must be different")
		return
	}

	preview, err := h.canonicalizer.Preview(r.Context(), auth.CanonicalizationOperator{
		UserID:  claims.UserID,
		IsAdmin: true,
	}, req.SourceUserID, req.TargetUserID)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrCanonicalizationForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Canonicalization not permitted")
		default:
			slog.ErrorContext(r.Context(), "canonicalization preview", "component", "api", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to preview canonicalization")
		}
		return
	}
	blockers := preview.Blockers
	if blockers == nil {
		blockers = []string{}
	}
	writeJSON(w, http.StatusOK, canonicalizationPreviewResponse{
		Token:               preview.Token,
		SourceFingerprint:   preview.SourceFingerprint,
		TargetFingerprint:   preview.TargetFingerprint,
		IdentityFingerprint: preview.IdentityFingerprint,
		Dependencies:        preview.Dependencies,
		FavoritesSupported:  preview.FavoritesSupported,
		InterestsSupported:  preview.InterestsSupported,
		Blockers:            blockers,
		ExpiresAt:           preview.ExpiresAt,
	})
}

func (h *TrustedLinkHandler) HandleExecuteCanonicalization(w http.ResponseWriter, r *http.Request) {
	if h.canonicalizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Canonicalization is not configured")
		return
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if claims.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
		return
	}

	var req canonicalizationExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token is required")
		return
	}

	receipt, err := h.canonicalizer.Execute(r.Context(), auth.CanonicalizationOperator{
		UserID:  claims.UserID,
		IsAdmin: true,
	}, req.Token)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrCanonicalizationStale):
			writeError(w, http.StatusConflict, "stale_preview", "Preview token is stale or snapshot has changed")
		case errors.Is(err, auth.ErrCanonicalizationBlocked):
			writeError(w, http.StatusUnprocessableEntity, "canonicalization_blocked", fmt.Sprintf("Canonicalization is blocked: %v", err))
		case errors.Is(err, auth.ErrCanonicalizationForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Canonicalization not permitted")
		default:
			slog.ErrorContext(r.Context(), "canonicalization execute", "component", "api", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to execute canonicalization")
		}
		return
	}
	writeJSON(w, http.StatusOK, canonicalizationExecuteResponse{
		SourceDeleted:        receipt.SourceDeleted,
		IdentityOwner:        receipt.IdentityOwner,
		FavoritesTransferred: receipt.FavoritesTransferred,
		InterestsTransferred: receipt.InterestsTransferred,
	})
}

func fingerprintSubject(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:16])
}
