package auth

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestExternalAccountCanonicalization_transfersDeduplicatedPersonalization_whenPreviewMatches(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	targetID := insertPluginProviderTestUser(t, ctx, pool, "canonical-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "canonical-source")
	targetProfile := fmt.Sprintf("canonical-target-profile-%d", targetID)
	sourceProfile := fmt.Sprintf("canonical-source-profile-%d", sourceID)
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, sourceID); err != nil {
		t.Fatalf("make source provider-only: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Target', true), ($3, $4, 'Source', true)`, targetProfile, targetID, sourceProfile, sourceID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'canonical-subject', $2)`, installationID, sourceID); err != nil {
		t.Fatalf("seed source identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization', true, true, 'none')`, installationID); err != nil {
		t.Fatalf("seed canonicalization binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) VALUES ($1, $3, 'shared', '2026-01-01T00:00:00Z'), ($2, $4, 'shared', '2026-02-01T00:00:00Z'), ($2, $4, 'source-only', '2026-02-01T00:00:00Z')`, targetID, sourceID, targetProfile, sourceProfile); err != nil {
		t.Fatalf("seed favorites: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite, updated_at) VALUES ($1, $3, 1, 'shared', true, '2026-01-01T00:00:00Z'), ($2, $4, 1, 'shared', false, '2026-02-01T00:00:00Z'), ($2, $4, 1, 'source-only', true, '2026-02-01T00:00:00Z')`, targetID, sourceID, targetProfile, sourceProfile); err != nil {
		t.Fatalf("seed interests: %v", err)
	}
	service := NewExternalAccountCanonicalizer(pool, []byte("test-preview-key"), time.Now).WithStoreProvider(pgstore.NewPostgresProvider(pool))

	// When
	preview, err := service.Preview(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, sourceID, targetID)
	if err != nil {
		t.Fatalf("preview canonicalization: %v", err)
	}
	receipt, err := service.Execute(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, preview.Token)

	// Then
	if err != nil {
		t.Fatalf("execute canonicalization: %v", err)
	}
	if !receipt.SourceDeleted || receipt.IdentityOwner != targetID || receipt.FavoritesTransferred != 1 || receipt.InterestsTransferred != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	var favorites, interests int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2`, targetID, targetProfile).Scan(&favorites); err != nil {
		t.Fatalf("count target favorites: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM profile_series_interest WHERE user_id = $1 AND profile_id = $2`, targetID, targetProfile).Scan(&interests); err != nil {
		t.Fatalf("count target interests: %v", err)
	}
	if favorites != 2 || interests != 2 {
		t.Fatalf("canonical personalization = favorites:%d interests:%d; want 2,2", favorites, interests)
	}
	replay, err := service.Execute(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, preview.Token)
	if err != nil {
		t.Fatalf("replay canonicalization: %v", err)
	}
	if !replay.SourceDeleted || replay.IdentityOwner != targetID {
		t.Fatalf("replay receipt = %+v", replay)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `TRUNCATE external_authorization_audit, external_identity_link_audit CASCADE`); err != nil {
			t.Errorf("cleanup canonicalization audit: %v", err)
		}
	})
}

func TestExternalAccountCanonicalization_rejectsUnsupportedDependency_withoutMutating(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	targetID := insertPluginProviderTestUser(t, ctx, pool, "blocked-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "blocked-source")
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, sourceID); err != nil {
		t.Fatalf("make source provider-only: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('blocked-target-profile', $1, 'Target', true), ('blocked-source-profile', $2, 'Source', true)`, targetID, sourceID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'blocked-subject', $2)`, installationID, sourceID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization', true, true, 'none')`, installationID); err != nil {
		t.Fatalf("seed canonicalization binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id) VALUES ($1, 'blocked-source-profile', 'unsupported')`, sourceID); err != nil {
		t.Fatalf("seed unsupported dependency: %v", err)
	}
	service := NewExternalAccountCanonicalizer(pool, []byte("test-preview-key"), time.Now).WithStoreProvider(pgstore.NewPostgresProvider(pool))

	// When
	preview, err := service.Preview(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, sourceID, targetID)
	if err != nil {
		t.Fatalf("preview canonicalization: %v", err)
	}
	_, err = service.Execute(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, preview.Token)

	// Then
	if !errors.Is(err, ErrCanonicalizationBlocked) {
		t.Fatalf("execute error = %v, want blocked", err)
	}
	var users, watchlist int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, sourceID).Scan(&users); err != nil {
		t.Fatalf("count source users: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_watchlist WHERE user_id = $1`, sourceID).Scan(&watchlist); err != nil {
		t.Fatalf("count source watchlist: %v", err)
	}
	if users != 1 || watchlist != 1 {
		t.Fatalf("blocked mutations = users:%d watchlist:%d; want 1,1", users, watchlist)
	}
}

func TestExternalAccountCanonicalization_rehomesMultipleAuthorizationAudits_preservingProvenance(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	targetID := insertPluginProviderTestUser(t, ctx, pool, "multi-audit-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "multi-audit-source")
	targetProfile := fmt.Sprintf("multi-audit-target-profile-%d", targetID)
	sourceProfile := fmt.Sprintf("multi-audit-source-profile-%d", sourceID)
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, sourceID); err != nil {
		t.Fatalf("make source provider-only: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Target', true), ($3, $4, 'Source', true)`, targetProfile, targetID, sourceProfile, sourceID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'multi-audit-subject', $2)`, installationID, sourceID); err != nil {
		t.Fatalf("seed source identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization', true, true, 'none')`, installationID); err != nil {
		t.Fatalf("seed canonicalization binding: %v", err)
	}
	var firstAuditID, secondAuditID int64
	if err := pool.QueryRow(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'canonicalization', 'user', 'user', ARRAY[]::text[], 'canonicalization', $3) RETURNING id`, sourceID, installationID, uuid.New()).Scan(&firstAuditID); err != nil {
		t.Fatalf("seed first authorization audit: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'canonicalization', 'admin', 'user', ARRAY['ldap-admins']::text[], 'canonicalization', $3) RETURNING id`, sourceID, installationID, uuid.New()).Scan(&secondAuditID); err != nil {
		t.Fatalf("seed second authorization audit: %v", err)
	}
	for _, auditID := range []int64{firstAuditID, secondAuditID} {
		var originalUserID int
		if err := pool.QueryRow(ctx, `SELECT original_user_id FROM external_authorization_audit WHERE id = $1`, auditID).Scan(&originalUserID); err != nil {
			t.Fatalf("read original_user_id for audit %d: %v", auditID, err)
		}
		if originalUserID != sourceID {
			t.Fatalf("pre-canonicalization original_user_id for audit %d = %d, want %d", auditID, originalUserID, sourceID)
		}
	}
	service := NewExternalAccountCanonicalizer(pool, []byte("test-preview-key"), time.Now).WithStoreProvider(pgstore.NewPostgresProvider(pool))

	// When
	preview, err := service.Preview(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, sourceID, targetID)
	if err != nil {
		t.Fatalf("preview canonicalization: %v", err)
	}
	receipt, err := service.Execute(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, preview.Token)

	// Then
	if err != nil {
		t.Fatalf("execute canonicalization: %v", err)
	}
	if !receipt.SourceDeleted || receipt.IdentityOwner != targetID {
		t.Fatalf("receipt = %+v", receipt)
	}
	for _, auditID := range []int64{firstAuditID, secondAuditID} {
		var userID, originalUserID int
		var originalProfileID string
		var canonicalizationAuditID int64
		if err := pool.QueryRow(ctx, `SELECT user_id, original_user_id, original_profile_id, canonicalization_audit_id FROM external_authorization_audit WHERE id = $1`, auditID).Scan(&userID, &originalUserID, &originalProfileID, &canonicalizationAuditID); err != nil {
			t.Fatalf("read re-homed authorization audit %d: %v", auditID, err)
		}
		if userID != targetID {
			t.Fatalf("authorization audit %d user_id = %d, want %d", auditID, userID, targetID)
		}
		if originalUserID != sourceID {
			t.Fatalf("authorization audit %d original_user_id = %d, want %d", auditID, originalUserID, sourceID)
		}
		if originalProfileID != sourceProfile {
			t.Fatalf("authorization audit %d original_profile_id = %q, want %q", auditID, originalProfileID, sourceProfile)
		}
		if canonicalizationAuditID == 0 {
			t.Fatalf("authorization audit %d canonicalization_audit_id = 0, want non-zero", auditID)
		}
	}
	var sourceUsers int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, sourceID).Scan(&sourceUsers); err != nil {
		t.Fatalf("count source users: %v", err)
	}
	if sourceUsers != 0 {
		t.Fatalf("source users = %d, want 0 after canonicalization", sourceUsers)
	}
	var orphanedReferences int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM external_authorization_audit aa LEFT JOIN external_identity_link_audit ca ON ca.id = aa.canonicalization_audit_id WHERE aa.user_id = $1 AND ca.id IS NULL`, targetID).Scan(&orphanedReferences); err != nil {
		t.Fatalf("count orphaned canonicalization references: %v", err)
	}
	if orphanedReferences != 0 {
		t.Fatalf("orphaned canonicalization references = %d, want 0", orphanedReferences)
	}
	replay, err := service.Execute(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, preview.Token)
	if err != nil {
		t.Fatalf("replay canonicalization: %v", err)
	}
	if !replay.SourceDeleted || replay.IdentityOwner != targetID {
		t.Fatalf("replay receipt = %+v", replay)
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `TRUNCATE external_authorization_audit, external_identity_link_audit CASCADE`); err != nil {
			t.Errorf("cleanup canonicalization audit: %v", err)
		}
	})
}

func TestExternalAccountCanonicalization_dependencyRegistryCoversUserOwnershipColumns(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	registered := canonicalizationOwnershipTables

	// When — normalize partition children to their parent so the static
	// registry tracks parent partitioned tables rather than every
	// time-based partition that the scheduler creates.
	rows, err := pool.Query(ctx, `SELECT DISTINCT COALESCE(parent.relname, c.table_name) AS table_name FROM information_schema.columns c JOIN pg_class rel ON rel.relname = c.table_name AND rel.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = c.table_schema) LEFT JOIN pg_inherits inh ON inh.inhrelid = rel.oid LEFT JOIN pg_class parent ON parent.oid = inh.inhparent WHERE c.table_schema = 'public' AND c.column_name = 'user_id' ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list user ownership columns: %v", err)
	}
	defer rows.Close()
	missing := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan ownership table: %v", err)
		}
		if _, ok := registered[table]; !ok {
			missing = append(missing, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ownership tables: %v", err)
	}

	// Then
	if len(missing) != 0 {
		t.Fatalf("user ownership tables absent from canonicalization dependency registry: %v", missing)
	}
}
