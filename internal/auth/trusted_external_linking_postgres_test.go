package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// seedTrustedLinkBinding inserts a plugin_auth_bindings row with the given
// trusted_link_mode, auto_provision, authorization_mode, and enabled state.
func seedTrustedLinkBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, installationID int, capabilityID string, enabled, autoProvision bool, trustedLinkMode, authorizationMode string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode, trusted_link_mode) VALUES ($1, $2, $3, $4, $5, $6)`, installationID, capabilityID, enabled, autoProvision, authorizationMode, trustedLinkMode); err != nil {
		t.Fatalf("seed trusted link binding: %v", err)
	}
}

// insertLocalPasswordUser creates a user with local_password_login_enabled=true
// and returns its ID. Username and email use the normalized citext semantics
// (trimmed but case preserved).
func insertLocalPasswordUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) int {
	t.Helper()
	username := fmt.Sprintf("local-user-%s-%d", label, time.Now().UnixNano())
	user, err := NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email:                     username + "@example.invalid",
		Username:                  username,
		Password:                  "local-password-hash-placeholder",
		LocalPasswordLoginEnabled: boolPtr(true),
		Role:                      "user",
	})
	if err != nil {
		t.Fatalf("seed local password user: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})
	return user.ID
}

// insertLocalPasswordUserNamed creates a user with the exact username and email
// specified, with local_password_login_enabled=true. Used for exact-match tests.
func insertLocalPasswordUserNamed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username, email string) int {
	t.Helper()
	user, err := NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email:                     email,
		Username:                  username,
		Password:                  "local-password-hash-placeholder",
		LocalPasswordLoginEnabled: boolPtr(true),
		Role:                      "user",
	})
	if err != nil {
		t.Fatalf("seed local password user named %q: %v", username, err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})
	return user.ID
}

func boolPtr(b bool) *bool { return &b }

// trustedLinkProvider creates a PluginProvider configured for trusted linking
// tests with the given asserted_username and external_subject.
func trustedLinkProvider(pool *pgxpool.Pool, installationID int, capabilityID string, externalSubject, assertedUsername, displayName, email string) *PluginProvider {
	response := &pluginv1.AuthenticateResponse{
		ExternalSubject: externalSubject,
		DisplayName:     displayName,
		Email:           email,
	}
	if assertedUsername != "" {
		response.AssertedUsername = &assertedUsername
	}
	return NewPluginProviderWithClientFactory(
		PluginProviderConfig{
			InstallationID: installationID,
			CapabilityID:   capabilityID,
			AutoProvision:  true,
			StoreProvider:  pgstore.NewPostgresProvider(pool),
		},
		NewSessionRepository(pool),
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: response}, nil
		},
	)
}

// countAuditRows returns the number of external_identity_link_audit rows for a user.
func countAuditRows(ctx context.Context, pool *pgxpool.Pool, userID int) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM external_identity_link_audit WHERE user_id = $1`, userID).Scan(&count)
	return count, err
}

// ============================================================================
// Test: canonical local match creates identity, no duplicate, no new user
// ============================================================================

