package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type canonicalizationAccounts struct {
	sourceID, targetID int
	sourceProfile      string
}

func seedCanonicalizationAccounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) canonicalizationAccounts {
	t.Helper()
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	targetID := insertPluginProviderTestUser(t, ctx, pool, label+"-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, label+"-source")
	sourceProfile := fmt.Sprintf("%s-source-profile-%d", label, sourceID)
	targetProfile := fmt.Sprintf("%s-target-profile-%d", label, targetID)
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, sourceID); err != nil {
		t.Fatalf("make source provider-only: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Source', true), ($3, $4, 'Target', true)`, sourceProfile, sourceID, targetProfile, targetID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, "canonical-"+label, sourceID); err != nil {
		t.Fatalf("seed source identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization', true, true, 'none')`, installationID); err != nil {
		t.Fatalf("seed canonicalization binding: %v", err)
	}
	return canonicalizationAccounts{sourceID: sourceID, targetID: targetID, sourceProfile: sourceProfile}
}

func canonicalizationService(pool *pgxpool.Pool, provider userstore.UserStoreProvider) *ExternalAccountCanonicalizer {
	return NewExternalAccountCanonicalizer(pool, []byte("canonicalization-store-credentials-key"), time.Now).WithStoreProvider(provider)
}

func previewAndExecuteCanonicalization(t *testing.T, ctx context.Context, service *ExternalAccountCanonicalizer, accounts canonicalizationAccounts) error {
	t.Helper()
	preview, err := service.Preview(ctx, CanonicalizationOperator{UserID: accounts.targetID, IsAdmin: true}, accounts.sourceID, accounts.targetID)
	if err != nil {
		t.Fatalf("preview canonicalization: %v", err)
	}
	_, err = service.Execute(ctx, CanonicalizationOperator{UserID: accounts.targetID, IsAdmin: true}, preview.Token)
	return err
}

func cleanupCanonicalizationAudits(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `TRUNCATE external_authorization_audit, external_identity_link_audit CASCADE`); err != nil {
			t.Errorf("cleanup canonicalization audits: %v", err)
		}
	})
}

func TestExternalAccountCanonicalization_revokesSourceCredentialsAndPreservesTargetLocalCredentials(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	accounts := seedCanonicalizationAccounts(t, ctx, pool, "credentials")
	cleanupCanonicalizationAudits(t, ctx, pool)
	sourceSessionID := "canonical-source-" + uuid.NewString()
	targetSessionID := "canonical-target-" + uuid.NewString()
	sourceOAuthState := "canonical-oauth-source-" + uuid.NewString()
	seedCredentialRows(t, ctx, pool, accounts, sourceSessionID, targetSessionID, sourceOAuthState)
	service := canonicalizationService(pool, pgstore.NewPostgresProvider(pool))

	// When
	err := previewAndExecuteCanonicalization(t, ctx, service, accounts)

	// Then
	if err != nil {
		t.Fatalf("execute canonicalization: %v", err)
	}
	validTarget, err := NewSessionRepository(pool).IsValid(ctx, targetSessionID)
	if err != nil {
		t.Fatalf("validate target interactive session: %v", err)
	}
	var sourceAPIKeys, targetAPIKeys, sourceABS, sourceJelly, sourcePlayback, sourceDevice, sourceOAuth int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM api_keys WHERE user_id = $1), (SELECT COUNT(*) FROM api_keys WHERE user_id = $2), (SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1), (SELECT COUNT(*) FROM jellycompat_sessions WHERE streamapp_user_id = $1), (SELECT COUNT(*) FROM jellycompat_playback_sessions WHERE user_id = $1::text), (SELECT COUNT(*) FROM device_login_requests WHERE approved_by_user_id = $1 OR auth_session_id = $3), (SELECT COUNT(*) FROM oauth_session WHERE state = $4)`, accounts.sourceID, accounts.targetID, sourceSessionID, sourceOAuthState).Scan(&sourceAPIKeys, &targetAPIKeys, &sourceABS, &sourceJelly, &sourcePlayback, &sourceDevice, &sourceOAuth); err != nil {
		t.Fatalf("inspect canonicalized credentials: %v", err)
	}
	if validTarget || sourceAPIKeys != 0 || targetAPIKeys != 1 || sourceABS != 0 || sourceJelly != 0 || sourcePlayback != 0 || sourceDevice != 0 || sourceOAuth != 0 {
		t.Fatalf("credential cleanup = target_session_valid:%t source_api:%d target_api:%d source_abs:%d source_jelly:%d source_playback:%d source_device:%d source_oauth:%d; want false,0,1,0,0,0,0,0", validTarget, sourceAPIKeys, targetAPIKeys, sourceABS, sourceJelly, sourcePlayback, sourceDevice, sourceOAuth)
	}
}

func TestExternalAccountCanonicalization_blocksUnknownUserStoreWithoutMutating(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	accounts := seedCanonicalizationAccounts(t, ctx, pool, "unknown-store")
	cleanupCanonicalizationAudits(t, ctx, pool)
	service := canonicalizationService(pool, nil)

	// When
	err := previewAndExecuteCanonicalization(t, ctx, service, accounts)

	// Then
	if !errors.Is(err, ErrCanonicalizationBlocked) {
		t.Fatalf("execute error = %v, want blocked", err)
	}
	var sourceUsers int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, accounts.sourceID).Scan(&sourceUsers); err != nil {
		t.Fatalf("count source users: %v", err)
	}
	if sourceUsers != 1 {
		t.Fatalf("source users = %d, want 1", sourceUsers)
	}
}

func TestExternalAccountCanonicalization_blocksEverySQLiteStoreStateWithoutMutating(t *testing.T) {
	for _, storeFile := range []string{"absent", "db", "wal", "shm"} {
		t.Run(storeFile, func(t *testing.T) {
			// Given
			ctx, pool := newPluginProviderDBTest(t)
			accounts := seedCanonicalizationAccounts(t, ctx, pool, "sqlite-"+storeFile)
			cleanupCanonicalizationAudits(t, ctx, pool)
			dataDir := t.TempDir()
			provider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: dataDir}))
			t.Cleanup(func() { _ = provider.Close() })
			if storeFile != "absent" {
				path := filepath.Join(dataDir, fmt.Sprintf("%d.db", accounts.sourceID))
				if storeFile != "db" {
					path += "-" + storeFile
				}
				if err := os.WriteFile(path, []byte("temporary SQLite state"), 0o600); err != nil {
					t.Fatalf("materialize %s store state: %v", storeFile, err)
				}
			}
			service := canonicalizationService(pool, provider)

			// When
			err := previewAndExecuteCanonicalization(t, ctx, service, accounts)

			// Then
			if !errors.Is(err, ErrCanonicalizationBlocked) {
				t.Fatalf("execute error = %v, want blocked for SQLite %s", err, storeFile)
			}
			var sourceUsers int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, accounts.sourceID).Scan(&sourceUsers); err != nil {
				t.Fatalf("count source users: %v", err)
			}
			if sourceUsers != 1 {
				t.Fatalf("source users = %d, want 1", sourceUsers)
			}
		})
	}
}

type interruptedSQLiteCleanupProvider struct {
	*userdb.SQLiteProvider
	path string
}

func (p interruptedSQLiteCleanupProvider) DeleteUser(ctx context.Context, userID int) error {
	if err := os.WriteFile(p.path, []byte("created during interrupted cleanup"), 0o600); err != nil {
		return err
	}
	if err := p.SQLiteProvider.DeleteUser(ctx, userID); err != nil {
		return err
	}
	return context.Canceled
}

func TestExternalAccountCanonicalization_neverStartsSQLiteCleanup(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	accounts := seedCanonicalizationAccounts(t, ctx, pool, "sqlite-interrupted-cleanup")
	cleanupCanonicalizationAudits(t, ctx, pool)
	dataDir := t.TempDir()
	sqliteProvider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: dataDir}))
	t.Cleanup(func() { _ = sqliteProvider.Close() })
	path := filepath.Join(dataDir, fmt.Sprintf("%d.db", accounts.sourceID))
	service := canonicalizationService(pool, interruptedSQLiteCleanupProvider{SQLiteProvider: sqliteProvider, path: path})

	// When
	err := previewAndExecuteCanonicalization(t, ctx, service, accounts)

	// Then
	if !errors.Is(err, ErrCanonicalizationBlocked) {
		t.Fatalf("execute error = %v, want blocked SQLite provider", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SQLite cleanup started despite blocked canonicalization: %v", err)
	}
	var sourceUsers int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = $1`, accounts.sourceID).Scan(&sourceUsers); err != nil {
		t.Fatalf("count source users: %v", err)
	}
	if sourceUsers != 1 {
		t.Fatalf("source users = %d after cleanup interruption, want 1", sourceUsers)
	}
}

func seedCredentialRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accounts canonicalizationAccounts, sourceSessionID, targetSessionID, sourceOAuthState string) {
	t.Helper()
	queries := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO auth_sessions (id, user_id, device_name, expires_at) VALUES ($1, $2, 'source', NOW() + INTERVAL '1 hour'), ($3, $4, 'target', NOW() + INTERVAL '1 hour')`, []any{sourceSessionID, accounts.sourceID, targetSessionID, accounts.targetID}},
		{`INSERT INTO api_keys (user_id, label, api_key) VALUES ($1, 'source', $2), ($3, 'target', $4)`, []any{accounts.sourceID, "source-api-" + uuid.NewString(), accounts.targetID, "target-api-" + uuid.NewString()}},
		{`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'source-device')`, []any{accounts.sourceID, "source-abs-" + uuid.NewString()}},
		{`INSERT INTO jellycompat_sessions (token, username, account_username, profile_id, profile_name, pseudo_user_id, streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at) VALUES ($1, 'source', 'source', $2, 'Source', $3, $4, 'access', 'refresh', NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 hour')`, []any{"source-jelly-" + uuid.NewString(), accounts.sourceProfile, uuid.NewString(), accounts.sourceID}},
		{`INSERT INTO jellycompat_playback_sessions (id, compat_token, user_id, data, expires_at) VALUES ($1, 'compat', $2, '{}'::jsonb, NOW() + INTERVAL '1 hour')`, []any{"source-playback-" + uuid.NewString(), strconv.Itoa(accounts.sourceID)}},
		{`INSERT INTO device_login_requests (id, device_code_hash, browser_code_hash, user_code_hash, match_code, device_name, approved_by_user_id, auth_session_id, status, expires_at, approved_at) VALUES ($1, $2, $3, $4, 'match', 'source-device', $5, $6, 'approved', NOW() + INTERVAL '1 hour', NOW())`, []any{uuid.NewString(), "device-" + uuid.NewString(), "browser-" + uuid.NewString(), "user-" + uuid.NewString(), accounts.sourceID, sourceSessionID}},
		{`INSERT INTO oauth_session (state, install_id, redirect_uri, linking_user_id, expires_at) VALUES ($1, 'install', 'https://example.invalid/callback', $2, NOW() + INTERVAL '1 hour')`, []any{sourceOAuthState, strconv.Itoa(accounts.sourceID)}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.query, query.args...); err != nil {
			t.Fatalf("seed credentials: %v", err)
		}
	}
}
