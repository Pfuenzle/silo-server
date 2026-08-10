package auth

import (
	"fmt"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestExternalAuthorization_FreshSessionInsertFailureRollsBack(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID, userID := seedExternalAuthorizationIdentity(t, ctx, pool, "session-rollback")
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'admins', 'admin')`, installationID); err != nil {
		t.Fatalf("seed admin mapping: %v", err)
	}
	triggerName := fmt.Sprintf("fail_external_authorization_session_%d", installationID)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'session failure'; END; $$; CREATE TRIGGER %s BEFORE INSERT ON auth_sessions FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, triggerName, triggerName)); err != nil {
		t.Fatalf("create session failure trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON auth_sessions; DROP FUNCTION IF EXISTS %s()`, triggerName, triggerName)); err != nil {
			t.Fatalf("drop session failure trigger: %v", err)
		}
	})
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "session-rollback-subject",
		Claims:          mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}}),
	})

	// When
	_, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: "user", Password: "password"}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour)})

	// Then
	if err == nil {
		t.Fatal("AuthenticateAndComplete() succeeded despite session insert failure")
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); err != nil {
		t.Fatalf("load user after session failure: %v", err)
	}
	if role != "user" {
		t.Fatalf("role after session failure = %q, want user", role)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)
}
