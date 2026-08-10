package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthBinding_RejectsMalformedTrustedLinkModeBeforeDatabaseAccess(t *testing.T) {
	// Given
	store := NewRuntimeConfigStore(nil)

	// When
	err := store.UpsertAuthBinding(context.Background(), 1, AuthBinding{
		InstallationID:  1,
		CapabilityID:    "ldap",
		TrustedLinkMode: AuthBindingTrustedLinkMode("malformed"),
	})

	// Then
	if !errors.Is(err, ErrAuthBindingTrustedLinkModeInvalid) {
		t.Fatalf("UpsertAuthBinding() error = %v, want invalid trusted link mode", err)
	}
}

func TestAuthBinding_TrustedLinkModeDefaultsToDisabledAndRoundTrips(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	actorID := seedAuthBindingRevocationUser(t, pool)
	store := NewRuntimeConfigStore(pool)

	// When
	if err := store.UpsertAuthBinding(context.Background(), actorID, AuthBinding{
		InstallationID: installationID,
		CapabilityID:   "ldap",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("create omitted-mode binding: %v", err)
	}
	got, err := store.GetAuthBinding(context.Background(), installationID, "ldap")

	// Then
	if err != nil {
		t.Fatalf("GetAuthBinding() error = %v", err)
	}
	if got.TrustedLinkMode != AuthBindingTrustedLinkModeDisabled {
		t.Fatalf("omitted trusted link mode = %q, want disabled", got.TrustedLinkMode)
	}
}

func TestAuthBinding_RejectsInvalidTrustedLinkMode_whenPersisted(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)

	// When
	_, err := pool.Exec(context.Background(), `
		INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, trusted_link_mode)
		VALUES ($1, 'ldap', 'unexpected')`, installationID)

	// Then
	assertCheckViolation(t, err)
}

func TestAuthBinding_RejectsSecondDefaultAndAllowsZeroDefaults_whenPersisted(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, default_login)
		VALUES ($1, 'ldap', true, true), ($1, 'oidc', true, false)`, installationID); err != nil {
		t.Fatalf("seed default and local fallback binding: %v", err)
	}

	// When
	_, err := pool.Exec(ctx, `
		UPDATE plugin_auth_bindings
		SET default_login = true
		WHERE plugin_installation_id = $1 AND capability_id = 'oidc'`, installationID)

	// Then
	assertUniqueViolation(t, err)
	if _, err := pool.Exec(ctx, `
		UPDATE plugin_auth_bindings
		SET default_login = false
		WHERE plugin_installation_id = $1`, installationID); err != nil {
		t.Fatalf("clear plugin defaults for local fallback: %v", err)
	}
	var defaults int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_bindings WHERE default_login`).Scan(&defaults); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	if defaults != 0 {
		t.Fatalf("plugin defaults = %d, want zero for local fallback", defaults)
	}
	_, err = pool.Exec(ctx, `
		UPDATE plugin_auth_bindings
		SET enabled = false, default_login = true
		WHERE plugin_installation_id = $1 AND capability_id = 'oidc'`, installationID)
	assertCheckViolation(t, err)
}

