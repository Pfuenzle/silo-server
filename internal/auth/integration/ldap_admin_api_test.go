//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

type ldapAccessGroup struct {
	ID int64 `json:"id"`
}

func (a *adminAPI) configureLDAP(t *testing.T, ctx context.Context, installationID int, value map[string]any) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d/config", installationID), a.token, map[string]any{"key": "ldap", "value": value})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("configure LDAP: status %d: %s", response.StatusCode, responseBody(t, response))
	}
}

func (a *adminAPI) enableLDAP(t *testing.T, ctx context.Context, installationID int) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d/auth-binding", installationID), a.token, map[string]any{"capability_id": "ldap", "enabled": true, "auto_provision": true, "authorization_mode": "external_groups_v1"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Silo-Restart-Required") != "true" {
		t.Fatalf("enable LDAP binding: status %d restart=%q", response.StatusCode, response.Header.Get("X-Silo-Restart-Required"))
	}
}

func (a *adminAPI) replaceLDAPMappings(t *testing.T, ctx context.Context, installationID int, mappings []map[string]any) {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/plugins/installations/%d/auth-group-mappings", installationID), a.token, map[string]any{"capability_id": "ldap", "mappings": mappings})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replace LDAP mappings: status %d: %s", response.StatusCode, responseBody(t, response))
	}
}

func (a *adminAPI) createLDAPAccessGroup(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	response := a.requestJSON(t, ctx, http.MethodPost, "/api/v1/admin/access-groups", a.token, map[string]any{"name": "LDAP integration access", "description": "packaged LDAP E2E", "is_default": false})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create LDAP access group: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	var group ldapAccessGroup
	decodeJSON(t, response.Body, &group)
	if group.ID == 0 {
		t.Fatal("LDAP access group response omitted id")
	}
	return group.ID
}
