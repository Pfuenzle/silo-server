package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestPluginProvider_BoundProvisioningRetriesWholeTransaction(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "direct serialization", err: &pgconn.PgError{Code: "40001"}},
		{name: "wrapped deadlock", err: fmt.Errorf("retryable: %w", &pgconn.PgError{Code: "40P01"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx, pool := newPluginProviderDBTest(t)
			installationID := insertPluginProviderTestInstallation(t, ctx, pool)
			capabilityID := "retry-" + test.name
			email := fmt.Sprintf("retry-%d-%s@example.invalid", installationID, test.name[:6])
			if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
				t.Fatalf("seed enabled binding: %v", err)
			}
			subject := fmt.Sprintf("retry-subject-%d", installationID)
			provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)}, nil, NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
				return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject, DisplayName: subject, Email: email}}, nil
			})
			calls := 0
			provider.identities.runTransaction = func(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
				calls++
				if calls == 1 {
					return test.err
				}
				provider.identities.runTransaction = nil
				err := provider.identities.withTransaction(ctx, fn)
				return err
			}

			// When
			user, err := provider.Authenticate(ctx, Credentials{Username: "ignored", Password: "ignored"})

			// Then
			if err != nil {
				t.Fatalf("Authenticate() error: %v", err)
			}
			t.Cleanup(func() { cleanupPluginProviderUser(t, context.Background(), pool, user.ID) })
			if calls != 2 {
				t.Fatalf("transaction attempts = %d, want 2", calls)
			}
			var users, identities, profiles int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT COUNT(*) FROM users WHERE id = $1),
				(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $2 AND external_subject = $3),
				(SELECT COUNT(*) FROM user_profiles WHERE user_id = $1 AND is_primary)`, user.ID, installationID, subject).Scan(&users, &identities, &profiles); err != nil {
				t.Fatalf("count retried provisioning rows: %v", err)
			}
			if users != 1 || identities != 1 || profiles != 1 {
				t.Fatalf("retried provisioning rows = users:%d identities:%d profiles:%d, want 1:1:1", users, identities, profiles)
			}
		})
	}
}

func TestPluginProvider_BoundProvisioningCommitErrorRollsBack(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	const capabilityID = "commit-error"
	email := fmt.Sprintf("commit-error-%d@example.invalid", installationID)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled binding: %v", err)
	}
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)}, nil, NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
		return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "commit-error", Email: email}}, nil
	})
	commitErr := errors.New("injected commit error")
	provider.identities.commit = func(context.Context, pgx.Tx) error { return commitErr }

	// When
	_, err := provider.Authenticate(ctx, Credentials{})

	// Then
	if !errors.Is(err, commitErr) {
		t.Fatalf("Authenticate() error = %v, want commit error", err)
	}
	var users, identities, profiles int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE email = $1),
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $2 AND external_subject = 'commit-error'),
		(SELECT COUNT(*) FROM user_profiles WHERE user_id IN (SELECT id FROM users WHERE email = $1))`, email, installationID).Scan(&users, &identities, &profiles); err != nil {
		t.Fatalf("count commit rollback rows: %v", err)
	}
	if users != 0 || identities != 0 || profiles != 0 {
		t.Fatalf("commit rollback rows = users:%d identities:%d profiles:%d, want 0:0:0", users, identities, profiles)
	}
}
