package database

import (
	"context"
	"testing"
	"time"
)

func TestTrustedExternalAccountLinkingMigration_rollsBackAllArtifactsWhenLatePolicyTableConflicts(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool := newTrustedLinkMigrationPool(t, ctx)
	if err := RunMigrations(ctx, pool, migrationFSThrough(t, trustedLinkPreMigrationVersion), "sql"); err != nil {
		t.Fatalf("migrate pre-feature schema: %v", err)
	}
	seed := seedTrustedLinkPreFeatureRows(t, ctx, pool)
	if _, err := pool.Exec(ctx, `CREATE TABLE auth_provider_policy_audit (id BIGSERIAL PRIMARY KEY)`); err != nil {
		t.Fatalf("create late migration conflict: %v", err)
	}

	// When
	err := RunMigrations(ctx, pool, migrationFSThrough(t, trustedLinkMigrationVersion), "sql")

	// Then
	if err == nil {
		t.Fatal("migration succeeded despite the late policy-table conflict")
	}
	var targetColumns, targetConstraints, targetIndexes, targetTables, targetFunctions, targetTriggers, targetForeignKeys, version int
	var bindings, identities, audits, defaults, disabledDefaults, identityOwner, auditOwner, preexistingPolicyColumns int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND ((table_name = 'plugin_auth_bindings' AND column_name = 'trusted_link_mode') OR (table_name = 'external_authorization_audit' AND column_name IN ('original_user_id', 'original_profile_id', 'canonicalization_audit_id')) OR (table_name = 'auth_provider_policy_audit' AND column_name <> 'id'))),
			(SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema = 'public' AND constraint_name IN ('plugin_auth_bindings_trusted_link_mode_check', 'plugin_auth_bindings_default_login_enabled_check', 'external_authorization_audit_original_user_id_bounds', 'external_authorization_audit_canonicalization_audit_id_fkey')),
			(SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname IN ('idx_plugin_auth_bindings_one_default_login', 'idx_external_identity_link_audit_user_created', 'idx_external_authorization_audit_canonicalization_audit', 'idx_external_identity_link_audit_one_canonicalization_per_source', 'idx_auth_provider_policy_audit_installation_created', 'idx_auth_provider_policy_audit_actor_created')),
			(SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'external_identity_link_audit'),
			(SELECT COUNT(*) FROM pg_proc WHERE proname IN ('preserve_external_authorization_audit_source', 'rehome_external_authorization_audit', 'reject_external_identity_link_audit_mutation')),
			(SELECT COUNT(*) FROM pg_trigger WHERE tgname IN ('external_authorization_audit_preserve_source', 'external_authorization_audit_rehome', 'external_identity_link_audit_immutable') AND NOT tgisinternal),
			(SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema = 'public' AND constraint_name = 'external_authorization_audit_canonicalization_audit_id_fkey'),
			(SELECT COUNT(*) FROM public.goose_db_version WHERE version_id = $1 AND is_applied),
			(SELECT COUNT(*) FROM plugin_auth_bindings),
			(SELECT COUNT(*) FROM plugin_auth_identities),
			(SELECT COUNT(*) FROM external_authorization_audit),
			(SELECT COUNT(*) FROM plugin_auth_bindings WHERE default_login),
			(SELECT COUNT(*) FROM plugin_auth_bindings WHERE NOT enabled AND default_login),
			(SELECT user_id FROM plugin_auth_identities WHERE id = $2),
			(SELECT user_id FROM external_authorization_audit WHERE id = $3),
			(SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auth_provider_policy_audit')`, trustedLinkMigrationVersion, seed.identityID, seed.authorizationAuditID).
		Scan(&targetColumns, &targetConstraints, &targetIndexes, &targetTables, &targetFunctions, &targetTriggers, &targetForeignKeys, &version, &bindings, &identities, &audits, &defaults, &disabledDefaults, &identityOwner, &auditOwner, &preexistingPolicyColumns); err != nil {
		t.Fatalf("inspect late rollback state: %v", err)
	}
	if targetColumns != 0 || targetConstraints != 0 || targetIndexes != 0 || targetTables != 0 || targetFunctions != 0 || targetTriggers != 0 || targetForeignKeys != 0 || version != 0 || bindings != 3 || identities != 1 || audits != 1 || defaults != 3 || disabledDefaults != 1 || identityOwner != seed.userID || auditOwner != seed.userID || preexistingPolicyColumns != 1 {
		t.Fatalf("late rollback = columns:%d constraints:%d indexes:%d tables:%d functions:%d triggers:%d foreign_keys:%d version:%d bindings:%d identities:%d audits:%d defaults:%d disabled_defaults:%d owners:%d/%d preexisting_policy_columns:%d", targetColumns, targetConstraints, targetIndexes, targetTables, targetFunctions, targetTriggers, targetForeignKeys, version, bindings, identities, audits, defaults, disabledDefaults, identityOwner, auditOwner, preexistingPolicyColumns)
	}
	t.Log("late rollback receipt: target_columns=0 constraints=0 indexes=0 tables=0 functions=0 triggers=0 fks=0 goose_version=0 seed_rows_unchanged=true")
}
