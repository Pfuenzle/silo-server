package plugins

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/externalgroups"
)

var (
	ErrAuthGroupMappingInvalid             = errors.New("plugin auth group mapping is invalid")
	ErrAuthGroupMappingConflict            = errors.New("plugin auth group mapping conflicts")
	ErrAuthGroupMappingAccessGroupNotFound = errors.New("plugin auth group mapping access group not found")
)

type AuthGroupMapping struct {
	InstallationID  int
	ExternalGroupID string
	TargetRole      *string
	AccessGroupID   *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AuthGroupMappingInput struct {
	ExternalGroupID string
	TargetRole      *string
	AccessGroupID   *int64
}

type AuthGroupMappingStore struct {
	pool *pgxpool.Pool
}

func NewAuthGroupMappingStore(pool *pgxpool.Pool) *AuthGroupMappingStore {
	return &AuthGroupMappingStore{pool: pool}
}

func ValidateAuthGroupMappingBatch(inputs []AuthGroupMappingInput) ([]AuthGroupMappingInput, error) {
	normalized := make([]AuthGroupMappingInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		externalGroupID, err := externalgroups.NormalizeID(input.ExternalGroupID)
		if err != nil {
			return nil, fmt.Errorf("external group id %q: %w", input.ExternalGroupID, ErrAuthGroupMappingInvalid)
		}
		if _, exists := seen[externalGroupID]; exists {
			return nil, fmt.Errorf("external group id %q: %w", externalGroupID, ErrAuthGroupMappingConflict)
		}
		seen[externalGroupID] = struct{}{}

		role, err := normalizeAuthGroupMappingRole(input.TargetRole)
		if err != nil {
			return nil, err
		}
		if role == nil && input.AccessGroupID == nil {
			return nil, fmt.Errorf("external group id %q has no target: %w", externalGroupID, ErrAuthGroupMappingInvalid)
		}
		normalized = append(normalized, AuthGroupMappingInput{
			ExternalGroupID: externalGroupID,
			TargetRole:      role,
			AccessGroupID:   input.AccessGroupID,
		})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ExternalGroupID < normalized[j].ExternalGroupID })
	return normalized, nil
}

func NormalizeAuthGroupIDs(inputs []string) ([]string, error) {
	ids := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		id, err := externalgroups.NormalizeID(input)
		if err != nil {
			return nil, fmt.Errorf("external group id %q: %w", input, ErrAuthGroupMappingInvalid)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("external group id %q: %w", id, ErrAuthGroupMappingConflict)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func normalizeAuthGroupMappingRole(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	role := strings.TrimSpace(*raw)
	switch role {
	case "user", "admin":
		return &role, nil
	default:
		return nil, fmt.Errorf("target role %q: %w", *raw, ErrAuthGroupMappingInvalid)
	}
}

func (s *AuthGroupMappingStore) List(ctx context.Context, installationID int) ([]AuthGroupMapping, error) {
	return listAuthGroupMappings(ctx, s.pool, installationID)
}

func (s *AuthGroupMappingStore) Replace(
	ctx context.Context,
	installationID int,
	inputs []AuthGroupMappingInput,
) ([]AuthGroupMapping, error) {
	normalized, err := ValidateAuthGroupMappingBatch(inputs)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning plugin auth group mapping replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockAuthGroupMappingInstallation(ctx, tx, installationID); err != nil {
		return nil, err
	}
	if err := validateAuthGroupMappingAccessGroups(ctx, tx, normalized); err != nil {
		return nil, err
	}
	existing, err := listAuthGroupMappings(ctx, tx, installationID)
	if err != nil {
		return nil, err
	}
	if authGroupMappingsEqual(existing, normalized) {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing unchanged plugin auth group mappings: %w", err)
		}
		return existing, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1`, installationID); err != nil {
		return nil, fmt.Errorf("clearing plugin auth group mappings: %w", err)
	}
	for _, mapping := range normalized {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_auth_group_mappings (
				plugin_installation_id, external_group_id, target_role, access_group_id
			) VALUES ($1, $2, $3, $4)`, installationID, mapping.ExternalGroupID, mapping.TargetRole, mapping.AccessGroupID); err != nil {
			return nil, fmt.Errorf("creating plugin auth group mapping: %w", err)
		}
	}
	if err := demoteExternalAuthorization(ctx, tx, installationID, nil, "mapping_changed"); err != nil {
		return nil, err
	}
	mappings, err := listAuthGroupMappings(ctx, tx, installationID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing plugin auth group mapping replacement: %w", err)
	}
	return mappings, nil
}

func authGroupMappingsEqual(existing []AuthGroupMapping, inputs []AuthGroupMappingInput) bool {
	if len(existing) != len(inputs) {
		return false
	}
	for i := range existing {
		if existing[i].ExternalGroupID != inputs[i].ExternalGroupID || !sameString(existing[i].TargetRole, inputs[i].TargetRole) || !sameInt64(existing[i].AccessGroupID, inputs[i].AccessGroupID) {
			return false
		}
	}
	return true
}

func sameString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func sameInt64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func lockAuthGroupMappingInstallation(ctx context.Context, tx pgx.Tx, installationID int) error {
	var lockedID int
	if err := tx.QueryRow(ctx, `SELECT id FROM plugin_installations WHERE id = $1 FOR UPDATE`, installationID).Scan(&lockedID); err != nil {
		return fmt.Errorf("locking plugin installation for auth group mappings: %w", err)
	}
	return nil
}

type authGroupMappingQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listAuthGroupMappings(ctx context.Context, db authGroupMappingQuerier, installationID int) ([]AuthGroupMapping, error) {
	rows, err := db.Query(ctx, `
		SELECT plugin_installation_id, external_group_id, target_role, access_group_id, created_at, updated_at
		FROM plugin_auth_group_mappings
		WHERE plugin_installation_id = $1
		ORDER BY external_group_id ASC`, installationID)
	if err != nil {
		return nil, fmt.Errorf("listing plugin auth group mappings: %w", err)
	}
	defer rows.Close()

	mappings := make([]AuthGroupMapping, 0)
	for rows.Next() {
		mapping, err := scanAuthGroupMapping(rows)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin auth group mappings: %w", err)
	}
	return mappings, nil
}

func validateAuthGroupMappingAccessGroups(ctx context.Context, tx pgx.Tx, mappings []AuthGroupMappingInput) error {
	checked := make(map[int64]struct{})
	for _, mapping := range mappings {
		if mapping.AccessGroupID == nil {
			continue
		}
		if _, exists := checked[*mapping.AccessGroupID]; exists {
			continue
		}
		checked[*mapping.AccessGroupID] = struct{}{}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_groups WHERE id = $1)`, *mapping.AccessGroupID).Scan(&exists); err != nil {
			return fmt.Errorf("checking plugin auth group mapping access group: %w", err)
		}
		if !exists {
			return fmt.Errorf("access group %d: %w", *mapping.AccessGroupID, ErrAuthGroupMappingAccessGroupNotFound)
		}
	}
	return nil
}

func scanAuthGroupMapping(row interface{ Scan(...any) error }) (AuthGroupMapping, error) {
	var mapping AuthGroupMapping
	if err := row.Scan(
		&mapping.InstallationID,
		&mapping.ExternalGroupID,
		&mapping.TargetRole,
		&mapping.AccessGroupID,
		&mapping.CreatedAt,
		&mapping.UpdatedAt,
	); err != nil {
		return AuthGroupMapping{}, fmt.Errorf("scanning plugin auth group mapping: %w", err)
	}
	return mapping, nil
}