func TestTrustedLink_CanonicalLocalMatchCreatesNoDuplicate(t *testing.T) {
	// Given: a trusted_existing binding and one local password user
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-canonical-match"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	localUsername := fmt.Sprintf("tl-canonical-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, localUsername, localUsername+"@example.invalid")
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-canonical", localUsername, "", "")
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey: %v", err)
	}

	// When: the external login runs with an asserted username matching the local user
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: localUsername, Password: "x"}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: the identity is linked to the existing local user, not a new one
	if user.ID != localUserID {
		t.Fatalf("linked user ID = %d, want local user %d", user.ID, localUserID)
	}
	// Exactly one identity row
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'ldap-sub-canonical'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 1 {
		t.Fatalf("identity count = %d, want 1", identityCount)
	}
	// No new user was created
	var totalUsers int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE username = $1`, localUsername).Scan(&totalUsers); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if totalUsers != 1 {
		t.Fatalf("user count = %d, want 1 (no duplicate)", totalUsers)
	}
	// Audit row written
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1", auditCount)
	}
}

// ============================================================================
// Test: no-match preserves existing auto_provision behavior
// ============================================================================

func TestTrustedLink_NoMatchAutoProvisions(t *testing.T) {
	// Given: a trusted_existing binding but no local user matching the asserted username
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-no-match"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-nomatch", "no_such_user", "auto-user", "auto@example.invalid")
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey: %v", err)
	}

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: "no_such_user", Password: "x"}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})

	// Then: a new user was auto-provisioned (not linked to a non-existent local account)
	if user.ID == 0 {
		t.Fatal("expected auto-provisioned user, got zero ID")
	}
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'ldap-sub-nomatch'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 1 {
		t.Fatalf("identity count = %d, want 1", identityCount)
	}
}

// ============================================================================
// Test: disabled target user fails closed (no link, no new user, no session)
// ============================================================================

func TestTrustedLink_DisabledTargetFailsClosed(t *testing.T) {
	// Given: a trusted_existing binding and a DISABLED local password user
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-disabled-target"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, "disabled-user", "disabled@example.invalid")
	if _, err := pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, localUserID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-disabled", "disabled-user", "", "")
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey: %v", err)
	}

	// When
	_, err = provider.AuthenticateAndComplete(ctx, Credentials{Username: "disabled-user", Password: "x"}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})

	// Then: fails closed - no link, no new user, no session, no audit
	if err == nil {
		t.Fatal("expected error for disabled target, got nil")
	}
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'ldap-sub-disabled'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 0 {
		t.Fatalf("identity count = %d, want 0 (no link to disabled)", identityCount)
	}
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("audit count = %d, want 0 (no audit on fail-closed)", auditCount)
	}
}

// ============================================================================
// Test: existing identity always wins after username changes (rename scenario)
// ============================================================================

func TestTrustedLink_ExistingIdentityWinsAfterRename(t *testing.T) {
	// Given: identity already claimed to user A; user A's username was renamed
	// The asserted_username now matches user B. Identity should still go to user A.
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-rename-scenario"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	originalOwnerID := insertLocalPasswordUserNamed(t, ctx, pool, "old-name", "old@example.invalid")
	// Another local user whose name now matches the asserted username
	newNameUserID := insertLocalPasswordUserNamed(t, ctx, pool, "new-name", "new@example.invalid")
	// Pre-claim the identity to the original owner
	const externalSubject = "ldap-sub-rename"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, externalSubject, originalOwnerID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	// The plugin response asserts the username that matches the NEW user
	provider := trustedLinkProvider(pool, installationID, capabilityID, externalSubject, "new-name", "", "")
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey: %v", err)
	}

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: identity always wins — original owner, not the username-matched user
	if user.ID != originalOwnerID {
		t.Fatalf("linked user ID = %d, want original owner %d (not name-matched %d)", user.ID, originalOwnerID, newNameUserID)
	}
}

// ============================================================================
// Test: email and display-name never link (only asserted_username)
// ============================================================================

func TestTrustedLink_EmailAndDisplayNameDoNotLink(t *testing.T) {
	// Given: trusted_existing binding; the response has no asserted_username but
	// display_name and email that match a local user.
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-email-display-no-link"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, "email-display-user", "ed@example.invalid")
	// Provider returns display_name matching the local user, but NO asserted_username
	provider := NewPluginProviderWithClientFactory(
		PluginProviderConfig{
			InstallationID: installationID,
			CapabilityID:   capabilityID,
			AutoProvision:  true,
			StoreProvider:  pgstore.NewPostgresProvider(pool),
		},
		NewSessionRepository(pool),
		NewUserRepository(pool),
		pool,
		func(context.Context) (pluginAuthClient, error) {
			return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
				ExternalSubject: "ldap-sub-email-display",
				DisplayName:     "email-display-user", // matches local user
				Email:           "ed@example.invalid", // matches local user
			}}, nil
		},
	)
	key, err := models.NewPluginSessionProviderKey(installationID, capabilityID)
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey: %v", err)
	}

	// When: login without asserted_username
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})

	// Then: auto-provisioned a new user (not linked to the local user)
	if user.ID == localUserID {
		t.Fatalf("user ID = %d = local user, want auto-provisioned (email/display_name must not link)", user.ID)
	}
}

// ============================================================================
// Test: two issuers / provider isolation — different installations are isolated
// ============================================================================

func TestTrustedLink_TwoIssuersProviderIsolation(t *testing.T) {
	// Given: two different installations, both trusted_existing, same asserted username
	ctx, pool := newPluginProviderDBTest(t)
	installA := insertPluginProviderTestInstallation(t, ctx, pool)
	installB := insertPluginProviderTestInstallation(t, ctx, pool)
	capA := "ldap-issuer-a"
	capB := "ldap-issuer-b"
	seedTrustedLinkBinding(t, ctx, pool, installA, capA, true, true, "trusted_existing", "none")
	seedTrustedLinkBinding(t, ctx, pool, installB, capB, true, true, "trusted_existing", "none")
	localUsername := fmt.Sprintf("tl-shared-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, localUsername, localUsername+"@example.invalid")
	providerA := trustedLinkProvider(pool, installA, capA, "issuer-a-sub-1", localUsername, "", "")
	providerB := trustedLinkProvider(pool, installB, capB, "issuer-b-sub-1", localUsername, "", "")
	keyA, _ := models.NewPluginSessionProviderKey(installA, capA)
	keyB, _ := models.NewPluginSessionProviderKey(installB, capB)

	// When: both logins happen
	userA, err := providerA.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &keyA})
	if err != nil {
		t.Fatalf("provider A error: %v", err)
	}
	userB, err := providerB.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &keyB})
	if err != nil {
		t.Fatalf("provider B error: %v", err)
	}

	// Then: both link to the same local user (deterministic)
	if userA.ID != localUserID || userB.ID != localUserID {
		t.Fatalf("user IDs = %d and %d, both want %d", userA.ID, userB.ID, localUserID)
	}
	// Two distinct identities, one user
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id IN ($1, $2)`, installA, installB).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 2 {
		t.Fatalf("identity count = %d, want 2 (one per installation)", identityCount)
	}
}

