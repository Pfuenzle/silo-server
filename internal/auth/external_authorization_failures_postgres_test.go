package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExternalAuthorization_RejectsMalformedEnvelope(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID, userID := seedExternalAuthorizationIdentity(t, ctx, pool, "malformed")
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "malformed-subject",
		Claims:          mustExternalGroupsClaims(t, map[string]any{"unexpected": true}),
	})

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)

	// Given
	oauthProvider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: "ldap", AuthMode: "oauth"}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{}, nil
	})

	// When
	_, err = oauthProvider.CompleteOAuth(ctx, &pluginv1.AuthenticateResponse{
		ExternalSubject: "malformed-subject",
		Claims:          mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}}),
	})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("CompleteOAuth() error = %v, want ErrInvalidCredentials", err)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); err != nil || role != "user" {
		t.Fatalf("hostile OAuth claims changed user role=%q err=%v", role, err)
	}
}

func TestExternalAuthorization_RejectsConflictingMappings(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID, userID := seedExternalAuthorizationIdentity(t, ctx, pool, "conflict")
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'admins', 'admin'), ($1, 'users', 'user')`, installationID); err != nil {
		t.Fatalf("seed conflicting mappings: %v", err)
	}
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "conflict-subject",
		Claims: mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{
			map[string]any{"id": "admins"}, map[string]any{"id": "users"},
		}}),
	})

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})

	// Then
	if !errors.Is(err, ErrExternalAuthorizationConflict) {
		t.Fatalf("Authenticate() error = %v, want ErrExternalAuthorizationConflict", err)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)
}

func TestExternalAuthorization_RejectsIndependentRoleAndGroupTargets(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID, userID := seedExternalAuthorizationIdentity(t, ctx, pool, "independent-conflict")
	var accessGroupID int64
	if err := pool.QueryRow(ctx, `INSERT INTO access_groups (name) VALUES ($1) RETURNING id`, fmt.Sprintf("external-authorization-%d", installationID)).Scan(&accessGroupID); err != nil {
		t.Fatalf("seed target access group: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1`, installationID); err != nil {
			t.Fatalf("cleanup mappings for installation %d: %v", installationID, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM access_groups WHERE id = $1`, accessGroupID); err != nil {
			t.Fatalf("cleanup target access group: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role, access_group_id) VALUES ($1, 'admins', 'admin', NULL), ($1, 'readers', NULL, $2)`, installationID, accessGroupID); err != nil {
		t.Fatalf("seed independent target mappings: %v", err)
	}
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "independent-conflict-subject",
		Claims: mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{
			map[string]any{"id": "admins"}, map[string]any{"id": "readers"},
		}}),
	})

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})

	// Then
	if !errors.Is(err, ErrExternalAuthorizationConflict) {
		t.Fatalf("Authenticate() error = %v, want ErrExternalAuthorizationConflict", err)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)
}

func TestExternalAuthorization_AuditFailureRollsBack(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID, userID := seedExternalAuthorizationIdentity(t, ctx, pool, "audit-rollback")
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'admins', 'admin')`, installationID); err != nil {
		t.Fatalf("seed admin mapping: %v", err)
	}
	name := fmt.Sprintf("fail_external_authorization_audit_%d", installationID)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit failure'; END; $$; CREATE TRIGGER %s BEFORE INSERT ON external_authorization_audit FOR EACH ROW EXECUTE FUNCTION %s()`, name, name, name)); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON external_authorization_audit; DROP FUNCTION IF EXISTS %s()`, name, name)); err != nil {
			t.Fatalf("drop audit failure trigger: %v", err)
		}
	})
	provider := externalAuthorizationTestProvider(t, pool, installationID, &pluginv1.AuthenticateResponse{
		ExternalSubject: "audit-rollback-subject",
		Claims:          mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}}),
	})

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})

	// Then
	if err == nil {
		t.Fatal("Authenticate() succeeded despite audit failure")
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); err != nil {
		t.Fatalf("load user after audit failure: %v", err)
	}
	if role != "user" {
		t.Fatalf("role after audit failure = %q, want user", role)
	}
	assertExternalAuthorizationAuditCount(t, ctx, pool, userID, 0)
}

func TestExternalAuthorization_AuditFailureRollsBackFirstProvision(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'ldap', true, true, 'external_groups_v1')`, installationID); err != nil {
		t.Fatalf("seed authoritative binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'admins', 'admin')`, installationID); err != nil {
		t.Fatalf("seed admin mapping: %v", err)
	}
	triggerName := fmt.Sprintf("fail_first_provision_audit_%d", installationID)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit failure'; END; $$; CREATE TRIGGER %s BEFORE INSERT ON external_authorization_audit FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, triggerName, triggerName)); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON external_authorization_audit; DROP FUNCTION IF EXISTS %s()`, triggerName, triggerName)); err != nil {
			t.Fatalf("drop audit failure trigger: %v", err)
		}
	})
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{
		InstallationID: installationID,
		CapabilityID:   "ldap",
		AuthMode:       "credentials",
		AutoProvision:  true,
		StoreProvider:  pgstore.NewPostgresProvider(pool),
	}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
			ExternalSubject: "first-provision-audit-subject",
			DisplayName:     "First Provision Audit",
			Email:           "first-provision-audit@example.invalid",
			Claims:          mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}}}),
		}}, nil
	})

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "user", Password: "password"})

	// Then
	if err == nil {
		t.Fatal("Authenticate() succeeded despite audit failure")
	}
	var users, identities, profiles, audits int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE email = 'first-provision-audit@example.invalid'),
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'first-provision-audit-subject'),
		(SELECT COUNT(*) FROM user_profiles WHERE name = 'first_provision_audit'),
		(SELECT COUNT(*) FROM external_authorization_audit WHERE plugin_installation_id = $1)`, installationID,
	).Scan(&users, &identities, &profiles, &audits); err != nil {
		t.Fatalf("count first-provision rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 || audits != 0 {
		t.Fatalf("first-provision residue = users:%d identities:%d profiles:%d audits:%d, want 0:0:0:0", users, identities, profiles, audits)
	}
}

func seedExternalAuthorizationIdentity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) (int, int) {
	t.Helper()
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, label)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, 'ldap', true, 'external_groups_v1')`, installationID); err != nil {
		t.Fatalf("seed authoritative binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, label+"-subject", userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return installationID, userID
}

func externalAuthorizationTestProvider(t *testing.T, pool *pgxpool.Pool, installationID int, response *pluginv1.AuthenticateResponse) *PluginProvider {
	t.Helper()
	return NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: "ldap", AuthMode: "credentials"}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: response}, nil
	})
}

func assertExternalAuthorizationAuditCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_authorization_audit WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != want {
		t.Fatalf("audit rows = %d, want %d", count, want)
	}
}
