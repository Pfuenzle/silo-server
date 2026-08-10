package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// InstallationDependencyConflictError deliberately reports only aggregate
// durable-authentication dependency counts, never sensitive record data.
type InstallationDependencyConflictError struct {
	Identities           int `json:"identities"`
	ProviderSessions     int `json:"provider_sessions"`
	GroupMappings        int `json:"group_mappings"`
	AuthorizationAudits  int `json:"authorization_audits"`
	IdentityLinkAudits   int `json:"identity_link_audits"`
	ProviderPolicyAudits int `json:"provider_policy_audits"`
}

func (e *InstallationDependencyConflictError) Error() string {
	return "plugin installation has dependent authentication data"
}

func (e *InstallationDependencyConflictError) Empty() bool {
	return e.Identities == 0 && e.ProviderSessions == 0 && e.GroupMappings == 0 && e.AuthorizationAudits == 0 && e.IdentityLinkAudits == 0 && e.ProviderPolicyAudits == 0
}

func (s *InstallationStore) Delete(ctx context.Context, id int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete installation transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	installation, err := scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM plugin_installations WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if installation.IsBuiltin() {
		return ErrBuiltinInstallationImmutable
	}
	dependencies, err := installationDependencyCounts(ctx, tx, id)
	if err != nil {
		return err
	}
	if !dependencies.Empty() {
		return dependencies
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
		return fmt.Errorf("deleting plugin installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete installation transaction: %w", err)
	}
	if err := os.RemoveAll(filepath.Dir(installation.InstallPath)); err != nil {
		return fmt.Errorf("removing plugin installation files: %w", err)
	}
	return nil
}

// Disable serializes with plugin login/mapping changes through the shared
// installation row lock before invalidating every provider-backed session.
func (s *InstallationStore) Disable(ctx context.Context, id int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin disable installation transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	installation, err := scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM plugin_installations WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if installation.IsBuiltin() {
		return ErrBuiltinInstallationImmutable
	}
	if !installation.Enabled {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET enabled = false, updated_at = NOW() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("disabling plugin installation: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE provider_key LIKE $1 AND revoked_at IS NULL`, fmt.Sprintf("plugin:%d:%%", id)); err != nil {
		return fmt.Errorf("revoking plugin provider sessions: %w", err)
	}
	if err := demoteExternalAuthorization(ctx, tx, id, nil, "installation_disabled"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit disable installation transaction: %w", err)
	}
	return nil
}

func installationDependencyCounts(ctx context.Context, tx pgx.Tx, installationID int) (*InstallationDependencyConflictError, error) {
	counts := &InstallationDependencyConflictError{}
	err := tx.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM plugin_auth_identities WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM auth_sessions WHERE provider_key LIKE $2),
			(SELECT COUNT(*) FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM external_authorization_audit WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM external_identity_link_audit WHERE plugin_installation_id = $1),
			(SELECT COUNT(*) FROM auth_provider_policy_audit WHERE plugin_installation_id = $1)`,
		installationID,
		fmt.Sprintf("plugin:%d:%%", installationID),
	).Scan(&counts.Identities, &counts.ProviderSessions, &counts.GroupMappings, &counts.AuthorizationAudits, &counts.IdentityLinkAudits, &counts.ProviderPolicyAudits)
	if err != nil {
		return nil, fmt.Errorf("counting plugin installation dependencies: %w", err)
	}
	return counts, nil
}
