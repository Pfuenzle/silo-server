package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/livetv"
)

func (h *LiveTVHandler) HandleListLibrary(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	sources, err := h.repo.ListSources(r.Context(), library.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV sources")
		return
	}
	response := liveTVLibraryResponse{ID: library.ID, Name: library.Name, Type: library.Type, Enabled: library.Enabled, Sources: make([]liveTVSourceResponse, 0, len(sources))}
	for _, source := range sources {
		response.Sources = append(response.Sources, sourceResponse(source))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *LiveTVHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, false)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	sources, err := h.repo.ListSources(r.Context(), library.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV sources")
		return
	}
	items := make([]liveTVSourceResponse, 0, len(sources))
	for _, source := range sources {
		items = append(items, sourceResponse(source))
	}
	writeJSON(w, http.StatusOK, liveTVSourcesResponse{Items: items, Total: len(items)})
}

func (h *LiveTVHandler) HandleCreateSource(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, false)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	request, ok := decodeSourceRequest(w, r)
	if !ok {
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	source, err := h.repo.CreateSource(r.Context(), livetv.Source{LibraryID: library.ID, Kind: request.Kind, SourceKey: request.SourceKey, Name: request.Name, Location: request.Location, Config: request.Config, Enabled: enabled})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "A Live TV source with this key already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create Live TV source")
		return
	}
	writeJSON(w, http.StatusCreated, sourceResponse(source))
}

func (h *LiveTVHandler) HandleUpdateSource(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, false)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	var request liveTVSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	request.SourceKey = strings.TrimSpace(chi.URLParam(r, "source_key"))
	request.Name = strings.TrimSpace(request.Name)
	request.Location = strings.TrimSpace(request.Location)
	if request.Location == "" {
		current, getErr := h.repo.GetSource(r.Context(), library.ID, request.SourceKey)
		if errors.Is(getErr, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Live TV source not found")
			return
		}
		if getErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV source")
			return
		}
		request.Location = current.Location
	}
	if err := livetv.ValidateSource(request.Kind, request.SourceKey, request.Name, request.Location); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid Live TV source")
		return
	}
	enabled := false
	if request.Enabled != nil {
		enabled = *request.Enabled
	} else {
		current, getErr := h.repo.GetSource(r.Context(), library.ID, request.SourceKey)
		if errors.Is(getErr, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Live TV source not found")
			return
		}
		if getErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV source")
			return
		}
		enabled = current.Enabled
	}
	source, err := h.repo.UpdateSource(r.Context(), livetv.Source{LibraryID: library.ID, Kind: request.Kind, SourceKey: request.SourceKey, Name: request.Name, Location: request.Location, Config: request.Config, Enabled: enabled})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update Live TV source")
		return
	}
	writeJSON(w, http.StatusOK, sourceResponse(source))
}

func (h *LiveTVHandler) HandleDeleteSource(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, false)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	if err := h.repo.DeleteSource(r.Context(), library.ID, chi.URLParam(r, "source_key")); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Live TV source not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete Live TV source")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LiveTVHandler) HandleRefreshSource(w http.ResponseWriter, r *http.Request) {
	library, err := h.liveTVLibrary(r, false)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	source, err := h.repo.GetSource(r.Context(), library.ID, chi.URLParam(r, "source_key"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Live TV source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV source")
		return
	}
	if h.runtime == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Live TV refresh is not configured")
		return
	}
	if _, err := h.runtime.RefreshSource(r.Context(), source, nil); err != nil {
		writeError(w, http.StatusServiceUnavailable, "refresh_failed", "Live TV source refresh failed")
		return
	}
	updated, err := h.repo.GetSource(r.Context(), library.ID, source.SourceKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load refreshed source")
		return
	}
	writeJSON(w, http.StatusOK, sourceResponse(updated))
}

func decodeSourceRequest(w http.ResponseWriter, r *http.Request) (liveTVSourceRequest, bool) {
	var request liveTVSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return liveTVSourceRequest{}, false
	}
	request.SourceKey = strings.TrimSpace(request.SourceKey)
	request.Name = strings.TrimSpace(request.Name)
	request.Location = strings.TrimSpace(request.Location)
	if err := livetv.ValidateSource(request.Kind, request.SourceKey, request.Name, request.Location); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid Live TV source")
		return liveTVSourceRequest{}, false
	}
	return request, true
}

func sourceResponse(source livetv.Source) liveTVSourceResponse {
	refreshError := ""
	if source.RefreshState == "stale" || source.RefreshState == "error" {
		refreshError = "Live TV source refresh failed"
	}
	return liveTVSourceResponse{ID: source.ID, LibraryID: source.LibraryID, Kind: string(source.Kind), SourceKey: source.SourceKey, Name: source.Name, Enabled: source.Enabled, RefreshState: source.RefreshState, RefreshError: refreshError, LastRefreshAt: source.LastRefreshAt}
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint")
}
