//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const irreversibleExternalAuthMigrationMessage = "external auth migration is irreversible; roll back binaries only"

type externalAuthMigrationCase struct {
	name                string
	version             int64
	hasGroupMappings    bool
	hasAuthorizationLog bool
}

type externalAuthMigrationSnapshot struct {
	version     int64
	rows        string
	constraints string
	schema      string
}

func TestAuthSecurityRegression_AuthMigrationDownIsFailClosedAndPreservesLocalLoginIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := repositoryRoot(t)
	resources := newIntegrationResources(t, ctx, root)

	for _, testCase := range []externalAuthMigrationCase{
		{name: "oauth session provider state ciphertext", version: 20260730183247},
		{name: "plugin auth group mappings", version: 20260730183302, hasGroupMappings: true},
		{name: "auth session provider key", version: 20260730202906, hasGroupMappings: true},
		{name: "external authorization audit", version: 20260730213000, hasGroupMappings: true, hasAuthorizationLog: true},
		{name: "harden external authorization audit", version: 20260730220000, hasGroupMappings: true, hasAuthorizationLog: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			databaseURL := createAuthMigrationDatabase(t, ctx, resources, testCase.version)
			t.Cleanup(func() { dropAuthMigrationDatabase(t, resources, testCase.version) })
			gooseUpTo(t, ctx, root, databaseURL, testCase.version)
			pool, err := pgxpool.New(ctx, databaseURL)
			if err != nil {
				t.Fatalf("connect migration database: %v", err)
			}
			t.Cleanup(pool.Close)
			seedExternalAuthMigrationData(t, ctx, pool, testCase)
			authenticateBreakGlassUser(t, ctx, pool)
			before := snapshotExternalAuthMigration(t, ctx, pool, testCase)
			if before.version != testCase.version {
				t.Fatalf("migration version before down = %d, want %d", before.version, testCase.version)
			}

			// When
			output, err := gooseDown(t, ctx, root, databaseURL)

			// Then
			if err == nil {
				t.Fatal("goose down succeeded for irreversible external-auth migration")
			}
			if !strings.Contains(output, irreversibleExternalAuthMigrationMessage) {
				t.Fatalf("goose down output = %q, want policy message %q", output, irreversibleExternalAuthMigrationMessage)
			}
			after := snapshotExternalAuthMigration(t, ctx, pool, testCase)
			if after != before {
				t.Fatalf("goose down changed schema or data\nbefore: %+v\nafter:  %+v", before, after)
			}
			authenticateBreakGlassUser(t, ctx, pool)
		})
	}
}

func createAuthMigrationDatabase(t *testing.T, ctx context.Context, resources integrationResources, version int64) string {
	t.Helper()
	database := authMigrationDatabaseName(version)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		command := exec.CommandContext(ctx, "docker", "exec", resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "postgres", "-c", "CREATE DATABASE "+database)
		command.Dir = resources.root
		output, err := command.CombinedOutput()
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("create migration database %s: %v\n%s", database, ctx.Err(), output)
		case <-deadline.C:
			t.Fatalf("create migration database %s: %v\n%s", database, err, output)
		case <-ticker.C:
		}
	}
	return migrationDatabaseURL(t, resources.databaseURL, database)
}