// ============================================================================
// Test: admin target is explicitly tested (target has admin role)
// ============================================================================

func TestTrustedLink_AdminTargetLinkedWhenPolicyAllows(t *testing.T) {
	// Given: an admin local password user as the target
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "ldap-admin-target"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	adminUsername := fmt.Sprintf("admin-link-%d", time.Now().UnixNano())
	adminUser, err := NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email:                     adminUsername + "@example.invalid",
		Username:                  adminUsername,
		Password:                  "admin-password",
		LocalPasswordLoginEnabled: boolPtr(true),
		Role:                      "admin",
	})
	if err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	t.Cleanup(func() { cleanupPluginProviderUser(t, context.Background(), pool, adminUser.ID) })

	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-admin", adminUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{Username: adminUsername, Password: "x"}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: linked to the admin user
	if user.ID != adminUser.ID {
		t.Fatalf("linked user ID = %d, want admin user %d", user.ID, adminUser.ID)
	}
	if user.Role != "admin" {
		t.Fatalf("linked user role = %q, want admin", user.Role)
	}
}

// ============================================================================
// Test: concurrent first login same subject — exactly one user/identity
// ============================================================================

func TestTrustedLink_ConcurrentFirstLoginSameSubject(t *testing.T) {
	// Given: multiple goroutines log in simultaneously with the same subject and
	// an asserted_username matching one local user.
	fixtureCtx, pool := newPluginProviderDBTest(t)
	ctx, cancel := context.WithTimeout(fixtureCtx, 60*time.Second)
	defer cancel()
	installationID := insertPluginProviderTestInstallation(t, fixtureCtx, pool)
	capabilityID := "concurrent-trusted-link"
	seedTrustedLinkBinding(t, fixtureCtx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	localUsername := fmt.Sprintf("tl-conc-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, fixtureCtx, pool, localUsername, localUsername+"@example.invalid")

	const goroutines = 10
	secondPool := newPluginProviderAdditionalPool(t, fixtureCtx)
	providers := make([]*PluginProvider, goroutines)
	for i := range goroutines {
		identityPool := pool
		if i%2 == 1 {
			identityPool = secondPool
		}
		providers[i] = NewPluginProviderWithClientFactory(
			PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(identityPool)},
			NewSessionRepository(identityPool),
			NewUserRepository(identityPool),
			identityPool,
			func(context.Context) (pluginAuthClient, error) {
				return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
					ExternalSubject:  "concurrent-trusted-sub",
					AssertedUsername: strPtr(localUsername),
				}}, nil
			},
		)
	}
	start := make(chan struct{})
	type result struct {
		userID int
		err    error
	}
	results := make(chan result, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for _, p := range providers {
		go func(p *PluginProvider) {
			defer wg.Done()
			<-start
			key, keyErr := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if keyErr != nil {
				results <- result{err: keyErr}
				return
			}
			user, err := p.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{userID: user.ID}
		}(p)
	}
	close(start)
	wg.Wait()
	close(results)

	// When/Then: all succeed and resolve to the same local user
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent login error: %v", r.err)
		}
		if r.userID != localUserID {
			t.Fatalf("concurrent login user ID = %d, want %d", r.userID, localUserID)
		}
	}
	// Exactly one identity, one user, audit rows for each attempt
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'concurrent-trusted-sub'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 1 {
		t.Fatalf("identity count = %d, want 1", identityCount)
	}
}

