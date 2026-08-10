package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type pluginAuthGroupMappingRequest struct {
	ExternalGroupID string  `json:"external_group_id"`
	TargetRole      *string `json:"target_role,omitempty"`
	AccessGroupID   *int64  `json:"access_group_id,omitempty"`
}

type pluginAuthGroupMappingBatchRequest struct {
	CapabilityID string                          `json:"capability_id"`
	Mappings     []pluginAuthGroupMappingRequest `json:"mappings"`
}

type pluginAuthGroupMappingPreviewRequest struct {
	CapabilityID     string   `json:"capability_id"`
	ExternalGroupIDs []string `json:"external_group_ids"`
}

type pluginAuthGroupMappingResponse struct {
	ExternalGroupID string    `json:"external_group_id"`
	TargetRole      *string   `json:"target_role,omitempty"`
	AccessGroupID   *int64    `json:"access_group_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type pluginAuthGroupMappingPreviewResponse struct {
	ExternalGroupID string `json:"external_group_id"`
	TargetRole      string `json:"target_role"`
	AccessGroupID   *int64 `json:"access_group_id,omitempty"`
	Matched         bool   `json:"matched"`
}

func (h *PluginHandler) HandleListAuthGroupMappings(w http.ResponseWriter, r *http.Request) {
	installationID, ok := h.authGroupMappingInstallationID(w, r)
	if !ok {
		return
	}
	if !h.requireExternalGroupsAuthorizationMode(w, r, installationID, r.URL.Query().Get("capability_id")) {
		return
	}
	mappings, err := h.configs.AuthGroupMappings().List(r.Context(), installationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list auth group mappings")
		return
	}
	writeJSON(w, http.StatusOK, toPluginAuthGroupMappingResponses(mappings))
}

func (h *PluginHandler) HandlePutAuthGroupMappings(w http.ResponseWriter, r *http.Request) {
	installationID, ok := h.authGroupMappingInstallationID(w, r)
	if !ok {
		return
	}
	var request pluginAuthGroupMappingBatchRequest
	if !decodeSingleJSON(w, r, &request) {
		return
	}
	if !h.requireExternalGroupsAuthorizationMode(w, r, installationID, request.CapabilityID) {
		return
	}
	mappings, err := h.configs.AuthGroupMappings().Replace(r.Context(), installationID, request.toInputs())
	if err != nil {
		writeAuthGroupMappingError(w, err, "Failed to save auth group mappings")
		return
	}
	writeJSON(w, http.StatusOK, toPluginAuthGroupMappingResponses(mappings))
}

func (h *PluginHandler) HandlePreviewAuthGroupMappings(w http.ResponseWriter, r *http.Request) {
	installationID, ok := h.authGroupMappingInstallationID(w, r)
	if !ok {
		return
	}
	var request pluginAuthGroupMappingPreviewRequest
	if !decodeSingleJSON(w, r, &request) {
		return
	}
	if !h.requireExternalGroupsAuthorizationMode(w, r, installationID, request.CapabilityID) {
		return
	}
	externalGroupIDs, err := plugins.NormalizeAuthGroupIDs(request.ExternalGroupIDs)
	if err != nil {
		writeAuthGroupMappingError(w, err, "Invalid external group IDs")
		return
	}
	mappings, err := h.configs.AuthGroupMappings().List(r.Context(), installationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to preview auth group mappings")
		return
	}
	writeJSON(w, http.StatusOK, previewAuthGroupMappings(externalGroupIDs, mappings))
}

func (r pluginAuthGroupMappingBatchRequest) toInputs() []plugins.AuthGroupMappingInput {
	inputs := make([]plugins.AuthGroupMappingInput, 0, len(r.Mappings))
	for _, mapping := range r.Mappings {
		inputs = append(inputs, plugins.AuthGroupMappingInput{
			ExternalGroupID: mapping.ExternalGroupID,
			TargetRole:      mapping.TargetRole,
			AccessGroupID:   mapping.AccessGroupID,
		})
	}
	return inputs
}

func (h *PluginHandler) authGroupMappingInstallationID(w http.ResponseWriter, r *http.Request) (int, bool) {
	installationID, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return 0, false
	}
	if h.configs == nil || h.installations == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Plugin configuration is not available")
		return 0, false
	}
	if h.rejectBuiltinInstallation(w, r, installationID) {
		return 0, false
	}
	return installationID, true
}

func (h *PluginHandler) requireExternalGroupsAuthorizationMode(
	w http.ResponseWriter,
	r *http.Request,
	installationID int,
	capabilityID string,
) bool {
	if strings.TrimSpace(capabilityID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "capability_id is required")
		return false
	}
	binding, err := h.configs.GetAuthBinding(r.Context(), installationID, capabilityID)
	switch {
	case errors.Is(err, plugins.ErrAuthBindingNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Plugin auth binding not found")
		return false
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin auth binding")
		return false
	case binding.EffectiveAuthorizationMode() != plugins.AuthBindingAuthorizationModeExternalGroupsV1:
		writeError(w, http.StatusBadRequest, "unsupported_authorization_mode", "Auth binding must opt in to external_groups_v1")
		return false
	default:
		return true
	}
}

func decodeSingleJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Request body must contain one JSON object")
		return false
	}
	return true
}

func writeAuthGroupMappingError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, plugins.ErrAuthGroupMappingConflict):
		writeError(w, http.StatusConflict, "conflict", "External group IDs must be unique")
	case errors.Is(err, plugins.ErrAuthGroupMappingInvalid), errors.Is(err, plugins.ErrAuthGroupMappingAccessGroupNotFound):
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid auth group mapping")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func toPluginAuthGroupMappingResponses(mappings []plugins.AuthGroupMapping) []pluginAuthGroupMappingResponse {
	responses := make([]pluginAuthGroupMappingResponse, 0, len(mappings))
	for _, mapping := range mappings {
		responses = append(responses, pluginAuthGroupMappingResponse{
			ExternalGroupID: mapping.ExternalGroupID,
			TargetRole:      mapping.TargetRole,
			AccessGroupID:   mapping.AccessGroupID,
			CreatedAt:       mapping.CreatedAt,
			UpdatedAt:       mapping.UpdatedAt,
		})
	}
	return responses
}

func previewAuthGroupMappings(externalGroupIDs []string, mappings []plugins.AuthGroupMapping) []pluginAuthGroupMappingPreviewResponse {
	byExternalID := make(map[string]plugins.AuthGroupMapping, len(mappings))
	for _, mapping := range mappings {
		byExternalID[mapping.ExternalGroupID] = mapping
	}
	responses := make([]pluginAuthGroupMappingPreviewResponse, 0, len(externalGroupIDs))
	for _, externalGroupID := range externalGroupIDs {
		mapping, matched := byExternalID[externalGroupID]
		role := "user"
		if matched && mapping.TargetRole != nil {
			role = *mapping.TargetRole
		}
		responses = append(responses, pluginAuthGroupMappingPreviewResponse{
			ExternalGroupID: externalGroupID,
			TargetRole:      role,
			AccessGroupID:   mapping.AccessGroupID,
			Matched:         matched,
		})
	}
	return responses
}
