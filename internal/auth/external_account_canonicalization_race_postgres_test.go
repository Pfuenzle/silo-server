package auth

// allow: SIZE_OK — one cohesive isolated PostgreSQL race matrix must keep shared barriers and outcome assertions together.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

const canonicalizationRaceRuns = 10

type canonicalizationRaceFixture struct {
	installationID int
	sourceID       int
	targetID       int
	service        *ExternalAccountCanonicalizer
	operator       CanonicalizationOperator
	token          string
}

func TestExternalAccountCanonicalization_Execute_waitsForProviderBindingBeforeUsers_whenProviderDisables(t *testing.T) {
	for run := 0; run < canonicalizationRaceRuns; run++ {
		t.Run(fmt.Sprintf("run-%d", run), func(t *testing.T) {
			// Given
			fixture := newCanonicalizationRaceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx := lockCanonicalizationBinding(t, ctx, fixture)
			defer rollbackRaceTransaction(ctx, tx)

			// When
			done := executeCanonicalizationAsync(ctx, fixture)
			assertCanonicalizationWaitsForBarrier(t, done)
			if _, err := tx.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = false WHERE plugin_installation_id = $1 AND capability_id = 'canonicalization-race'`, fixture.installationID); err != nil {
				t.Fatalf("disable provider binding: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit provider disable: %v", err)
			}

			// Then
			assertCanonicalizationStaleWithoutPartialState(t, fixture, <-done)
		})
	}
}

func TestExternalAccountCanonicalization_Execute_waitsForInstallationBeforeUsers_whenProviderDisables(t *testing.T) {
	for run := 0; run < canonicalizationRaceRuns; run++ {
		t.Run(fmt.Sprintf("run-%d", run), func(t *testing.T) {
			// Given
			fixture := newCanonicalizationRaceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx := lockCanonicalizationInstallation(t, ctx, fixture)
			defer rollbackRaceTransaction(ctx, tx)

			// When
			done := executeCanonicalizationAsync(ctx, fixture)
			assertCanonicalizationWaitsForBarrier(t, done)
			if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, fixture.installationID); err != nil {
				t.Fatalf("disable provider installation: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit provider disable: %v", err)
			}

			// Then
			assertCanonicalizationStaleWithoutPartialState(t, fixture, <-done)
		})
	}
}

func TestExternalAccountCanonicalization_Execute_waitsForIdentityClaimBeforeUsers_whenExternalLoginClaimsIdentity(t *testing.T) {
	for run := 0; run < canonicalizationRaceRuns; run++ {
		t.Run(fmt.Sprintf("run-%d", run), func(t *testing.T) {
			// Given
			fixture := newCanonicalizationRaceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx := lockCanonicalizationIdentity(t, ctx, fixture)
			defer rollbackRaceTransaction(ctx, tx)

			// When
			done := executeCanonicalizationAsync(ctx, fixture)
			assertCanonicalizationWaitsForBarrier(t, done)
			if _, err := tx.Exec(ctx, `UPDATE plugin_auth_identities SET external_subject = 'canonicalization-race-changed' WHERE plugin_installation_id = $1 AND user_id = $2`, fixture.installationID, fixture.sourceID); err != nil {
				t.Fatalf("claim changed external identity: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit external identity claim: %v", err)
			}

			// Then
			assertCanonicalizationStaleWithoutPartialState(t, fixture, <-done)
		})
	}
}

func TestExternalAccountCanonicalization_Execute_returnsStale_whenDependencyChangesAfterPreview(t *testing.T) {
	// Given
	fixture := newCanonicalizationRaceFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.pool.Exec(ctx, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id) VALUES ($1, $2, 'canonicalization-race')`, fixture.sourceID, canonicalizationRaceProfileID(fixture.sourceID)); err != nil {
		t.Fatalf("change source dependency: %v", err)
	}

	// When
	_, err := fixture.service.Execute(ctx, fixture.operator, fixture.token)

	// Then
	assertCanonicalizationStaleWithoutPartialState(t, fixture, err)
}

func TestExternalAccountCanonicalization_Execute_returnsStaleWithoutPartialState_whenSourceDeletionRaces(t *testing.T) {
	// Given
	fixture := newCanonicalizationRaceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx := lockCanonicalizationUser(t, ctx, fixture.sourceID)
	defer rollbackRaceTransaction(ctx, tx)

	// When
	done := executeCanonicalizationAsync(ctx, fixture)
	assertCanonicalizationWaitsForBarrier(t, done)
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.sourceID); err != nil {
		t.Fatalf("delete source account: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit source deletion: %v", err)
	}

	// Then
	err := <-done
	if !errors.Is(err, ErrCanonicalizationStale) {
		t.Fatalf("execute error = %v, want stale", err)
	}
	assertCanonicalizationAuditCount(t, fixture, 0)
}

func TestExternalAccountCanonicalization_Execute_returnsStaleWithoutPartialState_whenTargetDeletionRaces(t *testing.T) {
	// Given
	fixture := newCanonicalizationRaceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx := lockCanonicalizationUser(t, ctx, fixture.targetID)
	defer rollbackRaceTransaction(ctx, tx)

	// When
	done := executeCanonicalizationAsync(ctx, fixture)
	assertCanonicalizationWaitsForBarrier(t, done)
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetID); err != nil {
		t.Fatalf("delete target account: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit target deletion: %v", err)
	}

	// Then
	err := <-done
	if !errors.Is(err, ErrCanonicalizationStale) {
		t.Fatalf("execute error = %v, want stale", err)
	}
	assertCanonicalizationAuditCount(t, fixture, 0)
}

