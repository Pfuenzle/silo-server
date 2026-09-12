package handlers

import (
	"context"
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
