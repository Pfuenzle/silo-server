package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type identityOwnershipConflict interface {
	IdentityOwnershipConflict()
}

func TestPluginProvider_OwnershipConflict(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	ownerID := insertPluginProviderTestUser(t, ctx, pool, "owner")
	challengerID := insertPluginProviderTestUser(t, ctx, pool, "challenger")
	const subject = "immutable-subject"
	_, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3)`, installationID, subject, ownerID)
	if err != nil {
		t.Fatalf("seed plugin identity: %v", err)
	}
	var originalCreatedAt, originalUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT created_at, updated_at
		FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2`, installationID, subject,
	).Scan(&originalCreatedAt, &originalUpdatedAt); err != nil {
		t.Fatalf("load identity timestamp: %v", err)
	}
	provider := newPluginProviderForIdentityTest(pool, identityTestProviderInput{
		installationID: installationID,
		displayName:    "unused",
		email:          "unused@example.invalid",
	})

	// When
	err = provider.claimIdentity(ctx, subject, challengerID)

	// Then
	if err == nil {
		t.Fatal("claimIdentity() error = nil, want ownership conflict")
	}
	var conflict identityOwnershipConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("claimIdentity() error = %v, want typed ownership conflict", err)
	}
	var (
		ownerAfter     int
		createdAtAfter time.Time
		updatedAtAfter time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT user_id, created_at, updated_at
		FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2`, installationID, subject,
	).Scan(&ownerAfter, &createdAtAfter, &updatedAtAfter); err != nil {
		t.Fatalf("load identity after conflict: %v", err)
	}
	if ownerAfter != ownerID {
		t.Fatalf("identity owner = %d, want original owner %d", ownerAfter, ownerID)
	}
	if !updatedAtAfter.Equal(originalUpdatedAt) {
		t.Fatalf("identity updated_at = %s, want unchanged %s", updatedAtAfter, originalUpdatedAt)
	}
	if !createdAtAfter.Equal(originalCreatedAt) {
		t.Fatalf("identity created_at = %s, want unchanged %s", createdAtAfter, originalCreatedAt)
	}
	var users, profiles int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users WHERE id IN ($1, $2)),
			(SELECT COUNT(*) FROM user_profiles WHERE user_id IN ($1, $2))`, ownerID, challengerID,
	).Scan(&users, &profiles); err != nil {
		t.Fatalf("count conflict rows: %v", err)
	}
	if users != 2 || profiles != 0 {
		t.Fatalf("conflict rows = users:%d profiles:%d, want 2:0", users, profiles)
	}
}

