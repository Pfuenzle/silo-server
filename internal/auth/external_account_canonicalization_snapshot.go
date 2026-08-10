package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type canonicalizationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type canonicalizationSnapshot struct {
	sourceID, targetID                                        int
	sourceProfile, targetProfile                              string
	installationID                                            int
	capabilityID                                              string
	externalSubject                                           string
	identityOwner                                             int
	installationEnabled, bindingEnabled                       bool
	sourceFingerprint, targetFingerprint, identityFingerprint string
	dependencies                                              map[string]int
	blockers                                                  []string
}

func (s *ExternalAccountCanonicalizer) snapshot(ctx context.Context, q canonicalizationQuerier, sourceID, targetID int, locked bool) (canonicalizationSnapshot, error) {
	var sourceEnabled, sourceLocal, targetEnabled, targetLocal bool
	var sourceName, targetName string
	lock := ""
	if locked {
		lock = " FOR KEY SHARE"
	}
	if err := q.QueryRow(ctx, `SELECT enabled, local_password_login_enabled, username FROM users WHERE id = $1`+lock, sourceID).Scan(&sourceEnabled, &sourceLocal, &sourceName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return canonicalizationSnapshot{}, ErrNotFound
		}
		return canonicalizationSnapshot{}, fmt.Errorf("load source account: %w", err)
	}
	if err := q.QueryRow(ctx, `SELECT enabled, local_password_login_enabled, username FROM users WHERE id = $1`+lock, targetID).Scan(&targetEnabled, &targetLocal, &targetName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return canonicalizationSnapshot{}, ErrNotFound
		}
		return canonicalizationSnapshot{}, fmt.Errorf("load target account: %w", err)
	}
	state := canonicalizationSnapshot{sourceID: sourceID, targetID: targetID, sourceFingerprint: fingerprint(sourceName), targetFingerprint: fingerprint(targetName), dependencies: map[string]int{}}
	if !sourceEnabled || sourceLocal {
		state.blockers = append(state.blockers, "source_account_mode")
	}
	if !targetEnabled || !targetLocal {
		state.blockers = append(state.blockers, "target_account_mode")
	}
	if err := q.QueryRow(ctx, `SELECT id FROM user_profiles WHERE user_id = $1 AND is_primary`+lock, sourceID).Scan(&state.sourceProfile); err != nil {
		state.blockers = append(state.blockers, "source_primary_profile")
	}
	if err := q.QueryRow(ctx, `SELECT id FROM user_profiles WHERE user_id = $1 AND is_primary`+lock, targetID).Scan(&state.targetProfile); err != nil {
		state.blockers = append(state.blockers, "target_primary_profile")
	}
	if err := q.QueryRow(ctx, `SELECT plugin_installation_id, external_subject, user_id FROM plugin_auth_identities WHERE user_id = $1`+lock, sourceID).Scan(&state.installationID, &state.externalSubject, &state.identityOwner); err != nil {
		state.blockers = append(state.blockers, "source_external_identity")
	}
	state.identityFingerprint = fingerprint(state.externalSubject)
	if err := q.QueryRow(ctx, `SELECT enabled FROM plugin_installations WHERE id = $1`+lock, state.installationID).Scan(&state.installationEnabled); err != nil {
		state.blockers = append(state.blockers, "source_plugin_installation")
	}
	if err := q.QueryRow(ctx, `SELECT capability_id FROM external_authorization_audit WHERE user_id = $1 ORDER BY id LIMIT 1`, sourceID).Scan(&state.capabilityID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return canonicalizationSnapshot{}, fmt.Errorf("load canonicalization capability: %w", err)
	}
	if state.capabilityID == "" {
		if err := q.QueryRow(ctx, `SELECT capability_id FROM plugin_auth_bindings WHERE plugin_installation_id = $1 ORDER BY capability_id LIMIT 1`+lock, state.installationID).Scan(&state.capabilityID); err != nil {
			state.blockers = append(state.blockers, "source_plugin_binding")
		}
	}
	if err := q.QueryRow(ctx, `SELECT enabled FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2`+lock, state.installationID, state.capabilityID).Scan(&state.bindingEnabled); err != nil {
		state.blockers = append(state.blockers, "source_plugin_binding")
	}
	if !state.installationEnabled || !state.bindingEnabled {
		state.blockers = append(state.blockers, "source_provider_disabled")
	}
	dependencies, err := canonicalizationDependencies(ctx, q, sourceID, state.sourceProfile)
	if err != nil {
		return canonicalizationSnapshot{}, err
	}
	for name, count := range dependencies {
		state.dependencies[name] = count
		if count != 0 && !canonicalizationSupportedDependency(name) {
			state.blockers = append(state.blockers, name)
		}
	}
	if state.dependencies["plugin_auth_identities"] != 1 {
		state.blockers = append(state.blockers, "plugin_auth_identities")
	}
	if state.dependencies["user_profiles"] != 1 {
		state.blockers = append(state.blockers, "source_profiles")
	}
	if state.dependencies["external_authorization_audit"] > 0 && state.capabilityID == "" {
		state.blockers = append(state.blockers, "external_authorization_audit")
	}
	sort.Strings(state.blockers)
	return state, nil
}