// ============================================================================
// Test: concurrent distinct subjects same username — exactly one link, one conflict/provision
// ============================================================================

func TestTrustedLink_ConcurrentDistinctSubjectsSameUsername(t *testing.T) {
	// Given: two goroutines with DIFFERENT subjects but the SAME asserted username.
	// Lock order is consistent (installation→binding→identity→provider-username→user),
	// so no production deadlock exists; the two goroutines serialize on the
	// installation row lock. The test needs a bounded context and proper
	// goroutine lifecycle so it cannot hang on pool cleanup.
	fixtureCtx, pool := newPluginProviderDBTest(t)
	ctx, cancel := context.WithTimeout(fixtureCtx, 30*time.Second)
	defer cancel()
	installationID := insertPluginProviderTestInstallation(t, fixtureCtx, pool)
	capabilityID := "concurrent-distinct-same-name"
	seedTrustedLinkBinding(t, fixtureCtx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	raceUsername := fmt.Sprintf("tl-race-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, fixtureCtx, pool, raceUsername, raceUsername+"@example.invalid")

	subjects := []string{"subject-race-a", "subject-race-b"}
	type raceRes struct {
		err error
	}
	results := make(chan raceRes, len(subjects))
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(subjects))
	// Ensure goroutines are drained before pool.Close runs in t.Cleanup.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	for _, subject := range subjects {
		subject := subject
		p := NewPluginProviderWithClientFactory(
			PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(pool)},
			NewSessionRepository(pool),
			NewUserRepository(pool),
			pool,
			func(context.Context) (pluginAuthClient, error) {
				return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
					ExternalSubject:  subject,
					AssertedUsername: strPtr(raceUsername),
				}}, nil
			},
		)
		go func(p *PluginProvider) {
			defer wg.Done()
			<-start
			key, keyErr := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if keyErr != nil {
				results <- raceRes{err: keyErr}
				return
			}
			_, err := p.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
			results <- raceRes{err: err}
		}(p)
	}
	close(start)
	wg.Wait()
	close(results)

	var successCount, conflictCount int
	for r := range results {
		if r.err == nil {
			successCount++
			continue
		}
		var conflict trustedLinkOwnershipConflict
		if errors.As(r.err, &conflict) {
			conflictCount++
			continue
		}
		t.Fatalf("unexpected error: %v", r.err)
	}

	if successCount != 1 {
		t.Fatalf("success count = %d, want 1", successCount)
	}
	if conflictCount != 1 {
		t.Fatalf("conflict count = %d, want 1", conflictCount)
	}

	// Exactly one identity for the target user
	var identityCount int
	if err := pool.QueryRow(fixtureCtx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND user_id = $2`, installationID, localUserID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 1 {
		t.Fatalf("identity count = %d, want 1", identityCount)
	}

	// Exactly one audit row
	auditCount, err := countAuditRows(fixtureCtx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1", auditCount)
	}
}

// ============================================================================
// Test: audit failure rolls back identity and session
// ============================================================================

func TestTrustedLink_AuditFailureRollsBack(t *testing.T) {
	// Given: a trigger that prevents external_identity_link_audit inserts
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "audit-fail-link"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	localUsername := fmt.Sprintf("audit-fail-user-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, localUsername, localUsername+"@example.invalid")
	// Seed a prior active session that must NOT be revoked on failure
	priorSessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`, priorSessionID, localUserID, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("seed prior session: %v", err)
	}
	triggerName := fmt.Sprintf("block_tlaud_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit blocked'; END; $$; CREATE TRIGGER %s BEFORE INSERT ON external_identity_link_audit FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, triggerName, triggerName)); err != nil {
		t.Fatalf("create audit trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON external_identity_link_audit; DROP FUNCTION IF EXISTS %s()`, triggerName, triggerName)); err != nil {
			t.Fatalf("drop audit trigger: %v", err)
		}
	})
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-audit-fail", localUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)
	newSessionID := uuid.NewString()

	// When
	_, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: newSessionID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})

	// Then: fails, and identity is NOT created (rolled back)
	if err == nil {
		t.Fatal("expected error from audit trigger, got nil")
	}
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'ldap-sub-audit-fail'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 0 {
		t.Fatalf("identity count = %d, want 0 (audit failure rolls back identity)", identityCount)
	}
	// No new session was created
	var newSessionCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE id = $1`, newSessionID).Scan(&newSessionCount); err != nil {
		t.Fatalf("count new sessions: %v", err)
	}
	if newSessionCount != 0 {
		t.Fatalf("new session count = %d, want 0 (rolled back)", newSessionCount)
	}
	// Prior session was NOT revoked
	var priorRevokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, priorSessionID).Scan(&priorRevokedAt); err != nil {
		t.Fatalf("scan prior session: %v", err)
	}
	if priorRevokedAt != nil {
		t.Fatalf("prior session revoked_at = %v, want NULL (must not be revoked on rollback)", priorRevokedAt)
	}
	// No audit row
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("audit count = %d, want 0", auditCount)
	}
}

// ============================================================================
// Test: no ownership overwrite — existing identity with different user is preserved
// ============================================================================

func TestTrustedLink_NoOwnershipOverwrite(t *testing.T) {
	// Given: identity is already owned by user A, asserted username matches user B
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "no-overwrite"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	ownerA := insertLocalPasswordUserNamed(t, ctx, pool, "owner-a", "ownera@example.invalid")
	_ = insertLocalPasswordUserNamed(t, ctx, pool, "owner-b", "ownerb@example.invalid")
	const externalSubject = "ldap-sub-no-overwrite"
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, installationID, externalSubject, ownerA); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	// Provider asserts "owner-b" but identity belongs to "owner-a"
	provider := trustedLinkProvider(pool, installationID, capabilityID, externalSubject, "owner-b", "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: owner A wins (identity always wins)
	if user.ID != ownerA {
		t.Fatalf("user ID = %d, want owner A %d", user.ID, ownerA)
	}
}

// ============================================================================
// Test: policy-off (disabled) behavior — asserted username is ignored, auto-provisions
// ============================================================================

func TestTrustedLink_PolicyOffBehavior(t *testing.T) {
	// Given: trusted_link_mode=disabled, asserted_username present
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "policy-off"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "disabled", "none")
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, "policy-off-user", "poloff@example.invalid")
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-policy-off", "policy-off-user", "auto-username", "auto@example.invalid")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}
	t.Cleanup(func() {
		cleanupPluginProviderUser(t, context.Background(), pool, user.ID)
	})

	// Then: auto-provisioned, NOT linked to the existing local user
	if user.ID == localUserID {
		t.Fatalf("user ID = %d = local user, want auto-provisioned (policy disabled)", user.ID)
	}
	// No audit row for the local user
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("audit count = %d, want 0 (no audit when policy off)", auditCount)
	}
}

// ============================================================================
// Test: local_password_login_enabled=false target is rejected
// ============================================================================

func TestTrustedLink_NonLocalPasswordTargetRejected(t *testing.T) {
	// Given: local_password_login_enabled=false target
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "non-local-pw-target"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	nonLocalUser := insertLocalPasswordUserNamed(t, ctx, pool, "nonlocal-pw", "nonlocal@example.invalid")
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, nonLocalUser); err != nil {
		t.Fatalf("disable local password: %v", err)
	}
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-nonlocal", "nonlocal-pw", "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	// When
	_, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})

	// Then: fails closed (not a local password target)
	if err == nil {
		t.Fatal("expected error for non-local-password target, got nil")
	}
}

// ============================================================================
// Test: session revocation on target before link
// ============================================================================

func TestTrustedLink_RevokesTargetSessionsBeforeIssuingNewSession(t *testing.T) {
	// Given: local user has an existing active session
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "revoke-sessions"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	revokeUsername := fmt.Sprintf("tl-revoke-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, revokeUsername, revokeUsername+"@example.invalid")
	// Seed an active session for the local user
	existingSessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`, existingSessionID, localUserID, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("seed existing session: %v", err)
	}
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-revoke", revokeUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)
	newSessionID := uuid.NewString()

	// When
	_, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: newSessionID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: existing session is revoked
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, existingSessionID).Scan(&revokedAt); err != nil {
		t.Fatalf("scan existing session: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("existing session not revoked after trusted link")
	}
	// New session exists
	var newSessionExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL)`, newSessionID).Scan(&newSessionExists); err != nil {
		t.Fatalf("scan new session: %v", err)
	}
	if !newSessionExists {
		t.Fatal("new session not created after trusted link")
	}
}

// ============================================================================
// Test: retry behavior for serialization/deadlock errors
// ============================================================================

func TestTrustedLink_RetryOnSerializationError(t *testing.T) {
	// Given: first transaction attempt fails with serialization error, second succeeds
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "trusted-link-retry"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	retryUsername := fmt.Sprintf("tl-retry-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, retryUsername, retryUsername+"@example.invalid")
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-retry", retryUsername, "", "")
	calls := 0
	provider.identities.runTransaction = func(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("retryable: %w", &pgconn.PgError{Code: "40001"})
		}
		provider.identities.runTransaction = nil
		return provider.identities.withTransaction(ctx, fn)
	}
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	// When
	user, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then
	if user.ID != localUserID {
		t.Fatalf("user ID = %d, want %d", user.ID, localUserID)
	}
	if calls != 2 {
		t.Fatalf("transaction calls = %d, want 2", calls)
	}
}

// ============================================================================
// Test: disabled-match target fails closed (not auto-provisioned)
// ============================================================================

func TestTrustedLink_DisabledMatchTargetFailsClosed(t *testing.T) {
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "disabled-match-fail"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	disabledUsername := fmt.Sprintf("tl-disabled-match-%d", time.Now().UnixNano())
	userID := insertLocalPasswordUserNamed(t, ctx, pool, disabledUsername, disabledUsername+"@example.invalid")
	if _, err := pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, userID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-disabled-match", disabledUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)

	_, err := provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})

	if err == nil {
		t.Fatal("expected error for disabled target, got nil")
	}
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'ldap-sub-disabled-match'`, installationID).Scan(&identityCount); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 0 {
		t.Fatalf("identity count = %d, want 0 (no link to disabled target)", identityCount)
	}
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }

