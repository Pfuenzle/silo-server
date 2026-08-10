package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type unknownProvisioningProvider struct{}

func (unknownProvisioningProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("unused")
}
func (unknownProvisioningProvider) Close() error { return nil }

func TestPluginProvider_TransactionalProvisioningRequiresPositiveCapability(t *testing.T) {
	// Given
	sqlite := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = sqlite.Close() })
	providers := []struct {
		name     string
		provider userstore.UserStoreProvider
		want     bool
	}{
		{name: "unknown", provider: unknownProvisioningProvider{}, want: false},
		{name: "sqlite", provider: sqlite, want: false},
		{name: "wrapped sqlite", provider: failingProfileStoreProvider{inner: sqlite}, want: false},
		{name: "postgres", provider: pgstore.NewPostgresProvider(nil), want: true},
		{name: "wrapped postgres", provider: failingProfileStoreProvider{inner: pgstore.NewPostgresProvider(nil)}, want: true},
	}

	for _, test := range providers {
		t.Run(test.name, func(t *testing.T) {
			provider := &PluginProvider{config: PluginProviderConfig{StoreProvider: test.provider}}

			// When
			got := provider.supportsTransactionalProvisioning()

			// Then
			if got != test.want {
				t.Fatalf("supportsTransactionalProvisioning() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPluginProvider_CompleteEntrypointsRejectInvalidSessions(t *testing.T) {
	// Given
	provider := &PluginProvider{}

	// When
	_, credentialErr := provider.AuthenticateAndComplete(context.Background(), Credentials{}, models.AuthSession{})
	_, oauthErr := provider.CompleteOAuthAndComplete(context.Background(), &pluginv1.AuthenticateResponse{}, models.AuthSession{})

	// Then
	if !errors.Is(credentialErr, ErrInvalidCredentials) {
		t.Fatalf("AuthenticateAndComplete() error = %v, want ErrInvalidCredentials", credentialErr)
	}
	if !errors.Is(oauthErr, ErrInvalidCredentials) {
		t.Fatalf("CompleteOAuthAndComplete() error = %v, want ErrInvalidCredentials", oauthErr)
	}
}

func TestPluginProvider_BoundNoSessionEntrypointsCommitAtomicallyWithoutSessions(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	const capabilityID = "atomic-no-session"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled binding: %v", err)
	}
	credentialSubject := fmt.Sprintf("atomic-credentials-%d", installationID)
	oauthSubject := fmt.Sprintf("atomic-oauth-%d", installationID)
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: credentialSubject, DisplayName: credentialSubject, Email: credentialSubject + "@example.invalid"}}, nil
	})

	// When
	credentialUser, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	t.Cleanup(func() { cleanupPluginProviderUser(t, context.Background(), pool, credentialUser.ID) })
	oauthUser, err := provider.CompleteOAuth(ctx, &pluginv1.AuthenticateResponse{ExternalSubject: oauthSubject, DisplayName: oauthSubject, Email: oauthSubject + "@example.invalid"})
	if err != nil {
		t.Fatalf("CompleteOAuth() error: %v", err)
	}
	t.Cleanup(func() { cleanupPluginProviderUser(t, context.Background(), pool, oauthUser.ID) })

	// Then
	var identities, users, primaryProfiles, sessions int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1),
		(SELECT COUNT(*) FROM users WHERE id IN ($2, $3)),
		(SELECT COUNT(*) FROM user_profiles WHERE user_id IN ($2, $3) AND is_primary),
		(SELECT COUNT(*) FROM auth_sessions WHERE user_id IN ($2, $3))`, installationID, credentialUser.ID, oauthUser.ID).Scan(&identities, &users, &primaryProfiles, &sessions); err != nil {
		t.Fatalf("count atomic no-session rows: %v", err)
	}
	if identities != 2 || users != 2 || primaryProfiles != 2 || sessions != 0 {
		t.Fatalf("atomic no-session rows = identities:%d users:%d primary:%d sessions:%d, want 2:2:2:0", identities, users, primaryProfiles, sessions)
	}
}

func TestPluginProvider_BoundFirstProvisionSessionFailureRollsBack(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	const capabilityID = "atomic-session-rollback"
	email := fmt.Sprintf("atomic-session-rollback-%d@example.invalid", installationID)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled binding: %v", err)
	}
	trigger := fmt.Sprintf("fail_first_provision_session_%d", installationID)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'session failure'; END; $$; CREATE TRIGGER %s BEFORE INSERT ON auth_sessions FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, trigger, trigger)); err != nil {
		t.Fatalf("create session trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON auth_sessions; DROP FUNCTION IF EXISTS %s()`, trigger, trigger)); err != nil {
			t.Fatalf("drop session trigger: %v", err)
		}
	})
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "atomic-session-rollback", Email: email}}, nil
	})
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("new provider key: %v", err)
	}

	// When
	_, err = provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})

	// Then
	if err == nil {
		t.Fatal("AuthenticateAndComplete() error = nil, want session failure")
	}
	var users, identities, profiles, sessions int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE email = $1),
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $2 AND external_subject = 'atomic-session-rollback'),
		(SELECT COUNT(*) FROM user_profiles WHERE user_id IN (SELECT id FROM users WHERE email = $1)),
		(SELECT COUNT(*) FROM auth_sessions WHERE provider_key = $3)`, email, installationID, key.String()).Scan(&users, &identities, &profiles, &sessions); err != nil {
		t.Fatalf("count rollback rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 || sessions != 0 {
		t.Fatalf("first-provision session rollback rows = users:%d identities:%d profiles:%d sessions:%d, want 0:0:0:0", users, identities, profiles, sessions)
	}
}

func TestPluginProvider_TransactionalRetryHonorsRetryableErrorsAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		cancelled bool
		wantCalls int
		wantRetry bool
	}{
		{name: "direct serialization", err: &pgconn.PgError{Code: "40001"}, wantCalls: externalLoginAttempts, wantRetry: true},
		{name: "wrapped deadlock", err: fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40P01"}), wantCalls: externalLoginAttempts, wantRetry: true},
		{name: "cancelled serialization", err: &pgconn.PgError{Code: "40001"}, cancelled: true, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			if test.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			provider := &PluginProvider{config: PluginProviderConfig{CapabilityID: "retry"}, identities: &PluginIdentityRepository{runTransaction: func(context.Context, func(context.Context, pgx.Tx) error) error {
				calls++
				return test.err
			}}}

			// When
			_, err := provider.completeExternalLogin(ctx, Credentials{}, &pluginv1.AuthenticateResponse{ExternalSubject: "retry"}, nil)

			// Then
			if calls != test.wantCalls {
				t.Fatalf("transaction attempts = %d, want %d", calls, test.wantCalls)
			}
			if test.wantRetry {
				if err == nil || err.Error() != "completing external login: retry limit exceeded" {
					t.Fatalf("retry exhaustion error = %v, want retry-limit error", err)
				}
				return
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("cancelled retry error = %v, want injected cause", err)
			}
		})
	}
}
