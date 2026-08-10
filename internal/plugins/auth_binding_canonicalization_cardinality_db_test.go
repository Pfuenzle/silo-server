package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestExternalAuthorizationAudit_RehomesMatchingHistoryRowsInOneTransaction(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	sourceUserID := seedAuthBindingRevocationUser(t, pool)
	canonicalUserID := seedAuthBindingRevocationUser(t, pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
			t.Errorf("truncate disposable canonicalization fixture: %v", err)
		}
	})

	var canonicalizationAuditID, firstAuditID, secondAuditID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
		VALUES ($1, $2, 'source-profile', $3, 'ldap', 'canonicalization', 'trusted_existing', repeat('a', 64), 'duplicate_canonicalized', gen_random_uuid()) RETURNING id`, canonicalUserID, sourceUserID, installationID).Scan(&canonicalizationAuditID); err != nil {
		t.Fatalf("seed canonicalization audit: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id)
		VALUES ($1, $2, 'ldap', 'user', 'admin', ARRAY[]::TEXT[], 'trusted_link_policy', gen_random_uuid()) RETURNING id`, sourceUserID, installationID).Scan(&firstAuditID); err != nil {
		t.Fatalf("seed first authorization audit: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id)
		VALUES ($1, $2, 'ldap', 'user', 'admin', ARRAY[]::TEXT[], 'trusted_link_policy', gen_random_uuid()) RETURNING id`, sourceUserID, installationID).Scan(&secondAuditID); err != nil {
		t.Fatalf("seed second authorization audit: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bulk re-home transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	// When
	_, err = tx.Exec(ctx, `
		UPDATE external_authorization_audit
		SET user_id = $1, original_profile_id = 'source-profile', canonicalization_audit_id = $2
		WHERE id IN ($3, $4)`, canonicalUserID, canonicalizationAuditID, firstAuditID, secondAuditID)
	if err == nil {
		err = tx.Commit(ctx)
	}

	// Then
	if err != nil {
		t.Fatalf("bulk re-home matching authorization history: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, sourceUserID); err != nil {
		t.Fatalf("delete canonicalized source user: %v", err)
	}
	var orphanedUsersOrReferences int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM external_authorization_audit authorization_audit
		LEFT JOIN users canonical_user ON canonical_user.id = authorization_audit.user_id
		LEFT JOIN external_identity_link_audit canonicalization ON canonicalization.id = authorization_audit.canonicalization_audit_id
		WHERE authorization_audit.id IN ($1, $2)
			AND (canonical_user.id IS NULL OR canonicalization.id IS NULL)`, firstAuditID, secondAuditID).Scan(&orphanedUsersOrReferences); err != nil {
		t.Fatalf("count user or canonicalization-reference orphans: %v", err)
	}
	if orphanedUsersOrReferences != 0 {
		t.Fatalf("user or canonicalization-reference orphans = %d, want 0", orphanedUsersOrReferences)
	}
}

func TestExternalIdentityLinkAudit_AllowsOnlyOneCanonicalizationTargetPerSource(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	ctx := context.Background()
	installationID := seedAuthGroupMappingInstallation(t, pool)
	sourceUserID := seedAuthBindingRevocationUser(t, pool)
	targetUserIDs := []int{seedAuthBindingRevocationUser(t, pool), seedAuthBindingRevocationUser(t, pool)}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE external_authorization_audit, external_identity_link_audit`); err != nil {
			t.Errorf("truncate disposable competing-target fixture: %v", err)
		}
	})
	start := make(chan struct{})
	errs := make(chan error, len(targetUserIDs))
	var wait sync.WaitGroup
	wait.Add(len(targetUserIDs))

	// When
	for _, targetUserID := range targetUserIDs {
		go func(targetUserID int) {
			defer wait.Done()
			<-start
			_, err := pool.Exec(ctx, `
				INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
				VALUES ($1, $2, 'source-profile', $3, 'ldap', 'canonicalization', 'trusted_existing', repeat('b', 64), 'duplicate_canonicalized', gen_random_uuid())`, targetUserID, sourceUserID, installationID)
			errs <- err
		}(targetUserID)
	}
	close(start)
	wait.Wait()
	close(errs)

	// Then
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("competing canonicalization error = %v, want unique violation", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful canonicalization targets = %d, want 1", successes)
	}
}
