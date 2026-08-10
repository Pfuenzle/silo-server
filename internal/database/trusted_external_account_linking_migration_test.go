package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

const trustedLinkMigrationVersion int64 = 20260805142326
const trustedLinkPreMigrationVersion int64 = 20260803191207

func TestTrustedExternalAccountLinkingMigration_normalizesLegacyBindingsAndKeepsAuditReferencesValid(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool := newTrustedLinkMigrationPool(t, ctx)
	preFeatureFS := migrationFSThrough(t, trustedLinkPreMigrationVersion)
	if err := RunMigrations(ctx, pool, preFeatureFS, "sql"); err != nil {
		t.Fatalf("migrate pre-feature schema: %v", err)
	}
	seed := seedTrustedLinkPreFeatureRows(t, ctx, pool)

	// When
	if err := RunMigrations(ctx, pool, migrationFSThrough(t, trustedLinkMigrationVersion), "sql"); err != nil {
		t.Fatalf("migrate trusted external account linking: %v", err)
	}

	// Then
	var trustedModes, defaults int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE trusted_link_mode <> 'disabled'), COUNT(*) FILTER (WHERE default_login) FROM plugin_auth_bindings`).Scan(&trustedModes, &defaults); err != nil {
		t.Fatalf("read normalized bindings: %v", err)
	}
	if trustedModes != 0 || defaults != 0 {
		t.Fatalf("normalized bindings = trusted modes %d, defaults %d; want zero, zero", trustedModes, defaults)
	}
	var identityOwner int
	if err := pool.QueryRow(ctx, `SELECT user_id FROM plugin_auth_identities WHERE id = $1`, seed.identityID).Scan(&identityOwner); err != nil {
		t.Fatalf("read immutable identity: %v", err)
	}
	if identityOwner != seed.userID {
		t.Fatalf("identity owner = %d, want %d", identityOwner, seed.userID)
	}
	var originalUserID int
	var canonicalizationAuditIDIsNull bool
	if err := pool.QueryRow(ctx, `SELECT original_user_id FROM external_authorization_audit WHERE id = $1`, seed.authorizationAuditID).Scan(&originalUserID); err != nil {
		t.Fatalf("read authorization audit provenance: %v", err)
	}
	if originalUserID != seed.userID {
		t.Fatalf("authorization audit original user = %d, want %d", originalUserID, seed.userID)
	}
	if err := pool.QueryRow(ctx, `SELECT canonicalization_audit_id IS NULL FROM external_authorization_audit WHERE id = $1`, seed.authorizationAuditID).Scan(&canonicalizationAuditIDIsNull); err != nil {
		t.Fatalf("read authorization audit canonicalization reference: %v", err)
	}
	if !canonicalizationAuditIDIsNull {
		t.Fatal("legacy authorization audit canonicalization reference is not NULL")
	}
	assertTrustedLinkForeignKeyViolation(t, pool, ctx, 999999999, seed.installationIDs[0])
	assertTrustedLinkForeignKeyViolation(t, pool, ctx, seed.userID, 999999999)
	t.Log("upgrade receipt: trusted_modes=0 defaults=0 identity_owner_unchanged=true audit_provenance=true audit_fks=true")
}

type trustedLinkSeed struct {
	userID               int
	identityID           int
	authorizationAuditID int
	installationIDs      [3]int
}

func newTrustedLinkMigrationPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	config.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect postgres maintenance database: %v", err)
	}
	name := fmt.Sprintf("silo_trusted_link_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		adminPool.Close()
		t.Fatalf("create isolated migration database: %v", err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("connect isolated migration database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = adminPool.Exec(context.Background(), `DROP DATABASE `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
		adminPool.Close()
	})
	return pool
}

func migrationFSThrough(t *testing.T, lastVersion int64) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	err := fs.WalkDir(migrations.FS, "sql", func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		version, err := strconv.ParseInt(strings.SplitN(path.Base(file), "_", 2)[0], 10, 64)
		if err != nil || version > lastVersion {
			return err
		}
		contents, err := fs.ReadFile(migrations.FS, file)
		if err != nil {
			return err
		}
		files[file] = &fstest.MapFile{Data: contents}
		return nil
	})
	if err != nil {
		t.Fatalf("build filtered migration filesystem: %v", err)
	}
	return files
}

func seedTrustedLinkPreFeatureRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) trustedLinkSeed {
	t.Helper()
	seed := trustedLinkSeed{}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ('trusted-link-migration', 'trusted-link-migration@example.test', 'x', 'user') RETURNING id`).Scan(&seed.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('trusted-link-profile', $1, 'Trusted Link Migration', true)`, seed.userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	for index := range seed.installationIDs {
		if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ($1, '1', $2) RETURNING id`, fmt.Sprintf("trusted-link-%d", index), fmt.Sprintf("/tmp/trusted-link-%d", index)).Scan(&seed.installationIDs[index]); err != nil {
			t.Fatalf("seed installation %d: %v", index, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, default_login) VALUES ($1, 'first', true, true), ($2, 'second', true, true), ($3, 'disabled', false, true)`, seed.installationIDs[0], seed.installationIDs[1], seed.installationIDs[2]); err != nil {
		t.Fatalf("seed malformed legacy defaults: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'immutable-subject', $2) RETURNING id`, seed.installationIDs[0], seed.userID).Scan(&seed.identityID); err != nil {
		t.Fatalf("seed immutable identity: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'first', 'user', 'user', '{}', 'migration_fixture', '00000000-0000-0000-0000-000000000001') RETURNING id`, seed.userID, seed.installationIDs[0]).Scan(&seed.authorizationAuditID); err != nil {
		t.Fatalf("seed authorization audit: %v", err)
	}
	return seed
}

func assertTrustedLinkForeignKeyViolation(t *testing.T, pool *pgxpool.Pool, ctx context.Context, userID, installationID int) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id) VALUES ($1, $1, '', $2, 'first', 'trusted_link', 'disabled', repeat('a', 64), 'migration_fixture', '00000000-0000-0000-0000-000000000002')`, userID, installationID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("missing canonical reference error = %v, want foreign key violation", err)
	}
}
