package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5"
)

type failingProfileStoreProvider struct {
	inner userstore.UserStoreProvider
}

func (p failingProfileStoreProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	store, err := p.inner.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return failingProfileStore{UserStore: store}, nil
}

func (p failingProfileStoreProvider) Close() error { return p.inner.Close() }

func (p failingProfileStoreProvider) SupportsTransactionalProvisioning() bool {
	provider, ok := p.inner.(userstore.TransactionalProvisioningProvider)
	return ok && provider.SupportsTransactionalProvisioning()
}

type failingProfileStore struct{ userstore.UserStore }

func (failingProfileStore) CreateProfile(context.Context, userstore.Profile) error {
	return errors.New("profile storage unavailable")
}

func TestPluginProvider_RejectsFirstExternalProvisioningOnSQLite(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	label := fmt.Sprintf("sqlite-profile-%d", time.Now().UnixNano())
	email := label + "@example.invalid"
	subject := label + "-subject"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email); err != nil {
			t.Fatalf("cleanup SQLite rejected user: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, 'oidc', true, 'none')`, installationID); err != nil {
		t.Fatalf("seed OIDC binding: %v", err)
	}
	storeProvider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = storeProvider.Close() })
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{
			InstallationID: installationID,
			CapabilityID:   "oidc",
			AuthMode:       "oauth",
			AutoProvision:  true,
			StoreProvider:  storeProvider,
		},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: subject,
				DisplayName:     "Living Room",
				Email:           email,
			}}, nil
		},
	)

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
	var users, identities int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE email = $2),
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = $3)`, installationID, email, subject,
	).Scan(&users, &identities); err != nil {
		t.Fatalf("count rejected SQLite provisioning rows: %v", err)
	}
	if users != 0 || identities != 0 {
		t.Fatalf("SQLite first provisioning residue = users:%d identities:%d, want 0:0", users, identities)
	}
}

func TestPluginProvider_ExistingExternalIdentityOnSQLiteStillLogsIn(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "sqlite-existing")
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, authorization_mode) VALUES ($1, 'oidc', true, 'none')`, installationID); err != nil {
		t.Fatalf("seed OIDC binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'sqlite-existing-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed external identity: %v", err)
	}
	storeProvider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = storeProvider.Close() })
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, CapabilityID: "oidc", AuthMode: "oauth", AutoProvision: true, StoreProvider: storeProvider},
		NewSessionRepository(pool),
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "sqlite-existing-subject"}}, nil
		},
	)

	// When
	user, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	if user.ID != userID {
		t.Fatalf("Authenticate() user ID = %d, want %d", user.ID, userID)
	}
}

func TestPluginProvider_UsesSyntheticEmailOnCollision(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	localUser, err := NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email:    "local@example.invalid",
		Username: "local-existing-user",
		Password: "local-password",
		Role:     "user",
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	localBefore := *localUser
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, localUser.ID)
	})
	provider := newPluginProviderForIdentityTest(pool, identityTestProviderInput{
		installationID: installationID,
		displayName:    "External User",
		email:          "local@example.invalid",
	})

	// When
	user, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	if user.ID == localUser.ID {
		t.Fatal("Authenticate() linked to the existing local account")
	}
	if user.Email != syntheticPluginEmail(installationID, "concurrent-subject") {
		t.Fatalf("provisioned email = %q, want synthetic address", user.Email)
	}
	if user.LocalPasswordLoginEnabled {
		t.Fatal("provisioned account enabled local-password login")
	}
	localAfter, err := NewUserRepository(pool).GetByID(ctx, localUser.ID)
	if err != nil {
		t.Fatalf("reload local user: %v", err)
	}
	if !reflect.DeepEqual(*localAfter, localBefore) {
		t.Fatalf("local user changed during collision provisioning: before=%#v after=%#v", localBefore, *localAfter)
	}
	var profiles int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_profiles WHERE user_id = $1`, user.ID).Scan(&profiles); err != nil {
		t.Fatalf("count synthetic-account profiles: %v", err)
	}
	if profiles != 1 {
		t.Fatalf("synthetic account profiles = %d, want 1", profiles)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})
}

func TestPluginProvider_RollsBackProvisionFailure(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{
			InstallationID: installationID,
			AutoProvision:  true,
			StoreProvider:  failingProfileStoreProvider{inner: pgstore.NewPostgresProvider(pool)},
		},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: "profile-failure-subject",
				DisplayName:     "Failure",
				Email:           "profile-failure@example.invalid",
			}}, nil
		},
	)

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if err == nil {
		t.Fatal("Authenticate() error = nil, want profile failure")
	}
	var users, identities, profiles int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users WHERE email = 'profile-failure@example.invalid'),
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'profile-failure-subject'),
			(SELECT COUNT(*) FROM user_profiles WHERE name = 'failure')`, installationID,
	).Scan(&users, &identities, &profiles); err != nil {
		t.Fatalf("count provision failure rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 {
		t.Fatalf("provision failure rows = users:%d identities:%d profiles:%d, want 0:0:0", users, identities, profiles)
	}
}

func TestPluginProvider_CompensatesCreatedAccountWhenCommitFails(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: "commit-failure-subject",
				DisplayName:     "Commit Failure",
				Email:           "commit-failure@example.invalid",
			}}, nil
		},
	)
	commitErr := errors.New("injected commit failure")
	provider.identities.commit = func(context.Context, pgx.Tx) error { return commitErr }

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if !errors.Is(err, commitErr) {
		t.Fatalf("Authenticate() error = %v, want commit cause", err)
	}
	var users, identities, profiles int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users WHERE email = 'commit-failure@example.invalid'),
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'commit-failure-subject'),
			(SELECT COUNT(*) FROM user_profiles WHERE name = 'commit_failure')`, installationID,
	).Scan(&users, &identities, &profiles); err != nil {
		t.Fatalf("count commit failure rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 {
		t.Fatalf("commit failure rows = users:%d identities:%d profiles:%d, want 0:0:0", users, identities, profiles)
	}
}

func TestPluginProvider_CompensatesCreatedAccountWhenClaimFails(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{InstallationID: installationID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: "claim-failure-subject",
				DisplayName:     "Claim Failure",
				Email:           "claim-failure@example.invalid",
			}}, nil
		},
	)
	claimErr := errors.New("injected claim failure")
	provider.identities.claim = func(context.Context, pgx.Tx, PluginIdentityKey, int) error { return claimErr }

	// When
	_, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

	// Then
	if !errors.Is(err, claimErr) {
		t.Fatalf("Authenticate() error = %v, want claim cause", err)
	}
	var users, identities, profiles int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users WHERE email = 'claim-failure@example.invalid'),
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'claim-failure-subject'),
			(SELECT COUNT(*) FROM user_profiles WHERE name = 'claim_failure')`, installationID,
	).Scan(&users, &identities, &profiles); err != nil {
		t.Fatalf("count claim failure rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 {
		t.Fatalf("claim failure rows = users:%d identities:%d profiles:%d, want 0:0:0", users, identities, profiles)
	}
}