func TestAuthBinding_RejectsConcurrentSecondDefault_whenStoreWritesRace(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationIDs := []int{
		seedAuthGroupMappingInstallation(t, pool),
		seedAuthGroupMappingInstallation(t, pool),
	}
	actorID := seedAuthBindingRevocationUser(t, pool)
	firstPool := authBindingConcurrentPool(t)
	secondPool := authBindingConcurrentPool(t)
	stores := []*RuntimeConfigStore{NewRuntimeConfigStore(firstPool), NewRuntimeConfigStore(secondPool)}
	start := make(chan struct{})
	errs := make(chan error, len(stores))
	var wait sync.WaitGroup
	wait.Add(len(stores))

	// When
	for index, store := range stores {
		capabilityID := []string{"ldap", "oidc"}[index]
		installationID := installationIDs[index]
		go func(store *RuntimeConfigStore, capabilityID string) {
			defer wait.Done()
			<-start
			errs <- store.UpsertAuthBinding(context.Background(), actorID, AuthBinding{
				InstallationID: installationID,
				CapabilityID:   capabilityID,
				Enabled:        true,
				DefaultLogin:   true,
			})
		}(store, capabilityID)
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
		if !errors.Is(err, ErrAuthBindingDefaultLoginConflict) {
			t.Fatalf("concurrent default error = %v, want ErrAuthBindingDefaultLoginConflict", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful defaults = %d, want one", successes)
	}
}

func TestExternalIdentityLinkAudit_IsRedactedAppendOnlyAndPreservesOriginalSource(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin audit transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	// When
	var auditID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO external_identity_link_audit (
			user_id, original_user_id, original_profile_id, plugin_installation_id,
			capability_id, event_type, trusted_link_mode, external_subject_fingerprint,
			reason_code, correlation_id
		) VALUES ($1, $1, '', $2, 'ldap', 'trusted_link',
			'trusted_existing', repeat('a', 64), 'trusted_existing_match',
			'00000000-0000-0000-0000-000000000001') RETURNING id`, userID, installationID).Scan(&auditID)

	// Then
	if err != nil {
		t.Fatalf("insert redacted audit: %v", err)
	}
	var originalUserID int
	var originalProfileID string
	if err := tx.QueryRow(ctx, `
		SELECT original_user_id, original_profile_id
		FROM external_identity_link_audit WHERE id = $1`, auditID).Scan(&originalUserID, &originalProfileID); err != nil {
		t.Fatalf("read audit preservation fields: %v", err)
	}
	if originalUserID != userID || originalProfileID != "" {
		t.Fatalf("ordinary provenance = (%d, %q), want current user and empty profile", originalUserID, originalProfileID)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT audit_update_tamper`); err != nil {
		t.Fatalf("create update tamper savepoint: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE external_identity_link_audit SET reason_code = 'tampered' WHERE id = $1`, auditID)
	assertImmutableAuditViolation(t, err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT audit_update_tamper`); err != nil {
		t.Fatalf("rollback update tamper savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT audit_delete_tamper`); err != nil {
		t.Fatalf("create delete tamper savepoint: %v", err)
	}
	_, err = tx.Exec(ctx, `DELETE FROM external_identity_link_audit WHERE id = $1`, auditID)
	assertImmutableAuditViolation(t, err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT audit_delete_tamper`); err != nil {
		t.Fatalf("rollback delete tamper savepoint: %v", err)
	}
	var rawColumns int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'external_identity_link_audit'
			AND column_name IN ('external_subject', 'asserted_username', 'provider_payload')`).Scan(&rawColumns); err != nil {
		t.Fatalf("inspect audit columns: %v", err)
	}
	if rawColumns != 0 {
		t.Fatalf("audit exposes %d raw identity columns", rawColumns)
	}
}

func TestExternalIdentityLinkAudit_RejectsMissingCanonicalReferences(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	ctx := context.Background()

	// When
	_, missingUserErr := pool.Exec(ctx, `
		INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
		VALUES (999999999, 999999999, '', $1, 'ldap', 'trusted_link', 'disabled', repeat('b', 64), 'policy', '00000000-0000-0000-0000-000000000003')`, installationID)
	_, missingInstallationErr := pool.Exec(ctx, `
		INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id)
		VALUES ($1, $1, '', 999999999, 'ldap', 'trusted_link', 'disabled', repeat('c', 64), 'policy', '00000000-0000-0000-0000-000000000004')`, userID)

	// Then
	assertForeignKeyViolation(t, missingUserErr)
	assertForeignKeyViolation(t, missingInstallationErr)
}

func authBindingConcurrentPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := authGroupMappingTestPool(t)
	return pool
}

func assertCheckViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("error = %v, want check violation", err)
	}
}

func assertUniqueViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("error = %v, want unique violation", err)
	}
}

func assertImmutableAuditViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
		t.Fatalf("error = %v, want immutable audit violation", err)
	}
}

func assertForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("error = %v, want foreign key violation", err)
	}
}
