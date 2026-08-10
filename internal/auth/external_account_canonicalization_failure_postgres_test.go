package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type canonicalizationFailureStage struct {
	name      string
	table     string
	timing    string
	predicate string
	deferred  bool
	policy    bool
}

type canonicalizationFailureFixture struct {
	sourceID int
	targetID int
	service  *ExternalAccountCanonicalizer
	store    *canonicalizationFailureStore
}

type canonicalizationFailureStore struct {
	deletions int
}

func (*canonicalizationFailureStore) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, nil
}

func (*canonicalizationFailureStore) Close() error {
	return nil
}

func (*canonicalizationFailureStore) CanonicalizationStoreState(context.Context, int) (userstore.CanonicalizationStoreState, error) {
	return userstore.CanonicalizationStoreState{
		Proven:            true,
		Empty:             true,
		SharedTransaction: true,
		Kind:              userstore.CanonicalizationStorePostgres,
	}, nil
}

func (s *canonicalizationFailureStore) DeleteUser(context.Context, int) error {
	s.deletions++
	return nil
}

func TestExternalAccountCanonicalization_rollsBackMutationFailures_withoutDeletingSourceStore(t *testing.T) {
	stages := []canonicalizationFailureStage{
		{name: "audit_insert", table: "external_identity_link_audit", timing: "BEFORE INSERT", predicate: "NEW.original_user_id = %d"},
		{name: "authorization_audit_rehome", table: "external_authorization_audit", timing: "BEFORE UPDATE", predicate: "OLD.user_id = %d"},
		{name: "provenance_rehome", table: "activity_log", timing: "BEFORE UPDATE", predicate: "OLD.user_id = %d"},
		{name: "favorite_deduplication", table: "user_favorites", timing: "BEFORE DELETE", predicate: "OLD.user_id = %d"},
		{name: "interest_deduplication", table: "profile_series_interest", timing: "BEFORE DELETE", predicate: "OLD.user_id = %d"},
		{name: "identity_and_state_transfer", table: "external_authorization_states", timing: "BEFORE DELETE", predicate: "OLD.user_id = %d"},
		{name: "credential_revocation", table: "auth_sessions", timing: "BEFORE UPDATE", predicate: "OLD.user_id = %d"},
		{name: "profile_deletion", table: "user_profiles", timing: "BEFORE DELETE", predicate: "OLD.user_id = %d"},
		{name: "user_deletion", table: "users", timing: "BEFORE DELETE", predicate: "OLD.id = %d"},
		{name: "commit", table: "users", timing: "AFTER DELETE", deferred: true},
	}

	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			// Given
			ctx, pool := newPluginProviderDBTest(t)
			fixture := newCanonicalizationFailureFixture(t, ctx, pool, stage.policy)
			preview, err := fixture.service.Preview(ctx, CanonicalizationOperator{UserID: fixture.targetID, IsAdmin: true}, fixture.sourceID, fixture.targetID)
			if err != nil {
				t.Fatalf("preview canonicalization: %v", err)
			}
			before := snapshotCanonicalizationFailureState(t, ctx, pool, fixture.sourceID, fixture.targetID)
			removeTrigger := installCanonicalizationFailureTrigger(t, ctx, pool, stage, fixture.sourceID)

			// When
			_, err = fixture.service.Execute(ctx, CanonicalizationOperator{UserID: fixture.targetID, IsAdmin: true}, preview.Token)

			// Then
			if err == nil {
				t.Fatal("Execute() succeeded despite mutation-stage failure")
			}
			after := snapshotCanonicalizationFailureState(t, ctx, pool, fixture.sourceID, fixture.targetID)
			if after != before {
				t.Errorf("database changed after %s failure\nbefore: %s\nafter:  %s", stage.name, before, after)
			}
			if fixture.store.deletions != 0 {
				t.Errorf("source store deletions after %s failure = %d, want 0", stage.name, fixture.store.deletions)
			}

			if !stage.policy {
				removeTrigger()
				replay, replayErr := fixture.service.Execute(ctx, CanonicalizationOperator{UserID: fixture.targetID, IsAdmin: true}, preview.Token)
				if replayErr != nil {
					t.Fatalf("replay after transient %s failure: %v", stage.name, replayErr)
				}
				if !replay.SourceDeleted || replay.IdentityOwner != fixture.targetID {
					t.Fatalf("replay receipt after transient %s failure = %+v", stage.name, replay)
				}
			}
		})
	}
}

func newCanonicalizationFailureFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, _ bool) canonicalizationFailureFixture {
	t.Helper()
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	targetID := insertPluginProviderTestUser(t, ctx, pool, "canonical-failure-target")
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "canonical-failure-source")
	targetProfile := fmt.Sprintf("canonical-failure-target-profile-%d", targetID)
	sourceProfile := fmt.Sprintf("canonical-failure-source-profile-%d", sourceID)
	seeds := []struct {
		query string
		args  []any
	}{
		{`UPDATE users SET local_password_login_enabled = false WHERE id = $1`, []any{sourceID}},
		{`INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled, auto_provision, authorization_mode) VALUES ($1, 'canonicalization', true, true, 'none')`, []any{installationID}},
		{`INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Target', true), ($3, $4, 'Source', true)`, []any{targetProfile, targetID, sourceProfile, sourceID}},
		{`INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, 'canonical-failure-subject', $2)`, []any{installationID, sourceID}},
		{`INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1, $2, 'canonicalization', 'user', 'user', ARRAY[]::text[], 'canonicalization', $3)`, []any{sourceID, installationID, uuid.New()}},
		{`INSERT INTO activity_log (id, client_ip, user_id, method, path) VALUES (nextval('activity_log_id_seq'), '127.0.0.1', $1, 'GET', '/canonicalization')`, []any{sourceID}},
		{`INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, 'shared', '2026-01-01T00:00:00Z'), ($3, $4, 'shared', '2026-02-01T00:00:00Z'), ($3, $4, 'source-only', '2026-02-01T00:00:00Z')`, []any{targetID, targetProfile, sourceID, sourceProfile}},
		{`INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite, updated_at) VALUES ($1, $2, 1, 'shared', true, '2026-01-01T00:00:00Z'), ($3, $4, 1, 'shared', false, '2026-02-01T00:00:00Z'), ($3, $4, 1, 'source-only', true, '2026-02-01T00:00:00Z')`, []any{targetID, targetProfile, sourceID, sourceProfile}},
		{`INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) VALUES ($1, $2, 'canonicalization')`, []any{sourceID, installationID}},
		{`INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ($1, $2, NOW() + INTERVAL '1 hour'), ($3, $4, NOW() + INTERVAL '1 hour')`, []any{uuid.NewString(), sourceID, uuid.NewString(), targetID}},
	}
	for _, seed := range seeds {
		if _, err := pool.Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed canonicalization failure fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `TRUNCATE auth_provider_policy_audit, external_authorization_audit, external_identity_link_audit CASCADE`); err != nil {
			t.Errorf("cleanup canonicalization failure audit rows: %v", err)
		}
	})
	store := &canonicalizationFailureStore{}
	return canonicalizationFailureFixture{
		sourceID: sourceID,
		targetID: targetID,
		service: NewExternalAccountCanonicalizer(pool, []byte("test-preview-key"), time.Now).
			WithStoreProvider(store),
		store: store,
	}
}

func installCanonicalizationFailureTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, stage canonicalizationFailureStage, sourceID int) func() {
	t.Helper()
	name := fmt.Sprintf("zz_canonicalization_%s_%d", stage.name, sourceID)
	function := name + "_function"
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'canonicalization %s failure'; END; $$;`, function, stage.name)
	if stage.deferred {
		statement = fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id = %d THEN RAISE EXCEPTION 'canonicalization commit failure'; END IF; RETURN OLD; END; $$; CREATE CONSTRAINT TRIGGER %s AFTER DELETE ON users DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s()`, function, sourceID, name, function)
	} else {
		statement += fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH ROW WHEN (%s) EXECUTE FUNCTION %s()`, name, stage.timing, stage.table, fmt.Sprintf(stage.predicate, sourceID), function)
	}
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("create %s failure trigger: %v", stage.name, err)
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		if _, err := pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s; DROP FUNCTION IF EXISTS %s()`, name, stage.table, function)); err != nil {
			t.Errorf("drop %s failure trigger: %v", stage.name, err)
		}
	}
	t.Cleanup(remove)
	return remove
}

func snapshotCanonicalizationFailureState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID, targetID int) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(ctx, `
		SELECT jsonb_build_object(
			'users', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM users WHERE id IN ($1, $2)) row),
			'profiles', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM user_profiles WHERE user_id IN ($1, $2)) row),
			'identities', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM plugin_auth_identities WHERE user_id IN ($1, $2)) row),
			'canonicalization_audits', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM external_identity_link_audit WHERE original_user_id = $1 OR user_id IN ($1, $2)) row),
			'authorization_audits', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM external_authorization_audit WHERE original_user_id = $1 OR user_id IN ($1, $2)) row),
			'policy_audits', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM auth_provider_policy_audit WHERE actor_user_id IN ($1, $2)) row),
			'activity', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM activity_log WHERE user_id IN ($1, $2)) row),
			'favorites', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.user_id, row.profile_id, row.media_item_id), '[]'::jsonb) FROM (SELECT * FROM user_favorites WHERE user_id IN ($1, $2)) row),
			'interests', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.user_id, row.profile_id, row.library_id, row.series_id), '[]'::jsonb) FROM (SELECT * FROM profile_series_interest WHERE user_id IN ($1, $2)) row),
			'authorization_states', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.user_id, row.plugin_installation_id, row.capability_id), '[]'::jsonb) FROM (SELECT * FROM external_authorization_states WHERE user_id IN ($1, $2)) row),
			'auth_sessions', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM auth_sessions WHERE user_id IN ($1, $2)) row),
			'abs_sessions', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM abs_sessions WHERE user_id IN ($1, $2)) row),
			'api_keys', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM api_keys WHERE user_id IN ($1, $2)) row),
			'jellycompat_playback_sessions', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.id), '[]'::jsonb) FROM (SELECT * FROM jellycompat_playback_sessions WHERE user_id IN (($1::bigint)::text, ($2::bigint)::text)) row),
			'jellycompat_sessions', (SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.token), '[]'::jsonb) FROM (SELECT * FROM jellycompat_sessions WHERE streamapp_user_id IN ($1, $2)) row)
		)::text`, sourceID, targetID).Scan(&snapshot); err != nil {
		t.Fatalf("snapshot canonicalization state: %v", err)
	}
	return snapshot
}
