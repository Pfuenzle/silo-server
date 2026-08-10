package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func authGroupMappingTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedAuthGroupMappingInstallation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var installationID int
	pluginID := fmt.Sprintf("store-auth-group-mapping-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '0', '/nonexistent/auth-group-mapping-test')
		RETURNING id`, pluginID).Scan(&installationID)
	if err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		tag, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID)
		if err != nil || tag.RowsAffected() != 1 {
			t.Errorf("cleanup installation %d: rows=%d err=%v", installationID, tag.RowsAffected(), err)
		}
		var mappings, installations int
		if err := pool.QueryRow(context.Background(), `SELECT (SELECT COUNT(*) FROM plugin_auth_group_mappings WHERE plugin_installation_id = $1), (SELECT COUNT(*) FROM plugin_installations WHERE id = $1)`, installationID).Scan(&mappings, &installations); err != nil {
			t.Errorf("query installation %d residue: %v", installationID, err)
		} else if mappings != 0 || installations != 0 {
			t.Errorf("installation %d residue: mappings=%d installations=%d", installationID, mappings, installations)
		}
	})
	return installationID
}

func seedAuthGroupMappingAccessGroup(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var groupID int64
	name := fmt.Sprintf("store-auth-group-mapping-%d", time.Now().UnixNano())
	err := pool.QueryRow(context.Background(), `
		INSERT INTO access_groups (name, is_default)
		VALUES ($1, false)
		RETURNING id`, name).Scan(&groupID)
	if err != nil {
		t.Fatalf("seed access group: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_auth_group_mappings WHERE access_group_id = $1`, groupID); err != nil {
			t.Errorf("cleanup mappings for access group %d: %v", groupID, err)
		}
		tag, err := pool.Exec(context.Background(), `DELETE FROM access_groups WHERE id = $1`, groupID)
		if err != nil || tag.RowsAffected() != 1 {
			t.Errorf("cleanup access group %d: rows=%d err=%v", groupID, tag.RowsAffected(), err)
		}
		var mappings, groups int
		if err := pool.QueryRow(context.Background(), `SELECT (SELECT COUNT(*) FROM plugin_auth_group_mappings WHERE access_group_id = $1), (SELECT COUNT(*) FROM access_groups WHERE id = $1)`, groupID).Scan(&mappings, &groups); err != nil {
			t.Errorf("query access group %d residue: %v", groupID, err)
		} else if mappings != 0 || groups != 0 {
			t.Errorf("access group %d residue: mappings=%d groups=%d", groupID, mappings, groups)
		}
	})
	return groupID
}

func TestAuthGroupMapping_ReplacePerformsExactCRUDAndRejectsInvalidWritesAtomically(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	accessGroupID := seedAuthGroupMappingAccessGroup(t, pool)
	store := NewAuthGroupMappingStore(pool)
	user := "user"
	admin := "admin"
	ctx := context.Background()

	// When
	created, err := store.Replace(ctx, installationID, []AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-a",
		TargetRole:      &user,
		AccessGroupID:   &accessGroupID,
	}})

	// Then
	if err != nil {
		t.Fatalf("create mappings: %v", err)
	}
	if len(created) != 1 || created[0].ExternalGroupID != "directory-team-a" || created[0].TargetRole == nil || *created[0].TargetRole != "user" || created[0].AccessGroupID == nil || *created[0].AccessGroupID != accessGroupID {
		t.Fatalf("created mappings = %#v", created)
	}

	// When
	updated, err := store.Replace(ctx, installationID, []AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-a",
		TargetRole:      &admin,
	}})

	// Then
	if err != nil {
		t.Fatalf("update mappings: %v", err)
	}
	if len(updated) != 1 || updated[0].TargetRole == nil || *updated[0].TargetRole != "admin" || updated[0].AccessGroupID != nil {
		t.Fatalf("updated mappings = %#v", updated)
	}

	// When
	missingAccessGroupID := accessGroupID + 999999
	_, err = store.Replace(ctx, installationID, []AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-b",
		TargetRole:      &user,
		AccessGroupID:   &missingAccessGroupID,
	}})

	// Then
	if !errors.Is(err, ErrAuthGroupMappingAccessGroupNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
	persisted, err := store.List(ctx, installationID)
	if err != nil {
		t.Fatalf("list after failed replace: %v", err)
	}
	if len(persisted) != 1 || persisted[0].TargetRole == nil || *persisted[0].TargetRole != "admin" {
		t.Fatalf("failed replacement changed mappings: %#v", persisted)
	}

	// When
	_, err = store.Replace(ctx, installationID, []AuthGroupMappingInput{
		{ExternalGroupID: "directory-team-a", TargetRole: &admin},
		{ExternalGroupID: "directory-team-a", TargetRole: &user},
	})

	// Then
	if !errors.Is(err, ErrAuthGroupMappingConflict) {
		t.Fatalf("duplicate mappings error = %v", err)
	}

	// When
	deleted, err := store.Replace(ctx, installationID, nil)

	// Then
	if err != nil {
		t.Fatalf("delete mappings: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted mappings = %#v", deleted)
	}
}

func TestAuthGroupMapping_AccessGroupReferenceRestrictsDeletion(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	accessGroupID := seedAuthGroupMappingAccessGroup(t, pool)
	store := NewAuthGroupMappingStore(pool)
	user := "user"
	if _, err := store.Replace(context.Background(), installationID, []AuthGroupMappingInput{{
		ExternalGroupID: "directory-team-a",
		TargetRole:      &user,
		AccessGroupID:   &accessGroupID,
	}}); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	// When
	_, err := pool.Exec(context.Background(), `DELETE FROM access_groups WHERE id = $1`, accessGroupID)

	// Then
	if err == nil {
		t.Fatal("deleting a referenced access group succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("delete error = %v, want restrict violation", err)
	}
}

func TestAuthGroupMapping_ReplaceKeepsOneWholeBatchWhenIndependentConnectionsRace(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	firstPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect first replacement pool: %v", err)
	}
	t.Cleanup(firstPool.Close)
	secondPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect second replacement pool: %v", err)
	}
	t.Cleanup(secondPool.Close)
	admin := "admin"
	user := "user"
	firstStore := NewAuthGroupMappingStore(firstPool)
	secondStore := NewAuthGroupMappingStore(secondPool)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)

	// When
	go func() {
		defer wait.Done()
		<-start
		_, err := firstStore.Replace(context.Background(), installationID, []AuthGroupMappingInput{{
			ExternalGroupID: "directory-admin",
			TargetRole:      &admin,
		}})
		errs <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := secondStore.Replace(context.Background(), installationID, []AuthGroupMappingInput{{
			ExternalGroupID: "directory-user",
			TargetRole:      &user,
		}})
		errs <- err
	}()
	close(start)
	wait.Wait()
	close(errs)

	// Then
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replacement: %v", err)
		}
	}
	mappings, err := NewAuthGroupMappingStore(pool).List(context.Background(), installationID)
	if err != nil {
		t.Fatalf("list concurrent replacement result: %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("concurrent replacement left union: %#v", mappings)
	}
	mapping := mappings[0]
	if (mapping.ExternalGroupID != "directory-admin" || mapping.TargetRole == nil || *mapping.TargetRole != "admin") && (mapping.ExternalGroupID != "directory-user" || mapping.TargetRole == nil || *mapping.TargetRole != "user") {
		t.Fatalf("concurrent replacement left an incomplete batch: %#v", mapping)
	}
}
