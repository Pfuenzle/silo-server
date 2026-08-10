package plugins

import (
	"context"
	"testing"
)

func TestExternalAuthorizationAudit_RehomeRequiresMatchingCanonicalizationAudit(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	sourceUserID := seedAuthBindingRevocationUser(t, pool)
	canonicalUserID := seedAuthBindingRevocationUser(t, pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
			t.Errorf("truncate disposable canonicalization audit fixture: %v", err)
		}
	})

	var authorizationAuditID, canonicalizationAuditID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO external_authorization_audit (
			user_id, plugin_installation_id, capability_id, old_role, new_role,
			matched_group_ids, reason, correlation_id
		) VALUES ($1, $2, 'ldap', 'user', 'admin', ARRAY[]::TEXT[],
			'trusted_link_policy', '00000000-0000-0000-0000-000000000011')
		RETURNING id`, sourceUserID, installationID).Scan(&authorizationAuditID); err != nil {
		t.Fatalf("seed authorization audit: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO external_identity_link_audit (
			user_id, original_user_id, original_profile_id, plugin_installation_id,
			capability_id, event_type, trusted_link_mode, external_subject_fingerprint,
			reason_code, correlation_id
		) VALUES ($1, $2, 'source-profile', $3, 'ldap', 'canonicalization',
			'trusted_existing', repeat('d', 64), 'duplicate_canonicalized',
			'00000000-0000-0000-0000-000000000012') RETURNING id`, canonicalUserID, sourceUserID, installationID).Scan(&canonicalizationAuditID); err != nil {
		t.Fatalf("seed canonicalization audit: %v", err)
	}

	// When
	_, arbitraryTargetErr := pool.Exec(ctx, `
		UPDATE external_authorization_audit
		SET user_id = $2, original_profile_id = 'source-profile', canonicalization_audit_id = $3
		WHERE id = $1`, authorizationAuditID, sourceUserID, canonicalizationAuditID)
	_, err := pool.Exec(ctx, `
		UPDATE external_authorization_audit
		SET user_id = $2, original_profile_id = 'source-profile', canonicalization_audit_id = $3
		WHERE id = $1`, authorizationAuditID, canonicalUserID, canonicalizationAuditID)

	// Then
	assertImmutableAuditViolation(t, arbitraryTargetErr)
	if err != nil {
		t.Fatalf("re-home with matching canonicalization audit: %v", err)
	}
	var canonicalizationAuditReference int64
	if err := pool.QueryRow(ctx, `SELECT canonicalization_audit_id FROM external_authorization_audit WHERE id = $1`, authorizationAuditID).Scan(&canonicalizationAuditReference); err != nil {
		t.Fatalf("read canonicalization authorization: %v", err)
	}
	if canonicalizationAuditReference != canonicalizationAuditID {
		t.Fatalf("canonicalization audit reference = %d, want %d", canonicalizationAuditReference, canonicalizationAuditID)
	}
	_, secondRehomeErr := pool.Exec(ctx, `UPDATE external_authorization_audit SET user_id = $2 WHERE id = $1`, authorizationAuditID, sourceUserID)
	assertImmutableAuditViolation(t, secondRehomeErr)
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, sourceUserID); err != nil {
		t.Fatalf("delete re-homed source user: %v", err)
	}
	var orphanedCanonicalizationReferences int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM external_authorization_audit authorization_audit
		LEFT JOIN external_identity_link_audit canonicalization ON canonicalization.id = authorization_audit.canonicalization_audit_id
		WHERE authorization_audit.id = $1 AND canonicalization.id IS NULL`, authorizationAuditID).Scan(&orphanedCanonicalizationReferences); err != nil {
		t.Fatalf("count canonicalization audit orphans after source deletion: %v", err)
	}
	if orphanedCanonicalizationReferences != 0 {
		t.Fatalf("orphaned canonicalization audit references after source deletion = %d, want 0", orphanedCanonicalizationReferences)
	}
}

func TestExternalAuthorizationAudit_RehomeRejectsMismatchedCanonicalizationAudit(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	otherInstallationID := seedAuthGroupMappingInstallation(t, pool)
	canonicalUserID := seedAuthBindingRevocationUser(t, pool)
	otherUserID := seedAuthBindingRevocationUser(t, pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
			t.Errorf("truncate disposable canonicalization audit fixture: %v", err)
		}
	})

	for _, mismatch := range []struct {
		name          string
		sourceMatches bool
		auditTargetID int
		profileID     string
		capabilityID  string
		installation  int
	}{
		{name: "source", auditTargetID: canonicalUserID, profileID: "source-profile", capabilityID: "ldap", installation: installationID},
		{name: "target", sourceMatches: true, auditTargetID: otherUserID, profileID: "source-profile", capabilityID: "ldap", installation: installationID},
		{name: "profile", sourceMatches: true, auditTargetID: canonicalUserID, profileID: "other-profile", capabilityID: "ldap", installation: installationID},
		{name: "installation", sourceMatches: true, auditTargetID: canonicalUserID, profileID: "source-profile", capabilityID: "ldap", installation: otherInstallationID},
		{name: "capability", sourceMatches: true, auditTargetID: canonicalUserID, profileID: "source-profile", capabilityID: "oidc", installation: installationID},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			// Given
			sourceUserID := seedAuthBindingRevocationUser(t, pool)
			canonicalizationSourceUserID := sourceUserID
			if !mismatch.sourceMatches {
				canonicalizationSourceUserID = seedAuthBindingRevocationUser(t, pool)
			}
			t.Cleanup(func() {
				if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
					t.Errorf("truncate disposable mismatch fixture: %v", err)
				}
			})
			var authorizationAuditID, canonicalizationAuditID int64
			if err := pool.QueryRow(ctx, `
				INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id)
				VALUES ($1, $2, 'ldap', 'user', 'admin', ARRAY[]::TEXT[], 'trusted_link_policy', gen_random_uuid()) RETURNING id`, sourceUserID, installationID).Scan(&authorizationAuditID); err != nil {
				t.Fatalf("seed authorization audit: %v", err)
			}
			if err := pool.QueryRow(ctx, `
				INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
				VALUES ($1, $2, $3, $4, $5, 'canonicalization', 'trusted_existing', repeat('e', 64), 'duplicate_canonicalized', gen_random_uuid()) RETURNING id`, mismatch.auditTargetID, canonicalizationSourceUserID, mismatch.profileID, mismatch.installation, mismatch.capabilityID).Scan(&canonicalizationAuditID); err != nil {
				t.Fatalf("seed mismatched canonicalization audit: %v", err)
			}

			// When
			_, err := pool.Exec(ctx, `
				UPDATE external_authorization_audit
				SET user_id = $2, original_profile_id = 'source-profile', canonicalization_audit_id = $3
				WHERE id = $1`, authorizationAuditID, canonicalUserID, canonicalizationAuditID)

			// Then
			assertImmutableAuditViolation(t, err)
		})
	}
}

func TestExternalAuthorizationAudit_PreservesOriginalUserAndRejectsUnboundRehome(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	canonicalUserID := seedAuthBindingRevocationUser(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin authorization audit transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	// When
	var auditID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO external_authorization_audit (
			user_id, plugin_installation_id, capability_id, old_role, new_role,
			matched_group_ids, reason, correlation_id, original_user_id
		) VALUES ($1, $2, 'ldap', 'user', 'admin', ARRAY[]::TEXT[],
			'trusted_link_policy', '00000000-0000-0000-0000-000000000002', $3) RETURNING id`, userID, installationID, canonicalUserID).Scan(&auditID)

	// Then
	if err != nil {
		t.Fatalf("insert authorization audit: %v", err)
	}
	var originalUserID int
	if err := tx.QueryRow(ctx, `SELECT original_user_id FROM external_authorization_audit WHERE id = $1`, auditID).Scan(&originalUserID); err != nil {
		t.Fatalf("read authorization audit provenance: %v", err)
	}
	if originalUserID != userID {
		t.Fatalf("original user ID = %d, want %d", originalUserID, userID)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT authorization_rehome`); err != nil {
		t.Fatalf("create unbound re-home savepoint: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE external_authorization_audit SET user_id = $2, original_profile_id = 'source-profile' WHERE id = $1`, auditID, canonicalUserID)
	assertImmutableAuditViolation(t, err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT authorization_rehome`); err != nil {
		t.Fatalf("rollback unbound re-home savepoint: %v", err)
	}
}

func TestAuthProviderPolicyAudit_IsTruthfulAppendOnlyAndHasOnlyRealReferences(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	actorUserID := seedAuthBindingRevocationUser(t, pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE auth_provider_policy_audit`); err != nil {
			t.Errorf("truncate disposable provider policy audit fixture: %v", err)
		}
	})

	// When
	_, unchangedErr := pool.Exec(ctx, `
		INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id)
		VALUES ($1, $2, 'ldap', 'disabled', 'disabled', false, false, 'no_change', gen_random_uuid())`, actorUserID, installationID)
	var auditID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id)
		VALUES ($1, $2, 'ldap', 'disabled', 'trusted_existing', false, false, 'trusted_link_enabled', gen_random_uuid()) RETURNING id`, actorUserID, installationID).Scan(&auditID)

	// Then
	assertCheckViolation(t, unchangedErr)
	if err != nil {
		t.Fatalf("insert truthful provider policy audit: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE auth_provider_policy_audit SET reason_code = 'tampered' WHERE id = $1`, auditID)
	assertImmutableAuditViolation(t, err)
	_, deleteErr := pool.Exec(ctx, `DELETE FROM auth_provider_policy_audit WHERE id = $1`, auditID)
	assertImmutableAuditViolation(t, deleteErr)
	_, missingActorErr := pool.Exec(ctx, `
		INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id)
		VALUES (999999999, $1, 'ldap', 'disabled', 'trusted_existing', false, false, 'missing_actor', gen_random_uuid())`, installationID)
	assertForeignKeyViolation(t, missingActorErr)
	_, missingInstallationErr := pool.Exec(ctx, `
		INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id)
		VALUES ($1, 999999999, 'ldap', 'disabled', 'trusted_existing', false, false, 'missing_installation', gen_random_uuid())`, actorUserID)
	assertForeignKeyViolation(t, missingInstallationErr)
	var identityColumns int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'auth_provider_policy_audit'
			AND (column_name LIKE '%identity%' OR column_name LIKE '%subject%' OR column_name LIKE '%username%')`).Scan(&identityColumns); err != nil {
		t.Fatalf("inspect policy audit columns: %v", err)
	}
	if identityColumns != 0 {
		t.Fatalf("policy audit exposes %d identity columns", identityColumns)
	}
}

func TestExternalIdentityLinkAudit_RejectsNonIdentityPolicyEvent(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)

	// When
	_, err := pool.Exec(context.Background(), `
		INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
		VALUES ($1, $1, '', $2, 'ldap', 'policy_change', 'disabled', repeat('a', 64), 'policy', gen_random_uuid())`, userID, installationID)

	// Then
	assertCheckViolation(t, err)
}