func (s *canonicalizationSnapshot) sortBlockers() {
	sort.Strings(s.blockers)
}

func (s canonicalizationSnapshot) digest() string {
	keys := make([]string, 0, len(s.dependencies))
	for key := range s.dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{strconv.Itoa(s.sourceID), strconv.Itoa(s.targetID), s.sourceProfile, s.targetProfile, strconv.Itoa(s.installationID), s.capabilityID, strconv.FormatBool(s.installationEnabled), strconv.FormatBool(s.bindingEnabled), strconv.Itoa(s.identityOwner), s.sourceFingerprint, s.targetFingerprint, s.identityFingerprint}
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Itoa(s.dependencies[key]))
	}
	parts = append(parts, s.blockers...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

var canonicalizationOwnershipColumns = []string{
	"user_id", "actor_user_id", "profile_id", "silo_profile_id", "default_profile_id", "host_profile_id", "suggester_profile_id", "voter_profile_id",
	"impersonator_user_id", "created_by_user_id", "updated_by_user_id", "approved_by_user_id", "downloaded_by", "original_user_id", "silo_user_id",
	"accepted_user_id", "invited_by", "created_by", "requested_by_user_id", "linking_user_id", "host_user_id", "suggester_user_id",
}

func canonicalizationDependencies(ctx context.Context, q canonicalizationQuerier, sourceID int, sourceProfile string) (map[string]int, error) {
	rows, err := q.Query(ctx, `
		SELECT relation.relname, attribute.attname
		FROM pg_class relation
		JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		JOIN pg_attribute attribute ON attribute.attrelid = relation.oid
		WHERE namespace.nspname = 'public'
		  AND relation.relkind IN ('r', 'p')
		  AND NOT relation.relispartition
		  AND attribute.attname = ANY($1)
		  AND attribute.attnum > 0
		  AND NOT attribute.attisdropped`, canonicalizationOwnershipColumns)
	if err != nil {
		return nil, fmt.Errorf("list canonicalization ownership columns: %w", err)
	}
	defer rows.Close()
	columns := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, fmt.Errorf("scan canonicalization ownership column: %w", err)
		}
		columns[table] = append(columns[table], column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate canonicalization ownership columns: %w", err)
	}
	dependencies := make(map[string]int, len(columns))
	for table, tableColumns := range columns {
		predicates := make([]string, 0, len(tableColumns))
		args := make([]any, 0, len(tableColumns))
		for _, column := range tableColumns {
			predicates = append(predicates, pgx.Identifier{column}.Sanitize()+"::text = $"+strconv.Itoa(len(args)+1))
			if canonicalizationProfileColumn(column) {
				args = append(args, sourceProfile)
			} else {
				args = append(args, strconv.Itoa(sourceID))
			}
		}
		query := "SELECT COUNT(*) FROM " + pgx.Identifier{"public", table}.Sanitize() + " WHERE " + strings.Join(predicates, " OR ")
		var count int
		if err := q.QueryRow(ctx, query, args...).Scan(&count); err != nil {
			return nil, fmt.Errorf("count canonicalization dependency %s: %w", table, err)
		}
		dependencies[table] = count
	}
	return dependencies, nil
}

func canonicalizationProfileColumn(column string) bool {
	switch column {
	case "profile_id", "silo_profile_id", "default_profile_id", "host_profile_id", "suggester_profile_id", "voter_profile_id":
		return true
	default:
		return false
	}
}

func canonicalizationSupportedDependency(table string) bool {
	switch table {
	case "abs_sessions", "activity_log", "api_keys", "auth_sessions", "device_login_requests", "external_authorization_audit", "external_authorization_states", "jellycompat_playback_sessions", "jellycompat_sessions", "oauth_session", "operational_logs", "plugin_auth_identities", "policy_decisions", "profile_series_interest", "user_favorites", "user_profile_onboarding", "user_profiles":
		return true
	default:
		return false
	}
}

func init() {
	for _, table := range canonicalizationAdditionalOwnershipTables {
		canonicalizationOwnershipTables[table] = struct{}{}
	}
	registered := make(map[string]struct{}, len(canonicalizationDependencyRegistry))
	for _, dependency := range canonicalizationDependencyRegistry {
		registered[dependency.name] = struct{}{}
	}
	for table := range canonicalizationOwnershipTables {
		if _, exists := registered[table]; !exists {
			canonicalizationDependencyRegistry = append(canonicalizationDependencyRegistry, canonicalizationDependency{name: table, blocking: !canonicalizationSupportedDependency(table)})
		}
	}
}