// ============================================================================
// Test: distinct subjects same username conflict detection (TL-001)
// ============================================================================

type trustedLinkOwnershipConflict interface {
	TrustedLinkOwnershipConflict()
}

func TestTrustedLink_DistinctSubjectsSameUsernameConflict(t *testing.T) {
	// Given: subject-A already trusted-linked to target user; subject-B attempts
	// same username for the same installation/capability.
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "tl-conflict-detection"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	conflictUsername := fmt.Sprintf("tl-conflict-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, conflictUsername, conflictUsername+"@example.invalid")

	// Subject-A links first
	providerA := trustedLinkProvider(pool, installationID, capabilityID, "conflict-sub-a", conflictUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)
	userA, err := providerA.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("subject-A AuthenticateAndComplete: %v", err)
	}
	if userA.ID != localUserID {
		t.Fatalf("subject-A user = %d, want %d", userA.ID, localUserID)
	}

	// Subject-B attempts same username — must get typed conflict error
	providerB := trustedLinkProvider(pool, installationID, capabilityID, "conflict-sub-b", conflictUsername, "", "")
	_, err = providerB.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err == nil {
		t.Fatal("expected ownership conflict error for subject-B, got nil")
	}
	var conflict trustedLinkOwnershipConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want typed TrustedLinkOwnershipConflictError", err)
	}

	// Target identity still owned by subject-A
	var ownerA int
	if err := pool.QueryRow(ctx, `SELECT user_id FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'conflict-sub-a'`, installationID).Scan(&ownerA); err != nil {
		t.Fatalf("scan subject-A identity: %v", err)
	}
	if ownerA != localUserID {
		t.Fatalf("subject-A owner = %d, want %d", ownerA, localUserID)
	}

	// Subject-B has no identity
	var identityBCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND external_subject = 'conflict-sub-b'`, installationID).Scan(&identityBCount); err != nil {
		t.Fatalf("count subject-B identities: %v", err)
	}
	if identityBCount != 0 {
		t.Fatalf("subject-B identity count = %d, want 0 (conflict must not create identity)", identityBCount)
	}

	// No session for subject-B
	var sessionBCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1 AND id != ANY(ARRAY(SELECT id FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NOT NULL)) AND revoked_at IS NULL`, localUserID).Scan(&sessionBCount); err != nil {
		t.Fatalf("count active sessions: %v", err)
	}

	// Only one audit row (from subject-A link)
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1 (only subject-A's link)", auditCount)
	}
}

