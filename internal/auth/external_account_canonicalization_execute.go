package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *ExternalAccountCanonicalizer) apply(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) (CanonicalizationReceipt, error) {
	capabilityID := state.capabilityID
	if capabilityID == "" {
		capabilityID = "canonicalization"
	}
	var auditID int64
	if err := tx.QueryRow(ctx, `INSERT INTO external_identity_link_audit (user_id, original_user_id, original_profile_id, plugin_installation_id, capability_id, event_type, trusted_link_mode, external_subject_fingerprint, reason_code, correlation_id) VALUES ($1,$2,$3,$4,$5,'canonicalization','trusted_existing',$6,'admin_canonicalization',$7) RETURNING id`, state.targetID, state.sourceID, state.sourceProfile, state.installationID, capabilityID, state.identityFingerprint, uuid.New()).Scan(&auditID); err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("record canonicalization audit: %w", err)
	}
	if err := rehomeAuthorizationAudit(ctx, tx, state, auditID); err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := rehomeUserProvenance(ctx, tx, state); err != nil {
		return CanonicalizationReceipt{}, err
	}
	favorites, err := transferFavorites(ctx, tx, state)
	if err != nil {
		return CanonicalizationReceipt{}, err
	}
	interests, err := transferInterests(ctx, tx, state)
	if err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := transferIdentitiesAndStates(ctx, tx, state); err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := transferProfileOnboarding(ctx, tx, state); err != nil {
		return CanonicalizationReceipt{}, err
	}
	if err := revokeCanonicalizationCredentials(ctx, tx, state); err != nil {
		return CanonicalizationReceipt{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, state.sourceID); err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("delete source profiles: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, state.sourceID); err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("delete source account: %w", err)
	}
	return CanonicalizationReceipt{SourceDeleted: true, IdentityOwner: state.targetID, FavoritesTransferred: favorites, InterestsTransferred: interests}, nil
}

