package plugins

import (
	"context"
	"errors"
	"testing"
)

func TestInstallationStoreDelete_returnsDependencyConflict_whenOnlyIdentityLinkAuditExists(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
		VALUES ($1, $1, '', $2, 'ldap', 'trusted_link', 'trusted_existing', repeat('f', 64), 'trusted_existing_match', gen_random_uuid())`, userID, installationID); err != nil {
		t.Fatalf("seed identity link audit: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
			t.Errorf("truncate disposable identity link audit fixture: %v", err)
		}
	})

	// When
	err := NewInstallationStore(pool).Delete(ctx, installationID)

	// Then
	assertSingleAuditDependencyConflict(t, err, "identity link", func(conflict *InstallationDependencyConflictError) int {
		return conflict.IdentityLinkAudits
	})
}

func TestInstallationStoreDelete_returnsDependencyConflict_whenOnlyProviderPolicyAuditExists(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	actorUserID := seedAuthBindingRevocationUser(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id)
		VALUES ($1, $2, 'ldap', 'disabled', 'trusted_existing', false, false, 'trusted_link_enabled', gen_random_uuid())`, actorUserID, installationID); err != nil {
		t.Fatalf("seed provider policy audit: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE auth_provider_policy_audit`); err != nil {
			t.Errorf("truncate disposable provider policy audit fixture: %v", err)
		}
	})

	// When
	err := NewInstallationStore(pool).Delete(ctx, installationID)

	// Then
	assertSingleAuditDependencyConflict(t, err, "provider policy", func(conflict *InstallationDependencyConflictError) int {
		return conflict.ProviderPolicyAudits
	})
}

func assertSingleAuditDependencyConflict(t *testing.T, err error, auditName string, count func(*InstallationDependencyConflictError) int) {
	t.Helper()
	var conflict *InstallationDependencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Delete() error = %v, want typed %s audit dependency conflict before foreign-key deletion", err, auditName)
	}
	if count(conflict) != 1 {
		t.Fatalf("Delete() %s audit count = %d, want 1 in conflict %+v", auditName, count(conflict), conflict)
	}
	if conflict.Identities != 0 || conflict.ProviderSessions != 0 || conflict.GroupMappings != 0 || conflict.AuthorizationAudits != 0 {
		t.Fatalf("Delete() unrelated dependency counts = %+v, want only %s audit", conflict, auditName)
	}
}