func dropAuthMigrationDatabase(t *testing.T, resources integrationResources, version int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database := authMigrationDatabaseName(version)
	command := exec.CommandContext(ctx, "docker", "exec", resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "postgres", "-c", "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	command.Dir = resources.root
	if output, err := command.CombinedOutput(); err != nil && ctx.Err() == nil {
		t.Errorf("drop migration database %s: %v\n%s", database, err, output)
	}
}

func authMigrationDatabaseName(version int64) string {
	return fmt.Sprintf("auth_migration_%d", version)
}

func migrationDatabaseURL(t *testing.T, databaseURL, database string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func gooseUpTo(t *testing.T, ctx context.Context, root, databaseURL string, version int64) {
	t.Helper()
	runCommand(t, ctx, root, "go", "run", "github.com/pressly/goose/v3/cmd/goose@v3.27.1", "-dir", "migrations/sql", "postgres", databaseURL, "up-to", fmt.Sprint(version))
}

func gooseDown(t *testing.T, ctx context.Context, root, databaseURL string) (string, error) {
	t.Helper()
	command := exec.CommandContext(ctx, "go", "run", "github.com/pressly/goose/v3/cmd/goose@v3.27.1", "-dir", "migrations/sql", "postgres", databaseURL, "down")
	command.Dir = root
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	return string(output), err
}

func seedExternalAuthMigrationData(t *testing.T, ctx context.Context, pool *pgxpool.Pool, testCase externalAuthMigrationCase) {
	t.Helper()
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email: "breakglass@example.test", Username: "breakglass", Password: breakGlassPassword, Role: "admin",
	})
	if err != nil {
		t.Fatalf("create bcrypt break-glass user: %v", err)
	}
	var installationID int64
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ('external-auth-regression', '1.0.0', '/plugin') RETURNING id`).Scan(&installationID); err != nil {
		t.Fatalf("create plugin installation: %v", err)
	}
	if testCase.hasGroupMappings {
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, 'external-auth', true, 'external_groups_v1')`, installationID); err != nil {
			t.Fatalf("create plugin auth binding: %v", err)
		}
	} else if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'external-auth', true)`, installationID); err != nil {
		t.Fatalf("create plugin auth binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'external-subject', $2)`, installationID, user.ID); err != nil {
		t.Fatalf("create plugin auth identity: %v", err)
	}
	if testCase.hasGroupMappings {
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_group_mappings (plugin_installation_id, external_group_id, target_role) VALUES ($1, 'external-group', 'admin')`, installationID); err != nil {
			t.Fatalf("create external group mapping: %v", err)
		}
	}
	if testCase.hasAuthorizationLog {
		if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) VALUES ($1, $2, 'external-auth')`, user.ID, installationID); err != nil {
			t.Fatalf("create external authorization state: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'external-auth', 'user', 'admin', ARRAY['external-group'], 'group_match', $3)`, user.ID, installationID, uuid.New()); err != nil {
			t.Fatalf("create external authorization audit: %v", err)
		}
	}
}

func authenticateBreakGlassUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	users := auth.NewUserRepository(pool)
	provider := auth.NewLocalProvider(users, auth.NewSessionRepository(pool))
	user, err := provider.Authenticate(ctx, auth.Credentials{Username: "breakglass", Password: breakGlassPassword})
	if err != nil {
		t.Fatalf("authenticate bcrypt break-glass user through local provider: %v", err)
	}
	if user.Username != "breakglass" || user.Email != "breakglass@example.test" || user.Role != "admin" {
		t.Fatalf("authenticated local identity = %+v", user)
	}
}

func snapshotExternalAuthMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, testCase externalAuthMigrationCase) externalAuthMigrationSnapshot {
	t.Helper()
	tables := []string{"users", "plugin_installations", "plugin_auth_bindings", "plugin_auth_identities"}
	if testCase.hasGroupMappings {
		tables = append(tables, "plugin_auth_group_mappings")
	}
	if testCase.hasAuthorizationLog {
		tables = append(tables, "external_authorization_states", "external_authorization_audit")
	}
	var snapshot externalAuthMigrationSnapshot
	if err := pool.QueryRow(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied`).Scan(&snapshot.version); err != nil {
		t.Fatalf("read current migration version: %v", err)
	}
	snapshot.rows = snapshotRows(t, ctx, pool, tables)
	snapshot.constraints = snapshotQuery(t, ctx, pool, `SELECT COALESCE(jsonb_agg(jsonb_build_object('table', conrelid::regclass::text, 'name', conname, 'definition', pg_get_constraintdef(oid)) ORDER BY conrelid::regclass::text, conname)::text, '[]') FROM pg_constraint WHERE conrelid::regclass::text = ANY($1)`, tables)
	snapshot.schema = snapshotQuery(t, ctx, pool, `SELECT COALESCE(jsonb_agg(jsonb_build_object('table', table_name, 'column', column_name, 'type', data_type, 'nullable', is_nullable, 'default', column_default) ORDER BY table_name, ordinal_position)::text, '[]') FROM information_schema.columns WHERE table_schema = 'public' AND table_name = ANY($1)`, tables)
	return snapshot
}

func snapshotRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tables []string) string {
	t.Helper()
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		var rows string
		query := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(row_data) ORDER BY to_jsonb(row_data)::text)::text, '[]') FROM (SELECT * FROM public.%s) AS row_data`, table)
		if err := pool.QueryRow(ctx, query).Scan(&rows); err != nil {
			t.Fatalf("snapshot rows for %s: %v", table, err)
		}
		parts = append(parts, table+":"+rows)
	}
	return strings.Join(parts, "\n")
}

func snapshotQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, tables []string) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(ctx, query, tables).Scan(&snapshot); err != nil {
		t.Fatalf("snapshot schema: %v", err)
	}
	return snapshot
}