func TestPluginProvider_ConcurrentFirstLogin(t *testing.T) {
	// Given
	fixtureCtx, pool := newPluginProviderDBTest(t)
	ctx, cancel := context.WithTimeout(fixtureCtx, 60*time.Second)
	defer cancel()
	installationID := insertPluginProviderTestInstallation(t, fixtureCtx, pool)
	const capabilityID = "concurrent-first-login"
	if _, err := pool.Exec(fixtureCtx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled binding: %v", err)
	}
	if _, err := lookupPluginIdentity(fixtureCtx, pool, PluginIdentityKey{InstallationID: installationID, ExternalSubject: "concurrent-subject"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preliminary identity lookup error = %v, want ErrNotFound", err)
	}
	// User ID captured from results after goroutines; cleanup registered now.
	var autoProvisionedUserID int
	t.Cleanup(func() {
		if autoProvisionedUserID == 0 {
			t.Fatalf("concurrent first-login: autoProvisionedUserID was never captured")
		}
		cleanupPluginProviderUser(t, fixtureCtx, pool, autoProvisionedUserID)
	})
	secondPool := newPluginProviderAdditionalPool(t, fixtureCtx)
	var firstPID, secondPID int
	if err := pool.QueryRow(fixtureCtx, `SELECT pg_backend_pid()`).Scan(&firstPID); err != nil {
		t.Fatalf("load first pool backend PID: %v", err)
	}
	if err := secondPool.QueryRow(fixtureCtx, `SELECT pg_backend_pid()`).Scan(&secondPID); err != nil {
		t.Fatalf("load second pool backend PID: %v", err)
	}
	if firstPID == secondPID {
		t.Fatalf("pool backend PIDs = %d and %d, want distinct connections", firstPID, secondPID)
	}
	providers := make([]*PluginProvider, 0, 20)
	for attempt := range 20 {
		identityPool := pool
		if attempt%2 == 1 {
			identityPool = secondPool
		}
		providers = append(providers, NewPluginProviderWithClientFactory(
			PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(identityPool)},
			NewSessionRepository(identityPool),
			NewUserRepository(identityPool),
			identityPool,
			func(context.Context) (pluginAuthClient, error) {
				return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: "concurrent-subject", DisplayName: "concurrent", Email: fmt.Sprintf("concurrent-%d@example.invalid", installationID)}}, nil
			},
		))
	}
	start := make(chan struct{})
	type loginResult struct {
		userID    int
		sessionID string
		err       error
	}
	results := make(chan loginResult, len(providers))
	var workers sync.WaitGroup
	workers.Add(len(providers))
	for _, provider := range providers {
		go func(provider *PluginProvider) {
			defer workers.Done()
			<-start
			key, keyErr := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if keyErr != nil {
				results <- loginResult{err: keyErr}
				return
			}
			sessionID := uuid.NewString()
			user, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: "ignored", Password: "ignored"}, models.AuthSession{ID: sessionID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
			if err != nil {
				results <- loginResult{err: err}
				return
			}
			results <- loginResult{userID: user.ID, sessionID: sessionID}
		}(provider)
	}
	close(start)
	workers.Wait()
	close(results)

	// When
	userIDs := make([]int, 0, len(providers))
	sessionIDs := make(map[string]struct{}, len(providers))
	for result := range results {
		if result.err != nil {
			t.Fatalf("AuthenticateAndComplete() error: %v", result.err)
		}
		userIDs = append(userIDs, result.userID)
		sessionIDs[result.sessionID] = struct{}{}
	}

	// Then
	if len(userIDs) != len(providers) {
		t.Fatalf("concurrent Authenticate() results = %d, want %d", len(userIDs), len(providers))
	}
	for _, userID := range userIDs[1:] {
		if userID != userIDs[0] {
			t.Fatalf("concurrent Authenticate() user IDs = %v, want one owner", userIDs)
		}
	}
	if len(userIDs) == 0 {
		t.Fatalf("concurrent Authenticate() user IDs = %v, want one owner", userIDs)
	}
	autoProvisionedUserID = userIDs[0]
	if len(sessionIDs) != len(providers) {
		t.Fatalf("provider sessions = %d unique IDs, want %d", len(sessionIDs), len(providers))
	}
	providerKey, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
	}
	var identityCount, accountCount, profileCount, primaryProfileCount, sessionCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'concurrent-subject'),
			(SELECT COUNT(*) FROM users WHERE id IN (SELECT user_id FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'concurrent-subject')),
			(SELECT COUNT(*) FROM user_profiles WHERE user_id IN (
				SELECT user_id FROM plugin_auth_identities
				WHERE plugin_installation_id = $1 AND external_subject = 'concurrent-subject'
			)),
			(SELECT COUNT(*) FROM user_profiles WHERE is_primary AND user_id IN (
				SELECT user_id FROM plugin_auth_identities
				WHERE plugin_installation_id = $1 AND external_subject = 'concurrent-subject'
			)),
			(SELECT COUNT(*) FROM auth_sessions WHERE provider_key = $2)`, installationID, providerKey.String(),
	).Scan(&identityCount, &accountCount, &profileCount, &primaryProfileCount, &sessionCount); err != nil {
		t.Fatalf("count concurrent login rows: %v", err)
	}
	if identityCount != 1 || accountCount != 1 || profileCount != 1 || primaryProfileCount != 1 || sessionCount != len(providers) {
		t.Fatalf("concurrent first-login rows = identity:%d account:%d profile:%d primary:%d sessions:%d, want 1:1:1:1:%d", identityCount, accountCount, profileCount, primaryProfileCount, sessionCount, len(providers))
	}
	t.Logf("SQL row counts after 20 concurrent attempts: identity=%d account=%d profile=%d primary=%d", identityCount, accountCount, profileCount, primaryProfileCount)
}

func TestPluginProvider_ConcurrentFirstLoginsForDifferentSubjects(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	const capabilityID = "different-subject-race"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, $2, true, true, 'none')`, installationID, capabilityID); err != nil {
		t.Fatalf("seed enabled binding: %v", err)
	}
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error: %v", err)
	}
	type diffSubjectResult struct {
		userID int
		err    error
	}
	results := make(chan diffSubjectResult, 2)
	start := make(chan struct{})
	subjects := []string{fmt.Sprintf("different-subject-a-%d", installationID), fmt.Sprintf("different-subject-b-%d", installationID)}
	for _, subject := range subjects {
		provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)}, NewSessionRepository(pool), NewUserRepository(pool), pool, func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{ExternalSubject: subject, Email: subject + "@example.invalid"}}, nil
		})
		go func(provider *PluginProvider) {
			<-start
			user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
			results <- diffSubjectResult{userID: user.ID, err: err}
		}(provider)
	}

	// When
	close(start)
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent different-subject login: %v", r.err)
		}
		uid := r.userID
		t.Cleanup(func() { cleanupPluginProviderUser(t, ctx, pool, uid) })
	}

	// Then
	var identities, users, profiles, sessions int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject IN ($2, $3)),
		(SELECT COUNT(*) FROM users WHERE email IN ($2 || '@example.invalid', $3 || '@example.invalid')),
		(SELECT COUNT(*) FROM user_profiles WHERE user_id IN (SELECT user_id FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject IN ($2, $3)) AND is_primary),
		(SELECT COUNT(*) FROM auth_sessions WHERE provider_key = $4)`, installationID, subjects[0], subjects[1], key.String()).Scan(&identities, &users, &profiles, &sessions); err != nil {
		t.Fatalf("count different-subject rows: %v", err)
	}
	if identities != 2 || users != 2 || profiles != 2 || sessions != 2 {
		t.Fatalf("different-subject rows = identities:%d users:%d profiles:%d sessions:%d, want 2:2:2:2", identities, users, profiles, sessions)
	}
}

type identityTestProviderInput struct {
	installationID int
	displayName    string
	email          string
}

func newPluginProviderForIdentityTest(pool *pgxpool.Pool, input identityTestProviderInput) *PluginProvider {
	return NewPluginProviderWithClientFactory(
		PluginProviderConfig{
			InstallationID: input.installationID,
			AutoProvision:  true,
			StoreProvider:  pgstore.NewPostgresProvider(pool),
		},
		nil,
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: "concurrent-subject",
				DisplayName:     input.displayName,
				Email:           input.email,
			}}, nil
		},
	)
}

func newPluginProviderAdditionalPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect second test database pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
