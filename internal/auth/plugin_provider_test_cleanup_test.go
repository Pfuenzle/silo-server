package auth

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// requireDisposableTestDB verifies that the current SILO_TEST_DATABASE_URL
// points to a disposable test database before any destructive cleanup
// (ALTER TRIGGER, DELETE). The guard accepts a database whose name starts
// with "silo_" or whose URL contains "TEST_DB_ALLOW_DESTRUCTIVE=1".
// If neither condition holds the test is fatally aborted.
func requireDisposableTestDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("TEST_DB_ALLOW_DESTRUCTIVE") == "1" {
		return
	}
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("requireDisposableTestDB: SILO_TEST_DATABASE_URL is empty; cannot verify test database safety")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("requireDisposableTestDB: parse SILO_TEST_DATABASE_URL: %v", err)
	}
	dbName := strings.TrimPrefix(parsed.Path, "/")
	if strings.HasPrefix(dbName, "silo_") {
		return
	}
	// Fallback: verify via connection-level current_database().
	var currentDB string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&currentDB); err != nil {
		t.Fatalf("requireDisposableTestDB: query current_database: %v", err)
	}
	if strings.HasPrefix(currentDB, "silo_") {
		return
	}
	t.Fatalf("requireDisposableTestDB: database %q is not disposable (name must start with 'silo_' or set TEST_DB_ALLOW_DESTRUCTIVE=1)", currentDB)
}

// disableCleanupTriggers disables all immutable triggers that block DELETE
// on audit tables. Must be called inside a transaction before cleanup deletes.
// enableCleanupTriggers must be called before commit to restore safety.
func disableCleanupTriggers(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE external_identity_link_audit DISABLE TRIGGER external_identity_link_audit_immutable`,
		`ALTER TABLE auth_provider_policy_audit DISABLE TRIGGER auth_provider_policy_audit_immutable`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("disable trigger for cleanup: %v", err)
		}
	}
}

func enableCleanupTriggers(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE external_identity_link_audit ENABLE TRIGGER external_identity_link_audit_immutable`,
		`ALTER TABLE auth_provider_policy_audit ENABLE TRIGGER auth_provider_policy_audit_immutable`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("enable trigger after cleanup: %v", err)
		}
	}
}

func cleanupPluginProviderInstallation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, installationID int) {
	t.Helper()
	requireDisposableTestDB(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cleanup plugin installation %d: %v", installationID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	disableCleanupTriggers(t, ctx, tx)

	// FK-safe order: delete external_authorization_audit BEFORE
	// external_identity_link_audit because
	// external_authorization_audit.canonicalization_audit_id references
	// external_identity_link_audit(id) ON DELETE RESTRICT.
	// Delete auth_provider_policy_audit BEFORE plugin_installations because
	// auth_provider_policy_audit.plugin_installation_id references
	// plugin_installations(id) ON DELETE RESTRICT.
	for _, query := range []string{
		`DELETE FROM external_authorization_audit WHERE plugin_installation_id = $1`,
		`DELETE FROM external_authorization_states WHERE plugin_installation_id = $1`,
		`DELETE FROM external_identity_link_audit WHERE plugin_installation_id = $1`,
		`DELETE FROM auth_provider_policy_audit WHERE plugin_installation_id = $1`,
		`DELETE FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1`,
		`DELETE FROM plugin_auth_identities WHERE plugin_installation_id = $1`,
		`DELETE FROM plugin_auth_bindings WHERE plugin_installation_id = $1`,
		`DELETE FROM plugin_installations WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, query, installationID); err != nil {
			t.Fatalf("cleanup plugin installation %d: %v", installationID, err)
		}
	}

	enableCleanupTriggers(t, ctx, tx)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cleanup plugin installation %d: %v", installationID, err)
	}
}

func cleanupPluginProviderUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int) {
	t.Helper()
	requireDisposableTestDB(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cleanup test user %d: %v", userID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	disableCleanupTriggers(t, ctx, tx)

	// FK-safe order:
	// 1. Session and compat tables first (no blocking FKs).
	// 2. external_authorization_audit BEFORE external_identity_link_audit
	//    (canonicalization_audit_id → external_identity_link_audit.id RESTRICT).
	// 3. auth_provider_policy_audit BEFORE users
	//    (actor_user_id → users.id RESTRICT).
	// 4. plugin_auth_identities BEFORE users (belt-and-suspenders; CASCADE
	//    handles this too, but explicit delete avoids any edge case).
	// 5. users last.
	for _, query := range []string{
		`DELETE FROM auth_sessions WHERE user_id = $1 OR impersonator_user_id = $1`,
		`DELETE FROM jellycompat_playback_sessions WHERE user_id = ($1::bigint)::text`,
		`DELETE FROM jellycompat_sessions WHERE streamapp_user_id = $1`,
		`DELETE FROM abs_sessions WHERE user_id = $1`,
		`DELETE FROM external_authorization_audit WHERE user_id = $1`,
		`DELETE FROM external_authorization_states WHERE user_id = $1`,
		`DELETE FROM external_identity_link_audit WHERE user_id = $1`,
		`DELETE FROM auth_provider_policy_audit WHERE actor_user_id = $1`,
		`DELETE FROM plugin_auth_identities WHERE user_id = $1`,
		`DELETE FROM users WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, query, userID); err != nil {
			t.Fatalf("cleanup test user %d: %v", userID, err)
		}
	}

	enableCleanupTriggers(t, ctx, tx)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cleanup test user %d: %v", userID, err)
	}
	var residue int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, userID).Scan(&residue); err != nil || residue != 0 {
		t.Fatalf("cleanup test user %d residue=%d err=%v", userID, residue, err)
	}
}