var canonicalizationAdditionalOwnershipTables = []string{
	"activity_log", "auth_provider_policy_audit", "device_login_requests", "downloaded_subtitles", "history_import_user_mappings", "invitations", "invite_codes", "jellycompat_sessions", "literary_work_match_decisions", "media_group_overrides", "media_identity_overrides", "media_request_events", "media_requests", "media_root_overrides", "notification_preferences", "oauth_session", "operational_logs", "plex_sync_actor_mappings", "policy_decisions", "policy_document_versions", "user_profiles", "watch_together_rooms", "watch_together_suggestions", "watch_together_votes", "webhook_sync_profile_mappings",
}

type canonicalizationDependency struct {
	name, countQuery string
	blocking         bool
}

var canonicalizationDependencyRegistry = []canonicalizationDependency{
	{"user_favorites", `SELECT COUNT(*) FROM user_favorites WHERE user_id = $1`, false},
	{"profile_series_interest", `SELECT COUNT(*) FROM profile_series_interest WHERE user_id = $1`, false},
	{"plugin_auth_identities", `SELECT COUNT(*) FROM plugin_auth_identities WHERE user_id = $1`, false},
	{"external_authorization_audit", `SELECT COUNT(*) FROM external_authorization_audit WHERE user_id = $1`, false},
	{"external_authorization_states", `SELECT COUNT(*) FROM external_authorization_states WHERE user_id = $1`, false},
	{"auth_sessions", `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1`, false},
	{"api_keys", `SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, false},
	{"auth_provider_policy_audit", `SELECT COUNT(*) FROM auth_provider_policy_audit WHERE actor_user_id = $1`, true},
	{"external_identity_link_audit", `SELECT COUNT(*) FROM external_identity_link_audit WHERE user_id = $1`, true},
	{"user_watchlist", `SELECT COUNT(*) FROM user_watchlist WHERE user_id = $1`, true},
	{"user_watch_progress", `SELECT COUNT(*) FROM user_watch_progress WHERE user_id = $1`, true},
	{"user_personal_collections", `SELECT COUNT(*) FROM user_personal_collections WHERE user_id = $1`, true},
}

var canonicalizationOwnershipTables = map[string]struct{}{
	"api_keys": {}, "auth_sessions": {}, "external_authorization_audit": {}, "external_authorization_states": {}, "external_identity_link_audit": {}, "plugin_auth_identities": {}, "profile_series_interest": {},
	"abs_bookmarks": {}, "abs_playback_sessions": {}, "abs_rss_feeds": {}, "abs_sessions": {}, "activity_log": {}, "activity_log_default": {}, "activity_log_p_20260803": {}, "activity_log_p_20260810": {}, "activity_log_p_20260817": {}, "client_diagnostic_reports": {}, "download_subscriptions": {}, "downloads": {}, "ebook_reader_annotations": {}, "ebook_reader_config": {}, "ebook_reader_progress": {}, "history_import_connect_sessions": {}, "history_import_plex_sessions": {}, "history_import_runs": {}, "jellycompat_displayprefs": {}, "jellycompat_playback_sessions": {}, "marker_edit_audit": {}, "notification_deliveries": {}, "notification_discord_link_state": {}, "notification_discord_prefs": {}, "notification_email_prefs": {}, "notification_webhooks": {}, "operational_logs": {}, "operational_logs_default": {}, "operational_logs_p_20260805": {}, "operational_logs_p_20260806": {}, "operational_logs_p_20260807": {}, "operational_logs_p_20260808": {}, "playback_history_admin": {}, "playback_route_events": {}, "playback_sessions_sync": {}, "playback_v3_attempts": {}, "plex_sync_connections": {}, "policy_decisions": {}, "policy_decisions_default": {}, "push_devices": {}, "recommendation_cache": {}, "request_user_limits": {}, "user_aggregates": {}, "user_audio_preferences": {}, "user_collection_groups": {}, "user_device_settings": {}, "user_devices": {}, "user_downloads": {}, "user_favorites": {}, "user_history_hidden_items": {}, "user_home_item_dismissals": {}, "user_library_playback_preferences": {}, "user_personal_collection_items": {}, "user_personal_collection_profiles": {}, "user_personal_collections": {}, "user_playback_sessions": {}, "user_profile_allowed_libraries": {}, "user_profile_onboarding": {}, "user_profiles": {}, "user_ratings": {}, "user_series_playback_preferences": {}, "user_setting_migration_rejects": {}, "user_setting_mutations": {}, "user_setting_values": {}, "user_settings": {}, "user_subtitle_preferences": {}, "user_taste_clusters": {}, "user_taste_profiles": {}, "user_watch_history": {}, "user_watch_progress": {}, "user_watchlist": {}, "watch_provider_auth_sessions": {}, "watch_provider_connections": {}, "web_push_subscriptions": {}, "webhook_sync_connections": {},
}
