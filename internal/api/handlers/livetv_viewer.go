package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/livetv"
	"github.com/Silo-Server/silo-server/internal/models"
)

func (h *LiveTVHandler) HandleListChannels(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	limit, offset := parseLiveTVPage(r)
	channels, total, err := h.repo.ListChannels(r.Context(), library.ID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV channels")
		return
	}
	items := make([]liveTVChannelResponse, 0, len(channels))
	stale := h.libraryStale(r, library.ID)
	for _, channel := range channels {
		items = append(items, liveTVChannelResponse{ID: channel.StableID.String(), Name: channel.Name, Number: channel.Number, Category: string(channel.Category), Artwork: h.authorizeArtwork(r.Context(), channel.Artwork), Rating: channel.Rating, Stale: stale})
	}
	writeJSON(w, http.StatusOK, liveTVPage[liveTVChannelResponse]{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *LiveTVHandler) HandleChannelDetail(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	channel, err := h.repo.GetChannel(r.Context(), library.ID, livetv.SourceQualifiedID(chi.URLParam(r, "channel_id")))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV channel")
		return
	}
	writeJSON(w, http.StatusOK, liveTVChannelResponse{ID: channel.StableID.String(), Name: channel.Name, Number: channel.Number, Category: string(channel.Category), Artwork: h.authorizeArtwork(r.Context(), channel.Artwork), Rating: channel.Rating, Stale: h.libraryStale(r, library.ID)})
}

func (h *LiveTVHandler) HandleGuide(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	from, to, err := parseGuideWindow(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to must be RFC3339 timestamps with a maximum 24 hour window")
		return
	}
	channels, _, err := h.repo.ListChannels(r.Context(), library.ID, liveTVMaxPageSize, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV channels")
		return
	}
	items := make([]liveTVProgrammeResponse, 0)
	refreshError := h.libraryRefreshError(r, library.ID)
	for _, channel := range channels {
		programmes, listErr := h.repo.ListProgrammes(r.Context(), library.ID, channel.ID, from, to)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV programmes")
			return
		}
		for _, programme := range programmes {
			items = append(items, h.programmeResponse(r.Context(), programme, &channel))
		}
	}
	response := liveTVGuideResponse{LibraryID: library.ID, From: from, To: to, Stale: h.libraryStale(r, library.ID), Items: items}
	if refreshError != "" {
		response.RefreshError = refreshError
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *LiveTVHandler) HandleProgrammeDetail(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	programme, err := h.repo.GetProgramme(r.Context(), library.ID, livetv.SourceQualifiedID(chi.URLParam(r, "programme_id")))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV programme not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV programme")
		return
	}
	writeJSON(w, http.StatusOK, h.programmeResponse(r.Context(), programme, nil))
}