func transferProfileOnboarding(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) error {
	if _, err := tx.Exec(ctx, `INSERT INTO user_profile_onboarding (user_id, profile_id, tour_id, last_step, completed_at, skipped_at, updated_at) SELECT $1, $2, tour_id, last_step, completed_at, skipped_at, updated_at FROM user_profile_onboarding WHERE user_id = $3 ON CONFLICT (user_id, profile_id, tour_id) DO NOTHING`, state.targetID, state.targetProfile, state.sourceID); err != nil {
		return fmt.Errorf("transfer profile onboarding: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_profile_onboarding WHERE user_id = $1`, state.sourceID); err != nil {
		return fmt.Errorf("delete source profile onboarding: %w", err)
	}
	return nil
}

func rehomeUserProvenance(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) error {
	queries := []string{
		`UPDATE activity_log SET user_id = $1 WHERE user_id = $2`,
		`UPDATE operational_logs SET user_id = $1 WHERE user_id = $2`,
		`UPDATE policy_decisions SET user_id = $1, profile_id = $2 WHERE user_id = $3`,
	}
	for index, query := range queries {
		args := []any{state.targetID, state.sourceID}
		if index == 2 {
			args = []any{state.targetID, state.targetProfile, state.sourceID}
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("rehome canonicalization provenance: %w", err)
		}
	}
	return nil
}

func rehomeAuthorizationAudit(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot, auditID int64) error {
	if state.dependencies["external_authorization_audit"] == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE external_authorization_audit SET user_id = $1, original_profile_id = $2, canonicalization_audit_id = $3 WHERE user_id = $4`, state.targetID, state.sourceProfile, auditID, state.sourceID); err != nil {
		return fmt.Errorf("rehome external authorization audit: %w", err)
	}
	return nil
}

func transferFavorites(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) (int, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) SELECT $1, $2, media_item_id, added_at FROM user_favorites WHERE user_id = $3 ON CONFLICT (user_id, profile_id, media_item_id) DO NOTHING`, state.targetID, state.targetProfile, state.sourceID)
	if err != nil {
		return 0, fmt.Errorf("transfer favorites: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_favorites WHERE user_id = $1`, state.sourceID); err != nil {
		return 0, fmt.Errorf("delete source favorites: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func transferInterests(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) (int, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite, watchlist, continue_watching, next_up_candidate, last_completed_episode_key, next_expected_episode_key, last_notified_episode_key, updated_at) SELECT $1, $2, library_id, series_id, favorite, watchlist, continue_watching, next_up_candidate, last_completed_episode_key, next_expected_episode_key, last_notified_episode_key, updated_at FROM profile_series_interest WHERE user_id = $3 ON CONFLICT (profile_id, library_id, series_id) DO NOTHING`, state.targetID, state.targetProfile, state.sourceID)
	if err != nil {
		return 0, fmt.Errorf("transfer series interests: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM profile_series_interest WHERE user_id = $1`, state.sourceID); err != nil {
		return 0, fmt.Errorf("delete source interests: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func transferIdentitiesAndStates(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) error {
	if _, err := tx.Exec(ctx, `UPDATE plugin_auth_identities SET user_id = $1 WHERE user_id = $2`, state.targetID, state.sourceID); err != nil {
		return fmt.Errorf("transfer plugin identities: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) SELECT $1, plugin_installation_id, capability_id FROM external_authorization_states WHERE user_id = $2 ON CONFLICT DO NOTHING`, state.targetID, state.sourceID); err != nil {
		return fmt.Errorf("reconcile authorization states: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM external_authorization_states WHERE user_id = $1`, state.sourceID); err != nil {
		return fmt.Errorf("delete source authorization states: %w", err)
	}
	return nil
}

func revokeCanonicalizationCredentials(ctx context.Context, tx pgx.Tx, state canonicalizationSnapshot) error {
	queries := []struct {
		query string
		args  []any
	}{
		{`UPDATE auth_sessions SET revoked_at = NOW() WHERE user_id IN ($1, $2) AND revoked_at IS NULL`, []any{state.sourceID, state.targetID}},
		{`DELETE FROM abs_sessions WHERE user_id = $1`, []any{state.sourceID}},
		{`DELETE FROM jellycompat_playback_sessions WHERE user_id = ($1::bigint)::text`, []any{state.sourceID}},
		{`DELETE FROM jellycompat_sessions WHERE streamapp_user_id = $1`, []any{state.sourceID}},
		{`DELETE FROM device_login_requests WHERE approved_by_user_id = $1 OR auth_session_id IN (SELECT id FROM auth_sessions WHERE user_id = $1)`, []any{state.sourceID}},
		{`DELETE FROM oauth_session WHERE linking_user_id = ($1::bigint)::text`, []any{state.sourceID}},
		{`DELETE FROM api_keys WHERE user_id = $1`, []any{state.sourceID}},
	}
	for _, operation := range queries {
		if _, err := tx.Exec(ctx, operation.query, operation.args...); err != nil {
			return fmt.Errorf("revoke canonicalization credentials: %w", err)
		}
	}
	return nil
}

func (s *ExternalAccountCanonicalizer) replayReceipt(ctx context.Context, tx pgx.Tx, token canonicalizationToken) (CanonicalizationReceipt, error) {
	var targetID int
	err := tx.QueryRow(ctx, `SELECT user_id FROM external_identity_link_audit WHERE original_user_id = $1 AND event_type = 'canonicalization'`, token.Source).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CanonicalizationReceipt{}, ErrCanonicalizationStale
	}
	if err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("load canonicalization replay: %w", err)
	}
	if targetID != token.Target {
		return CanonicalizationReceipt{}, ErrCanonicalizationStale
	}
	var existingTargetID int
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, targetID).Scan(&existingTargetID); err != nil {
		return CanonicalizationReceipt{}, ErrCanonicalizationStale
	}
	if err := tx.Commit(ctx); err != nil {
		return CanonicalizationReceipt{}, fmt.Errorf("commit canonicalization replay: %w", err)
	}
	return CanonicalizationReceipt{SourceDeleted: true, IdentityOwner: targetID}, nil
}
