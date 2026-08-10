package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestExternalAuthorizationAudit_RejectsNullAndOutOfBoundsGroupIDs(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "audit-bounds")
	args := []any{userID, installationID, "ldap", "user", "user", "bounds", "00000000-0000-0000-0000-000000000001"}
	query := `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, matched_group_ids, reason, correlation_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	cases := []struct {
		name     string
		groupIDs any
	}{
		{name: "null element", groupIDs: []*string{stringPointer("admins"), nil}},
		{name: "too many elements", groupIDs: make([]string, 101)},
		{name: "overlong element", groupIDs: []string{strings.Repeat("a", 257)}},
	}
	tooMany := cases[1].groupIDs.([]string)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("group-%d", i)
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := pool.Exec(ctx, query, append(args[:5:5], tt.groupIDs, args[5], args[6])...)

			// Then
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("insert error = %v, want check violation", err)
			}
		})
	}
}

func TestExternalAuthorizationAudit_PreservesHistoricalGroupIDAfterDeletion(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	installationID := insertPluginProviderTestInstallation(t, ctx, pool)
	userID := insertPluginProviderTestUser(t, ctx, pool, "audit-history")
	var groupID int64
	if err := pool.QueryRow(ctx, `INSERT INTO access_groups (name) VALUES ($1) RETURNING id`, fmt.Sprintf("audit-history-%d", installationID)).Scan(&groupID); err != nil {
		t.Fatalf("create historical access group: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO external_authorization_audit (user_id, plugin_installation_id, capability_id, old_role, new_role, old_access_group_id, matched_group_ids, reason, correlation_id) VALUES ($1,$2,'ldap','admin','user',$3,'{}','mapping_changed','00000000-0000-0000-0000-000000000002')`, userID, installationID, groupID); err != nil {
		t.Fatalf("create historical audit row: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM external_authorization_audit WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("cleanup audit history: %v", err)
		}
	})

	// When
	if _, err := pool.Exec(ctx, `DELETE FROM access_groups WHERE id = $1`, groupID); err != nil {
		t.Fatalf("delete historical access group: %v", err)
	}

	// Then
	var historicalID int64
	if err := pool.QueryRow(ctx, `SELECT old_access_group_id FROM external_authorization_audit WHERE user_id = $1`, userID).Scan(&historicalID); err != nil {
		t.Fatalf("load historical audit row: %v", err)
	}
	if historicalID != groupID {
		t.Fatalf("historical access group ID = %d, want %d", historicalID, groupID)
	}
}

func stringPointer(value string) *string { return &value }
