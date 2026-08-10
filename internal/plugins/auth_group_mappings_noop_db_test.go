package plugins

import (
	"context"
	"testing"
)

func TestAuthGroupMapping_ReplaceIdenticalBatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	store := NewAuthGroupMappingStore(pool)
	admin := "admin"
	input := []AuthGroupMappingInput{{ExternalGroupID: "  directory-admin  ", TargetRole: &admin}}
	created, err := store.Replace(ctx, installationID, input)
	if err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("seed mappings = %#v", created)
	}
	if _, err := pool.Exec(ctx, `UPDATE plugin_auth_group_mappings SET updated_at = '2001-01-01T00:00:00Z' WHERE plugin_installation_id = $1`, installationID); err != nil {
		t.Fatalf("set mapping timestamp sentinel: %v", err)
	}

	unchanged, err := store.Replace(ctx, installationID, input)
	if err != nil {
		t.Fatalf("replace identical mappings: %v", err)
	}
	if len(unchanged) != 1 || unchanged[0].ExternalGroupID != "directory-admin" {
		t.Fatalf("unchanged mappings = %#v", unchanged)
	}
	if got := unchanged[0].UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2001-01-01T00:00:00Z" {
		t.Fatalf("identical replacement rewrote mapping: updated_at=%s", got)
	}
}
