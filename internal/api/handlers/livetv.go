package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/livetv"
)

type LiveTVHandler struct {
	folders        *catalog.FolderRepository
	repo           *livetv.PostgresRepository
	runtime        *livetv.Runtime
	playback       *livetv.LivePlaybackService
	playbackOrigin string
	artwork        interface {
		PresignURL(context.Context, string, string) string
	}
	objectStore interface {
		Bucket() string
		PresignGetURL(context.Context, string, string, time.Duration) (string, error)
	}
}

func (h *LiveTVHandler) SetArtworkResolver(resolver interface {
	PresignURL(context.Context, string, string) string
}) {
	if h != nil {
		h.artwork = resolver
	}
}

func (h *LiveTVHandler) SetArtworkStore(store interface {
	Bucket() string
	PresignGetURL(context.Context, string, string, time.Duration) (string, error)
}) {
	if h != nil {
		h.objectStore = store
	}
}

func (h *LiveTVHandler) HandleStopPlayback(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "live_playback_unavailable", "Live TV playback is unavailable")
		return
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.UserID <= 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	grantID := chi.URLParam(r, "grant_id")
	session, ok := h.playback.Lookup(grantID)
	if !ok || session.UserID != claims.UserID || session.ProfileID != apimw.GetProfileID(r.Context()) || session.SessionID != claims.SessionID {
		writeError(w, http.StatusNotFound, "not_found", "Live TV playback session not found")
		return
	}
	h.playback.RevokeContext(r.Context(), grantID)
	w.WriteHeader(http.StatusNoContent)
}

func NewLiveTVHandler(folders *catalog.FolderRepository, pool *pgxpool.Pool, runtime *livetv.Runtime) *LiveTVHandler {
	if pool == nil {
		return nil
	}
	return &LiveTVHandler{folders: folders, repo: livetv.NewPostgresRepository(pool), runtime: runtime}
}

func MountLiveTVAdminRoutes(r chi.Router, handler *LiveTVHandler) {
	if handler == nil {
		return
	}
	r.Route("/livetv/libraries/{library_id}/sources", func(r chi.Router) {
		r.Get("/", handler.HandleListSources)
		r.Post("/", handler.HandleCreateSource)
		r.Put("/{source_key}", handler.HandleUpdateSource)
		r.Delete("/{source_key}", handler.HandleDeleteSource)
		r.Post("/{source_key}/refresh", handler.HandleRefreshSource)
	})
}

func (h *LiveTVHandler) SetPlaybackService(service *livetv.LivePlaybackService) {
	if h != nil {
		h.playback = service
	}
}

func (h *LiveTVHandler) SetPlaybackOrigin(origin string) {
	if h != nil {
		h.playbackOrigin = strings.TrimRight(strings.TrimSpace(origin), "/")
	}
}

func (h *LiveTVHandler) HandlePlaybackStream(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "live_playback_unavailable", "Live TV playback is unavailable")
		return
	}
	ctx := r.Context()
	if token := r.URL.Query().Get("live_token"); token != "" {
		identity, err := h.playback.MediaTokenIdentity(ctx, chi.URLParam(r, "grant_id"), token)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden", "Live TV playback session is not authorized")
			return
		}
		profileID := apimw.GetProfileID(ctx)
		if profileID == "" {
			profileID = r.Header.Get("X-Profile-Id")
		}
		if profileID != "" && profileID != identity.ProfileID {
			writeError(w, http.StatusForbidden, "forbidden", "Live TV playback session is not authorized")
			return
		}
		ctx = livetv.WithLivePlaybackIdentity(ctx, identity)
	} else {
		claims := apimw.GetClaims(ctx)
		profileID := apimw.GetProfileID(ctx)
		if claims == nil || claims.UserID <= 0 || strings.TrimSpace(profileID) == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		ctx = livetv.WithLivePlaybackIdentity(ctx, livetv.LivePlaybackIdentity{
			UserID: claims.UserID, ProfileID: profileID, SessionID: claims.SessionID,
		})
	}
	request := r.WithContext(ctx)
	request.URL.Path = strings.TrimPrefix(request.URL.Path, "/api/v1")
	h.playback.ServeHTTP(w, request)
}

func (h *LiveTVHandler) HandlePlaybackQualities(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "live_playback_unavailable", "Live TV playback is unavailable")
		return
	}
	ctx, err := livePlaybackRequestContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	state, err := h.playback.QualityState(ctx, chi.URLParam(r, "grant_id"))
	if err != nil {
		writeLiveQualityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, liveTVQualityResponse{Options: state.Options, ActiveID: state.ActiveID, TranscodingSupported: state.TranscodingSupported, UnsupportedReason: state.UnsupportedReason})
}

func (h *LiveTVHandler) HandlePlaybackQuality(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "live_playback_unavailable", "Live TV playback is unavailable")
		return
	}
	ctx, err := livePlaybackRequestContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var request struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Quality ID is required")
		return
	}
	option, err := h.playback.SelectQuality(ctx, chi.URLParam(r, "grant_id"), request.ID)
	if err != nil {
		writeLiveQualityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, liveTVQualityResponse{Options: []livetv.LiveQualityOption{option}, ActiveID: option.ID, TranscodingSupported: false})
}

func writeLiveQualityError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	code := "live_quality_unavailable"
	if errors.Is(err, livetv.ErrLivePlaybackForbidden) {
		status = http.StatusForbidden
		code = "forbidden"
	} else if errors.Is(err, livetv.ErrLivePlaybackNotFound) {
		status = http.StatusNotFound
		code = "not_found"
	} else if errors.Is(err, livetv.ErrLivePlaybackExpired) {
		status = http.StatusGone
		code = "expired"
	}
	writeError(w, status, code, err.Error())
}

func livePlaybackRequestContext(r *http.Request) (context.Context, error) {
	claims := apimw.GetClaims(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if claims == nil || claims.UserID <= 0 || strings.TrimSpace(profileID) == "" {
		return nil, errors.New("missing playback identity")
	}
	return livetv.WithLivePlaybackIdentity(r.Context(), livetv.LivePlaybackIdentity{UserID: claims.UserID, ProfileID: profileID, SessionID: claims.SessionID}), nil
}

func (h *LiveTVHandler) HandleCapability(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, liveTVCapabilityResponse{
		SchemaVersion:     1,
		Enabled:           h != nil && h.repo != nil,
		PlaybackAvailable: h != nil && h.playback != nil && h.playback.ProxyOrigin() != "",
		LibraryTypes:      []string{"livetv"},
		Features: []string{
			"library_sources", "channels", "programme_details", "guide_window", "profile_favorites", "home_sections",
		},
	})
}
