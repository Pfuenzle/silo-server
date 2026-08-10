package plugins

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func demoteExternalAuthorization(ctx context.Context, tx pgx.Tx, installationID int, capabilityID *string, reason string) error {
	var defaultGroupID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM access_groups WHERE is_default FOR KEY SHARE`).Scan(&defaultGroupID); err != nil {
		return fmt.Errorf("loading default access group for external authorization demotion: %w", err)
	}
	query := `SELECT u.id, u.role, u.access_group_id, s.capability_id
		FROM external_authorization_states s
		JOIN users u ON u.id = s.user_id
		WHERE s.plugin_installation_id = $1`
	args := []any{installationID}
	if capabilityID != nil {
		query += ` AND s.capability_id = $2`
		args = append(args, *capabilityID)
	}
	query += ` FOR UPDATE OF u`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("locking externally managed users for demotion: %w", err)
	}
	type managedUser struct {
		userID        int
		role          string
		accessGroupID *int64
		capabilityID  string
	}
	managedUsers := make([]managedUser, 0)
	for rows.Next() {
		var user managedUser
		if err := rows.Scan(&user.userID, &user.role, &user.accessGroupID, &user.capabilityID); err != nil {
			return fmt.Errorf("scanning externally managed user: %w", err)
		}
		managedUsers = append(managedUsers, user)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating externally managed users: %w", err)
	}
	rows.Close()
	for _, user := range managedUsers {
		changed := user.role != "user" || user.accessGroupID == nil || *user.accessGroupID != defaultGroupID
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE users SET role = 'user', access_group_id = $2, access_policy_revision = access_policy_revision + 1, updated_at = NOW() WHERE id = $1`, user.userID, defaultGroupID); err != nil {
				return fmt.Errorf("demoting externally managed user: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, old_access_group_id, new_access_group_id, matched_group_ids, reason, correlation_id) VALUES ($1,$2,$3,$4,'user',$5,$6,'{}',$7,$8)`, user.userID, installationID, user.capabilityID, user.role, user.accessGroupID, defaultGroupID, reason, uuid.New()); err != nil {
			return fmt.Errorf("auditing external authorization demotion: %w", err)
		}
		for _, query := range []string{
			`UPDATE auth_sessions SET revoked_at = NOW() WHERE revoked_at IS NULL AND (user_id = $1 OR impersonator_user_id = $1)`,
			`DELETE FROM jellycompat_playback_sessions WHERE user_id = ($1::bigint)::text`,
			`DELETE FROM jellycompat_sessions WHERE streamapp_user_id = $1`,
			`UPDATE abs_sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, query, user.userID); err != nil {
				return fmt.Errorf("revoking externally managed user sessions: %w", err)
			}
		}
	}
	return nil
}
