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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

func seedAuthGroupMappingHandlerInstallation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var installationID int
	pluginID := fmt.Sprintf("handler-auth-group-mapping-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '0', '/nonexistent/auth-group-mapping-handler-test')
		RETURNING id`, pluginID).Scan(&installationID)
	if err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		tag, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID)
		if err != nil || tag.RowsAffected() != 1 {
			t.Errorf("cleanup installation %d: rows=%d err=%v", installationID, tag.RowsAffected(), err)
		}
		var mappings, installations int
		if err := pool.QueryRow(context.Background(), `SELECT (SELECT COUNT(*) FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1), (SELECT COUNT(*) FROM plugin_installations WHERE id = $1)`, installationID).Scan(&mappings, &installations); err != nil {
			t.Errorf("query installation %d residue: %v", installationID, err)
		} else if mappings != 0 || installations != 0 {
			t.Errorf("installation %d residue: mappings=%d installations=%d", installationID, mappings, installations)
		}
	})
	return installationID
}

func TestAuthGroupMapping_AdminHandlerPersistsExactMappings(t *testing.T) {
	// Given
	pool := pluginBuiltinTestPool(t)
	installationID := seedAuthGroupMappingHandlerInstallation(t, pool)
	handler := builtinTestHandler(pool)
	seedExternalGroupBinding(t, pool, handler, installationID)
	validBody := `{"capability_id":"ldap","mappings":[{"external_group_id":"directory-team-a","target_role":"admin"}]}`

	// When
	putRecorder := httptest.NewRecorder()
	handler.HandlePutAuthGroupMappings(putRecorder, requestWithIDParamBody(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-group-mappings", "id", installationID, strings.NewReader(validBody)))

	// Then
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", putRecorder.Code, putRecorder.Body.String())
	}
	var stored []pluginAuthGroupMappingResponse
	if err := json.Unmarshal(putRecorder.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if len(stored) != 1 || stored[0].ExternalGroupID != "directory-team-a" || stored[0].TargetRole == nil || *stored[0].TargetRole != "admin" {
		t.Fatalf("stored mappings = %#v", stored)
	}
}

func TestAuthGroupMapping_AdminHandlerPreviewDoesNotMutateUsers(t *testing.T) {
	// Given
	pool := pluginBuiltinTestPool(t)
	installationID := seedAuthGroupMappingHandlerInstallation(t, pool)
	handler := builtinTestHandler(pool)
	seedExternalGroupBinding(t, pool, handler, installationID)
	admin := "admin"
	if _, err := handler.configs.AuthGroupMappings().Replace(context.Background(), installationID, []plugins.AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-a",
		TargetRole:      &admin,
	}}); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	var usersBefore int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&usersBefore); err != nil {
		t.Fatalf("count users before preview: %v", err)
	}

	// When
	previewRecorder := httptest.NewRecorder()
	handler.HandlePreviewAuthGroupMappings(previewRecorder, requestWithIDParamBody(http.MethodPost, "/api/v1/admin/plugins/installations/0/auth-group-mappings/preview", "id", installationID, strings.NewReader(`{"capability_id":"ldap","external_group_ids":["directory-team-a","directory-team-b"]}`)))

	// Then
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body=%s", previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview []pluginAuthGroupMappingPreviewResponse
	if err := json.Unmarshal(previewRecorder.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(preview) != 2 || !preview[0].Matched || preview[0].TargetRole != "admin" || preview[1].Matched || preview[1].TargetRole != "user" {
		t.Fatalf("preview = %#v", preview)
	}
	var usersAfter int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&usersAfter); err != nil {
		t.Fatalf("count users after preview: %v", err)
	}
	if usersAfter != usersBefore {
		t.Fatalf("preview changed user count from %d to %d", usersBefore, usersAfter)
	}
}

func TestAuthGroupMapping_AdminHandlerInvalidBatchLeavesStoredMappingsUntouched(t *testing.T) {
	// Given
	pool := pluginBuiltinTestPool(t)
	installationID := seedAuthGroupMappingHandlerInstallation(t, pool)
	handler := builtinTestHandler(pool)
	seedExternalGroupBinding(t, pool, handler, installationID)
	user := "user"
	if _, err := handler.configs.AuthGroupMappings().Replace(context.Background(), installationID, []plugins.AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-a",
		TargetRole:      &user,
	}}); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	// When
	invalidRecorder := httptest.NewRecorder()
	handler.HandlePutAuthGroupMappings(invalidRecorder, requestWithIDParamBody(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-group-mappings", "id", installationID, strings.NewReader(`{"capability_id":"ldap","mappings":[{"external_group_id":"directory-team-b","target_role":"user"},{"external_group_id":"*","target_role":"admin"}]}`)))

	// Then
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid put status = %d, body=%s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
	listRecorder := httptest.NewRecorder()
	listRequest := requestWithIDParam(http.MethodGet, "/api/v1/admin/plugins/installations/0/auth-group-mappings?capability_id=ldap", "id", installationID)
	handler.HandleListAuthGroupMappings(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var afterFailure []pluginAuthGroupMappingResponse
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &afterFailure); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(afterFailure) != 1 || afterFailure[0].ExternalGroupID != "directory-team-a" {
		t.Fatalf("invalid write changed persisted mappings: %#v", afterFailure)
	}
}

func TestAuthBindingAuthorizationMode_HandlerRejectsUnsupportedMode(t *testing.T) {
	// Given
	pool := pluginBuiltinTestPool(t)
	installationID := seedAuthGroupMappingHandlerInstallation(t, pool)
	actorID := seedBuiltinTestUser(t, pool, "unsupported-mode")
	handler := builtinTestHandler(pool)

	// When
	unsupportedBindingRecorder := httptest.NewRecorder()
	handler.HandlePutAuthBinding(unsupportedBindingRecorder, requestWithIDParamBodyAdmin(http.MethodPut, "/api/v1/admin/plugins/installations/0/auth-binding", "id", installationID, strings.NewReader(`{"capability_id":"ldap","authorization_mode":"external_groups_v2"}`), actorID))

	// Then
	if unsupportedBindingRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported mode status = %d, body=%s", unsupportedBindingRecorder.Code, unsupportedBindingRecorder.Body.String())
	}
}

func seedExternalGroupBinding(t *testing.T, pool *pgxpool.Pool, handler *PluginHandler, installationID int) {
	t.Helper()
	actorID := seedBuiltinTestUser(t, pool, "ext-group-actor")
	if err := handler.configs.UpsertAuthBinding(context.Background(), actorID, plugins.AuthBinding{
		InstallationID:    installationID,
		CapabilityID:      "ldap",
		AuthorizationMode: plugins.AuthBindingAuthorizationModeExternalGroupsV1,
	}); err != nil {
		t.Fatalf("seed auth binding: %v", err)
	}
}