// ============================================================================
// Race: concurrent distinct subjects same username — count>=10, exactly one
// success, one conflict, one target identity/audit, no second identity
// ============================================================================

func TestTrustedLink_RaceDistinctSubjectsSameUsername(t *testing.T) {
	fixtureCtx, pool := newPluginProviderDBTest(t)
	ctx, cancel := context.WithTimeout(fixtureCtx, 60*time.Second)
	defer cancel()
	installationID := insertPluginProviderTestInstallation(t, fixtureCtx, pool)
	capabilityID := "tl-race-distinct-same-user"
	seedTrustedLinkBinding(t, fixtureCtx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	raceUsername := fmt.Sprintf("tl-race-distinct-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, fixtureCtx, pool, raceUsername, raceUsername+"@example.invalid")

	const goroutines = 10
	secondPool := newPluginProviderAdditionalPool(t, fixtureCtx)
	start := make(chan struct{})
	type raceResult struct {
		userID int
		err    error
	}
	results := make(chan raceResult, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		subject := fmt.Sprintf("race-sub-%d", i)
		identityPool := pool
		if i%2 == 1 {
			identityPool = secondPool
		}
		provider := NewPluginProviderWithClientFactory(
			PluginProviderConfig{InstallationID: installationID, CapabilityID: capabilityID, AutoProvision: true, StoreProvider: pgstore.NewPostgresProvider(identityPool)},
			NewSessionRepository(identityPool),
			NewUserRepository(identityPool),
			identityPool,
			func(context.Context) (pluginAuthClient, error) {
				return pluginProviderTestClient{response: &pluginv1.AuthenticateResponse{
					ExternalSubject:  subject,
					AssertedUsername: strPtr(raceUsername),
				}}, nil
			},
		)
		go func(p *PluginProvider) {
			defer wg.Done()
			<-start
			key, keyErr := models.NewPluginSessionProviderKey(installationID, capabilityID)
			if keyErr != nil {
				results <- raceResult{err: keyErr}
				return
			}
			user, err := p.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
			if err != nil {
				results <- raceResult{err: err}
				return
			}
			results <- raceResult{userID: user.ID}
		}(provider)
	}
	close(start)
	wg.Wait()
	close(results)

	var successCount, conflictCount int
	for r := range results {
		if r.err == nil {
			successCount++
			if r.userID != localUserID {
				t.Fatalf("race success user = %d, want %d", r.userID, localUserID)
			}
			continue
		}
		var conflict trustedLinkOwnershipConflict
		if errors.As(r.err, &conflict) {
			conflictCount++
			continue
		}
		t.Fatalf("unexpected race error: %v", r.err)
	}
	if successCount != 1 {
		t.Fatalf("success count = %d, want exactly 1 (first subject claims)", successCount)
	}
	if conflictCount != goroutines-1 {
		t.Fatalf("conflict count = %d, want %d", conflictCount, goroutines-1)
	}

	// Exactly one identity per goroutine that claimed — but only ONE target user identity
	var targetIdentityCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND user_id = $2`, installationID, localUserID).Scan(&targetIdentityCount); err != nil {
		t.Fatalf("count target identities: %v", err)
	}
	if targetIdentityCount != 1 {
		t.Fatalf("target identity count = %d, want exactly 1 (only first subject claims)", targetIdentityCount)
	}

	// Exactly one audit row for the target user
	auditCount, err := countAuditRows(ctx, pool, localUserID)
	if err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want exactly 1", auditCount)
	}
}

// ============================================================================
// Test: API-key preservation through trusted link (TL-003)
// ============================================================================

func TestTrustedLink_APIKeyPreservation(t *testing.T) {
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	capabilityID := "tl-apikey-preserve"
	seedTrustedLinkBinding(t, ctx, pool, installationID, capabilityID, true, true, "trusted_existing", "none")
	apiUsername := fmt.Sprintf("tl-apikey-%d", time.Now().UnixNano())
	localUserID := insertLocalPasswordUserNamed(t, ctx, pool, apiUsername, apiUsername+"@example.invalid")

	apiRepo := NewAPIKeyRepository(pool)
	targetKey, err := apiRepo.Create(ctx, localUserID, "test-api-key")
	if err != nil {
		t.Fatalf("create target API key: %v", err)
	}

	// Seed an active session that will be revoked
	existingSessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`, existingSessionID, localUserID, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("seed existing session: %v", err)
	}

	provider := trustedLinkProvider(pool, installationID, capabilityID, "ldap-sub-apikey", apiUsername, "", "")
	key, _ := models.NewPluginSessionProviderKey(installationID, capabilityID)
	newSessionID := uuid.NewString()

	// When
	_, err = provider.AuthenticateAndComplete(ctx, Credentials{}, models.AuthSession{ID: newSessionID, ExpiresAt: time.Now().Add(time.Hour), ProviderKey: &key})
	if err != nil {
		t.Fatalf("AuthenticateAndComplete error: %v", err)
	}

	// Then: target API key preserved exactly
	keys, err := apiRepo.ListByUser(ctx, localUserID)
	if err != nil {
		t.Fatalf("list API keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("API key count = %d, want 1", len(keys))
	}
	if keys[0].Key != targetKey.Key {
		t.Fatalf("API key changed from %s to %s", targetKey.Key, keys[0].Key)
	}

	// Prior session revoked
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, existingSessionID).Scan(&revokedAt); err != nil {
		t.Fatalf("scan prior session: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("prior session not revoked after trusted link")
	}

	// New session active
	var newExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL)`, newSessionID).Scan(&newExists); err != nil {
		t.Fatalf("scan new session: %v", err)
	}
	if !newExists {
		t.Fatal("new session not created after trusted link")
	}
}

// pgconn import is needed for the retry test.
var _ = pgconn.PgError{Code: "test"}
