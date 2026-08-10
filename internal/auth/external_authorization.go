package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
)

var ErrExternalAuthorizationConflict = errors.New("external authorization mappings conflict")

type externalAuthorizationTarget struct {
	role          string
	accessGroupID *int64
}

func (p *PluginProvider) reconcileExternalAuthorization(ctx context.Context, user *models.User, claims *structpb.Struct) (*models.User, error) {
	return p.completePluginLogin(ctx, user, claims, nil)
}

func (p *PluginProvider) completePluginLogin(ctx context.Context, user *models.User, claims *structpb.Struct, session *models.AuthSession) (*models.User, error) {
	if user == nil {
		return nil, ErrInvalidCredentials
	}
	if strings.TrimSpace(p.config.CapabilityID) == "" {
		if session != nil {
			session.UserID = user.ID
			if err := p.sessions.Create(ctx, *session); err != nil {
				return nil, err
			}
		}
		return user, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning plugin login completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	installationEnabled, err := lockPluginInstallation(ctx, tx, p.config.InstallationID)
	if err != nil {
		return nil, err
	}
	if !installationEnabled {
		return nil, ErrInvalidCredentials
	}
	var mode string
	if err := tx.QueryRow(ctx, `SELECT authorization_mode FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2 AND enabled FOR UPDATE`, p.config.InstallationID, p.config.CapabilityID).Scan(&mode); err != nil {
		return nil, ErrInvalidCredentials
	}
	user, err = externalAuthorizationUser(ctx, tx, user.ID)
	if err != nil {
		return nil, err
	}
	if mode == "external_groups_v1" {
		if p.config.AuthMode != "credentials" {
			return nil, ErrInvalidCredentials
		}
		groups, parseErr := ParseExternalGroupsV1(claims)
		if parseErr != nil {
			return nil, ErrInvalidCredentials
		}
		user, err = p.reconcileLDAPGroupsTx(ctx, tx, user, groups)
		if err != nil {
			return nil, err
		}
	} else if mode != "none" {
		return nil, ErrInvalidCredentials
	}
	if session != nil {
		session.UserID = user.ID
		if err := p.sessions.createWithQuerier(ctx, tx, *session); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing plugin login completion: %w", err)
	}
	return user, nil
}

func (p *PluginProvider) reconcileLDAPGroupsTx(ctx context.Context, tx pgx.Tx, user *models.User, groups []ExternalGroup) (*models.User, error) {
	target, matched, err := externalAuthorizationTargetFor(ctx, tx, p.config.InstallationID, groups)
	if err != nil {
		return nil, err
	}
	changed := user.Role != target.role || !sameAccessGroup(user.AccessGroupID, target.accessGroupID)
	if changed {
		if _, err := tx.Exec(ctx, `UPDATE users SET role = $2, access_group_id = $3, access_policy_revision = access_policy_revision + 1, updated_at = NOW() WHERE id = $1`, user.ID, target.role, target.accessGroupID); err != nil {
			return nil, fmt.Errorf("updating external authorization: %w", err)
		}
		if err := revokeUserAuthorizationSessions(ctx, tx, user.ID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, old_access_group_id, new_access_group_id, matched_group_ids, reason, correlation_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, user.ID, p.config.InstallationID, p.config.CapabilityID, user.Role, target.role, user.AccessGroupID, target.accessGroupID, matched, "ldap_login", uuid.New()); err != nil {
		return nil, fmt.Errorf("auditing external authorization: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO external_authorization_states (user_id, plugin_installation_id, capability_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, user.ID, p.config.InstallationID, p.config.CapabilityID); err != nil {
		return nil, fmt.Errorf("recording external authorization ownership: %w", err)
	}
	user.Role = target.role
	user.AccessGroupID = target.accessGroupID
	return user, nil
}

func lockPluginInstallation(ctx context.Context, tx pgx.Tx, installationID int) (bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM plugin_installations WHERE id = $1 FOR UPDATE`, installationID).Scan(&enabled); err != nil {
		return false, fmt.Errorf("locking plugin installation for authentication: %w", err)
	}
	return enabled, nil
}

func revokeUserAuthorizationSessions(ctx context.Context, tx pgx.Tx, userID int) error {
	for _, query := range []string{
		`UPDATE auth_sessions SET revoked_at = NOW() WHERE revoked_at IS NULL AND (user_id = $1 OR impersonator_user_id = $1)`,
		`DELETE FROM jellycompat_playback_sessions WHERE user_id = ($1::bigint)::text`,
		`DELETE FROM jellycompat_sessions WHERE streamapp_user_id = $1`,
		`UPDATE abs_sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, query, userID); err != nil {
			return fmt.Errorf("revoking externally managed sessions: %w", err)
		}
	}
	return nil
}

func externalAuthorizationTargetFor(ctx context.Context, tx pgx.Tx, installationID int, groups []ExternalGroup) (externalAuthorizationTarget, []string, error) {
	var defaultGroupID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM access_groups WHERE is_default FOR KEY SHARE`).Scan(&defaultGroupID); err != nil {
		return externalAuthorizationTarget{}, nil, fmt.Errorf("loading default access group: %w", err)
	}
	defaultTarget := externalAuthorizationTarget{role: "user", accessGroupID: &defaultGroupID}
	target := defaultTarget
	matchedTarget := false
	matched := make([]string, 0, len(groups))
	for _, group := range groups {
		var role *string
		var accessGroupID *int64
		err := tx.QueryRow(ctx, `SELECT target_role, access_group_id FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1 AND external_group_id = $2`, installationID, group.ID).Scan(&role, &accessGroupID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return externalAuthorizationTarget{}, nil, fmt.Errorf("loading external authorization mapping: %w", err)
		}
		matched = append(matched, group.ID)
		candidate := defaultTarget
		if role != nil {
			candidate.role = *role
		}
		if accessGroupID != nil {
			candidate.accessGroupID = accessGroupID
		}
		if matchedTarget && (candidate.role != target.role || !sameAccessGroup(candidate.accessGroupID, target.accessGroupID)) {
			return externalAuthorizationTarget{}, nil, ErrExternalAuthorizationConflict
		}
		target = candidate
		matchedTarget = true
	}
	return target, matched, nil
}

func externalAuthorizationUser(ctx context.Context, tx pgx.Tx, userID int) (*models.User, error) {
	user, err := scanUser(tx.QueryRow(ctx, `SELECT `+allColumns+` FROM users WHERE id = $1 FOR UPDATE`, userID))
	if err != nil {
		return nil, fmt.Errorf("locking external authorization user: %w", err)
	}
	return user, nil
}

func sameAccessGroup(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