func (h *LiveTVHandler) HandleCurrentNext(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	now := time.Now().UTC()
	channelID := chi.URLParam(r, "channel_id")
	channel, err := h.repo.GetChannel(r.Context(), library.ID, livetv.SourceQualifiedID(channelID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV channel")
		return
	}
	guide := liveTVGuideResponse{LibraryID: library.ID, From: now, To: now.Add(24 * time.Hour), Stale: h.libraryStale(r, library.ID), Items: make([]liveTVProgrammeResponse, 0, 2)}
	programmes, err := h.repo.ListProgrammes(r.Context(), library.ID, channel.ID, now, now.Add(24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV programmes")
		return
	}
	for index, programme := range programmes {
		if index == 2 {
			break
		}
		guide.Items = append(guide.Items, h.programmeResponse(r.Context(), programme, &channel))
	}
	writeJSON(w, http.StatusOK, guide)
}

func (h *LiveTVHandler) HandlePlaybackResolution(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	channelID := chi.URLParam(r, "channel_id")
	stableID := livetv.SourceQualifiedID(channelID)
	if _, err := h.repo.GetChannel(r.Context(), library.ID, stableID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV channel not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve Live TV channel")
		return
	}
	if h.playback == nil || h.playbackOrigin == "" {
		writeJSON(w, http.StatusServiceUnavailable, liveTVPlaybackResponse{ChannelID: channelID, Live: true, Playable: false, ErrorCode: "live_playback_unavailable"})
		return
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.UserID <= 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	grant, err := h.playback.Start(livetv.WithLivePlaybackIdentity(r.Context(), livetv.LivePlaybackIdentity{UserID: claims.UserID, ProfileID: apimw.GetProfileID(r.Context()), SessionID: claims.SessionID}), livetv.LivePlaybackRequest{
		UserID: claims.UserID, ProfileID: apimw.GetProfileID(r.Context()), LibraryID: library.ID,
		SessionID: claims.SessionID, Mode: livetv.LivePlaybackModeHLS, ChannelID: stableID,
	})
	if err != nil {
		if errors.Is(err, livetv.ErrLivePlaybackUnavailable) || errors.Is(err, livetv.ErrSourcePolicy) {
			writeJSON(w, http.StatusServiceUnavailable, liveTVPlaybackResponse{ChannelID: channelID, Live: true, Playable: false, ErrorCode: "live_playback_unavailable"})
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve Live TV playback")
		return
	}
	grant.ManifestURL = h.playbackOrigin + "/stream/live/" + grant.GrantID + "/manifest"
	writeJSON(w, http.StatusOK, liveTVPlaybackResponse{ChannelID: channelID, Live: true, Playable: true, GrantID: grant.GrantID, URL: grant.ManifestURL})
}

func (h *LiveTVHandler) liveTVLibrary(r *http.Request, viewer bool) (*models.MediaFolder, error) {
	id, err := strconv.Atoi(chi.URLParam(r, "library_id"))
	if err != nil || id <= 0 {
		return nil, &liveTVHTTPError{status: http.StatusBadRequest, code: "bad_request", message: "Invalid Live TV library ID"}
	}
	if h.folders == nil {
		return nil, &liveTVHTTPError{status: http.StatusServiceUnavailable, code: "unavailable", message: "Library repository is not configured"}
	}
	library, err := h.folders.GetByID(r.Context(), id)
	if errors.Is(err, catalog.ErrFolderNotFound) {
		return nil, &liveTVHTTPError{status: http.StatusNotFound, code: "not_found", message: "Live TV library not found"}
	}
	if err != nil {
		return nil, &liveTVHTTPError{status: http.StatusInternalServerError, code: "internal_error", message: "Failed to load Live TV library"}
	}
	if !liveTVLibraryVisible(library, viewer, libraryAccessible(r, library.ID)) {
		return nil, &liveTVHTTPError{status: http.StatusNotFound, code: "not_found", message: "Live TV library not found"}
	}
	return library, nil
}

func liveTVLibraryVisible(library *models.MediaFolder, viewer, accessible bool) bool {
	if library == nil || !librarykind.IsLiveTV(library.Type) {
		return false
	}
	return !viewer || (library.Enabled && accessible)
}

func libraryAccessible(r *http.Request, libraryID int) bool {
	scope, ok := access.GetScope(r.Context())
	if !ok || !scope.LibrariesRestricted {
		return true
	}
	for _, allowed := range scope.AllowedLibraryIDs {
		if allowed == libraryID {
			return true
		}
	}
	return false
}

func (h *LiveTVHandler) libraryStale(r *http.Request, libraryID int) bool {
	sources, err := h.repo.ListSources(r.Context(), libraryID)
	if err != nil {
		return false
	}
	for _, source := range sources {
		if source.RefreshState == "stale" || source.RefreshState == "error" {
			return true
		}
	}
	return false
}

func (h *LiveTVHandler) libraryRefreshError(r *http.Request, libraryID int) string {
	sources, err := h.repo.ListSources(r.Context(), libraryID)
	if err != nil || len(sources) == 0 {
		return ""
	}
	for _, source := range sources {
		if source.RefreshState == "stale" || source.RefreshState == "error" {
			return "Live TV source refresh failed"
		}
	}
	return ""
}

func (h *LiveTVHandler) programmeResponse(ctx context.Context, programme livetv.Programme, channel *livetv.Channel) liveTVProgrammeResponse {
	response := liveTVProgrammeResponse{ID: programme.StableID.String(), Title: programme.Title, Description: programme.Description, StartsAt: programme.StartsAt, EndsAt: programme.EndsAt, Artwork: h.authorizeArtwork(ctx, programme.Artwork), Rating: programme.Rating}
	if channel != nil {
		response.ChannelID = channel.StableID.String()
		response.ChannelName = channel.Name
	}
	return response
}

func (h *LiveTVHandler) authorizeArtwork(ctx context.Context, raw []byte) json.RawMessage {
	var value map[string]string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || h == nil || (h.artwork == nil && h.objectStore == nil) {
		return nil
	}
	for key, path := range value {
		resolved := ""
		fromObjectStore := h.objectStore != nil && !strings.Contains(path, "://") && !strings.HasPrefix(path, "/")
		if fromObjectStore {
			resolved, _ = h.objectStore.PresignGetURL(ctx, h.objectStore.Bucket(), path, 15*time.Minute)
		} else if h.artwork != nil {
			resolved = h.artwork.PresignURL(ctx, path, "card")
		}
		if resolved == "" || (!fromObjectStore && !isAuthorizedArtworkURL(resolved)) {
			delete(value, key)
			continue
		}
		value[key] = resolved
	}
	if len(value) == 0 {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func isAuthorizedArtworkURL(raw string) bool {
	if strings.HasPrefix(raw, "/api/") {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	query := parsed.Query()
	return query.Get("X-Amz-Signature") != "" || query.Get("X-Goog-Signature") != "" || query.Get("verify") != ""
}

func parseLiveTVPage(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = liveTVDefaultPageSize
	}
	if limit > liveTVMaxPageSize {
		limit = liveTVMaxPageSize
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func parseGuideWindow(r *http.Request) (time.Time, time.Time, error) {
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || !to.After(from) || to.Sub(from) > 24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("invalid guide window")
	}
	return from.UTC(), to.UTC(), nil
}

type liveTVHTTPError struct {
	status  int
	code    string
	message string
}

func (e *liveTVHTTPError) Error() string { return e.code + ": " + e.message }

func writeLiveTVError(w http.ResponseWriter, err error) {
	var liveErr *liveTVHTTPError
	if errors.As(err, &liveErr) {
		writeError(w, liveErr.status, liveErr.code, liveErr.message)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Live TV request failed")
}
