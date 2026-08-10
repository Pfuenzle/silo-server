package database

import (
	"context"
	"testing"
)

func TestTrustedExternalAccountLinkingMigration_createsBoundAuditSchema(t *testing.T) {
	// Given
	ctx := context.Background()
	pool := newTrustedLinkMigrationPool(t, ctx)
	if err := RunMigrations(ctx, pool, migrationFSThrough(t, trustedLinkPreMigrationVersion), "sql"); err != nil {
		t.Fatalf("migrate pre-feature schema: %v", err)
	}

	// When
	if err := RunMigrations(ctx, pool, migrationFSThrough(t, trustedLinkMigrationVersion), "sql"); err != nil {
		t.Fatalf("migrate trusted external account linking: %v", err)
	}

	// Then
	var nullableReference, restrictedReferences, policyColumns, indexes, authorizationReferenceIndexIsNonUnique, canonicalizationSourceIndexUnique int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'external_authorization_audit' AND column_name = 'canonicalization_audit_id' AND is_nullable = 'YES'),
			(SELECT COUNT(*) FROM pg_constraint WHERE conname IN ('external_authorization_audit_canonicalization_audit_id_fkey', 'auth_provider_policy_audit_actor_user_id_fkey', 'auth_provider_policy_audit_plugin_installation_id_fkey') AND confdeltype = 'r'),
			(SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auth_provider_policy_audit' AND column_name IN ('actor_user_id', 'plugin_installation_id', 'capability_id', 'old_trusted_link_mode', 'new_trusted_link_mode', 'old_default_login', 'new_default_login', 'reason_code', 'correlation_id', 'created_at')),
			(SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname IN ('idx_external_authorization_audit_canonicalization_audit', 'idx_external_identity_link_audit_one_canonicalization_per_source', 'idx_auth_provider_policy_audit_installation_created', 'idx_auth_provider_policy_audit_actor_created')),
			(SELECT COUNT(*) FROM pg_index index_definition JOIN pg_class index_relation ON index_relation.oid = index_definition.indexrelid WHERE index_relation.relname = 'idx_external_authorization_audit_canonicalization_audit' AND NOT index_definition.indisunique),
			(SELECT COUNT(*) FROM pg_index index_definition JOIN pg_class index_relation ON index_relation.oid = index_definition.indexrelid WHERE index_relation.relname = 'idx_external_identity_link_audit_one_canonicalization_per_source' AND index_definition.indisunique)`).Scan(&nullableReference, &restrictedReferences, &policyColumns, &indexes, &authorizationReferenceIndexIsNonUnique, &canonicalizationSourceIndexUnique); err != nil {
		t.Fatalf("inspect bound audit schema: %v", err)
	}
	if nullableReference != 1 || restrictedReferences != 3 || policyColumns != 10 || indexes != 4 || authorizationReferenceIndexIsNonUnique != 1 || canonicalizationSourceIndexUnique != 1 {
		t.Fatalf("bound audit schema = nullable reference:%d restricted references:%d policy columns:%d indexes:%d non-unique authorization index:%d unique source index:%d", nullableReference, restrictedReferences, policyColumns, indexes, authorizationReferenceIndexIsNonUnique, canonicalizationSourceIndexUnique)
	}
}