func TestExternalAccountCanonicalization_Execute_replaysOnceAndRejectsCompetingTarget(t *testing.T) {
	// Given
	fixture := newCanonicalizationRaceFixture(t)
	competitorID := insertPluginProviderTestUser(t, context.Background(), fixture.service.pool, "canonicalization-race-competitor")
	if _, err := fixture.service.pool.Exec(context.Background(), `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Competitor', true)`, canonicalizationRaceProfileID(competitorID), competitorID); err != nil {
		t.Fatalf("seed competitor profile: %v", err)
	}
	competingPreview, err := fixture.service.Preview(context.Background(), fixture.operator, fixture.sourceID, competitorID)
	if err != nil {
		t.Fatalf("preview competing target: %v", err)
	}

	// When
	results := executeCanonicalizationPair(t, fixture, competingPreview.Token)

	// Then
	if results.successes != 2 || results.stale != 1 {
		t.Fatalf("canonicalization outcomes = successes:%d stale:%d; want 2,1", results.successes, results.stale)
	}
	assertCanonicalizationAuditCount(t, fixture, 1)
	var owner int
	if err := fixture.service.pool.QueryRow(context.Background(), `SELECT user_id FROM plugin_auth_identities WHERE plugin_installation_id = $1`, fixture.installationID).Scan(&owner); err != nil {
		t.Fatalf("load identity owner: %v", err)
	}
	if owner != fixture.targetID {
		t.Fatalf("identity owner = %d, want first target %d", owner, fixture.targetID)
	}
}

type canonicalizationRaceResults struct{ successes, stale int }

func executeCanonicalizationPair(t *testing.T, fixture canonicalizationRaceFixture, competitorToken string) canonicalizationRaceResults {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, 3)
	var workers sync.WaitGroup
	for _, token := range []string{fixture.token, fixture.token, competitorToken} {
		workers.Add(1)
		go func(token string) {
			defer workers.Done()
			<-start
			_, err := fixture.service.Execute(context.Background(), fixture.operator, token)
			errs <- err
		}(token)
	}
	close(start)
	workers.Wait()
	close(errs)
	results := canonicalizationRaceResults{}
	for err := range errs {
		if err == nil {
			results.successes++
			continue
		}
		if errors.Is(err, ErrCanonicalizationStale) {
			results.stale++
			continue
		}
		t.Fatalf("execute race error = %v", err)
	}
	return results
}

func newCanonicalizationRaceFixture(t *testing.T) canonicalizationRaceFixture {
	t.Helper()
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization-race', true, true, 'none')`, installationID); err != nil {
		t.Fatalf("seed provider binding: %v", err)
	}
	targetID := insertPluginProviderTestUser(t, ctx, pool, "canonicalization-race-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "canonicalization-race-source")
	if _, err := pool.Exec(ctx, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, sourceID); err != nil {
		t.Fatalf("make source provider-only: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Source', true), ($3, $4, 'Target', true)`, canonicalizationRaceProfileID(sourceID), sourceID, canonicalizationRaceProfileID(targetID), targetID); err != nil {
		t.Fatalf("seed canonicalization profiles: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'canonicalization-race-subject', $2)`, installationID, sourceID); err != nil {
		t.Fatalf("seed source identity: %v", err)
	}
	service := NewExternalAccountCanonicalizer(pool, []byte("canonicalization-race-key"), time.Now).WithStoreProvider(pgstore.NewPostgresProvider(pool))
	preview, err := service.Preview(ctx, CanonicalizationOperator{UserID: targetID, IsAdmin: true}, sourceID, targetID)
	if err != nil {
		t.Fatalf("preview canonicalization: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := pool.Exec(cleanupCtx, `ALTER TABLE external_identity_link_audit DISABLE TRIGGER external_identity_link_audit_immutable`); err != nil {
			t.Errorf("disable canonicalization race audit trigger: %v", err)
			return
		}
		defer func() {
			if _, err := pool.Exec(cleanupCtx, `ALTER TABLE external_identity_link_audit ENABLE TRIGGER external_identity_link_audit_immutable`); err != nil {
				t.Errorf("enable canonicalization race audit trigger: %v", err)
			}
		}()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM external_identity_link_audit WHERE original_user_id = $1`, sourceID); err != nil {
			t.Errorf("cleanup canonicalization race audit: %v", err)
		}
	})
	return canonicalizationRaceFixture{installationID: installationID, sourceID: sourceID, targetID: targetID, service: service, operator: CanonicalizationOperator{UserID: targetID, IsAdmin: true}, token: preview.Token}
}

func canonicalizationRaceProfileID(userID int) string {
	return fmt.Sprintf("canonicalization-race-profile-%d", userID)
}

func newCanonicalizationRacePool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("create race pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func lockCanonicalizationBinding(t *testing.T, ctx context.Context, fixture canonicalizationRaceFixture) pgx.Tx {
	t.Helper()
	tx, err := newCanonicalizationRacePool(t, ctx).Begin(ctx)
	if err != nil {
		t.Fatalf("begin binding barrier: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = 'canonicalization-race' FOR UPDATE`, fixture.installationID); err != nil {
		t.Fatalf("lock provider binding: %v", err)
	}
	return tx
}

func lockCanonicalizationInstallation(t *testing.T, ctx context.Context, fixture canonicalizationRaceFixture) pgx.Tx {
	t.Helper()
	tx, err := newCanonicalizationRacePool(t, ctx).Begin(ctx)
	if err != nil {
		t.Fatalf("begin installation barrier: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM plugin_installations WHERE id = $1 FOR UPDATE`, fixture.installationID); err != nil {
		t.Fatalf("lock provider installation: %v", err)
	}
	return tx
}

func lockCanonicalizationIdentity(t *testing.T, ctx context.Context, fixture canonicalizationRaceFixture) pgx.Tx {
	t.Helper()
	tx, err := newCanonicalizationRacePool(t, ctx).Begin(ctx)
	if err != nil {
		t.Fatalf("begin identity barrier: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND user_id = $2 FOR UPDATE`, fixture.installationID, fixture.sourceID); err != nil {
		t.Fatalf("lock external identity: %v", err)
	}
	return tx
}

func lockCanonicalizationUser(t *testing.T, ctx context.Context, userID int) pgx.Tx {
	t.Helper()
	tx, err := newCanonicalizationRacePool(t, ctx).Begin(ctx)
	if err != nil {
		t.Fatalf("begin user deletion barrier: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatalf("lock user for deletion: %v", err)
	}
	return tx
}

func executeCanonicalizationAsync(ctx context.Context, fixture canonicalizationRaceFixture) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := fixture.service.Execute(ctx, fixture.operator, fixture.token)
		done <- err
	}()
	return done
}

func assertCanonicalizationWaitsForBarrier(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("execute completed before dependency barrier released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func assertCanonicalizationStaleWithoutPartialState(t *testing.T, fixture canonicalizationRaceFixture, err error) {
	t.Helper()
	if !errors.Is(err, ErrCanonicalizationStale) {
		t.Fatalf("execute error = %v, want stale", err)
	}
	var users, identities int
	if err := fixture.service.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE id IN ($1, $2)`, fixture.sourceID, fixture.targetID).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := fixture.service.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND user_id = $2`, fixture.installationID, fixture.sourceID).Scan(&identities); err != nil {
		t.Fatalf("count source identities: %v", err)
	}
	if users != 2 || identities != 1 {
		t.Fatalf("partial canonicalization state = users:%d identities:%d; want 2,1", users, identities)
	}
	assertCanonicalizationAuditCount(t, fixture, 0)
}

func assertCanonicalizationAuditCount(t *testing.T, fixture canonicalizationRaceFixture, want int) {
	t.Helper()
	var audits int
	if err := fixture.service.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM external_identity_link_audit WHERE original_user_id = $1 AND event_type = 'canonicalization'`, fixture.sourceID).Scan(&audits); err != nil {
		t.Fatalf("count canonicalization audit: %v", err)
	}
	if audits != want {
		t.Fatalf("canonicalization audit rows = %d, want %d", audits, want)
	}
}

func rollbackRaceTransaction(ctx context.Context, tx pgx.Tx) {
	if tx != nil {
		_ = tx.Rollback(ctx)
	}
}
