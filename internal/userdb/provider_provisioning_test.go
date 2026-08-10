package userdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteProvider_DeleteUserRemovesClosedDatabaseFile(t *testing.T) {
	// Given
	dataDir := t.TempDir()
	provider := NewSQLiteProvider(NewUserDBPool(PoolConfig{DataDir: dataDir}))
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close provider: %v", err)
		}
	})
	const userID = 4242
	if _, err := provider.ForUser(context.Background(), userID); err != nil {
		t.Fatalf("ForUser() error: %v", err)
	}

	// When
	err := provider.DeleteUser(context.Background(), userID)

	// Then
	if err != nil {
		t.Fatalf("DeleteUser() error: %v", err)
	}
	for _, path := range []string{"4242.db", "4242.db-wal", "4242.db-shm"} {
		_, statErr := os.Stat(filepath.Join(dataDir, path))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("user database %s stat error = %v, want absence", path, statErr)
		}
	}
	t.Log("SQLite user database file absent after failed-provisioning cleanup")
}

func TestUserDBPoolDelete_ReturnsByContextDeadlineThenCleansAfterBlockedClose(t *testing.T) {
	// Given
	pool := NewUserDBPool(PoolConfig{DataDir: t.TempDir()})
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close pool: %v", err)
		}
	})
	const userID = 4243
	userDB, err := pool.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	userDB.close = func() error {
		close(closeStarted)
		<-releaseClose
		return userDB.DB.Close()
	}
	deleteCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()

	// When
	deleteResult := make(chan error, 1)
	go func() { deleteResult <- pool.Delete(deleteCtx, userID) }()
	<-closeStarted
	err = <-deleteResult
	elapsed := time.Since(startedAt)

	// Then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Delete() error = %v, want context deadline exceeded", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Delete() returned after %s, want bounded return", elapsed)
	}
	t.Logf("blocked SQLite close retained database path=%s until release", userDB.Path)
	if _, statErr := os.Stat(userDB.Path); statErr != nil {
		t.Fatalf("database disappeared before blocked close released: %v", statErr)
	}
	if _, err := pool.Get(context.Background(), userID); !errors.Is(err, ErrUserDBDeleting) {
		t.Fatalf("Get() error during cleanup = %v, want ErrUserDBDeleting", err)
	}
	close(releaseClose)
	deadline := time.Now().Add(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, statErr := os.Stat(userDB.Path)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("database remained after blocked close released: %v", statErr)
		}
		<-ticker.C
	}
}

func TestSQLiteProvider_DeleteUserRemovesMaterializedWALAndSHM(t *testing.T) {
	// Given
	dataDir := t.TempDir()
	provider := NewSQLiteProvider(NewUserDBPool(PoolConfig{DataDir: dataDir}))
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close provider: %v", err)
		}
	})
	const userID = 4244
	store, err := provider.ForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("ForUser() error: %v", err)
	}
	if err := store.CreateProfile(context.Background(), Profile{ID: "sidecars", Name: "sidecars", ShowForcedSubtitles: true}); err != nil {
		t.Fatalf("CreateProfile() error: %v", err)
	}
	paths := []string{filepath.Join(dataDir, "4244.db"), filepath.Join(dataDir, "4244.db-wal"), filepath.Join(dataDir, "4244.db-shm")}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("materialized SQLite sidecar %s: %v", path, err)
		}
	}
	t.Logf("materialized SQLite paths: db=%s wal=%s shm=%s", paths[0], paths[1], paths[2])

	// When
	err = provider.DeleteUser(context.Background(), userID)

	// Then
	if err != nil {
		t.Fatalf("DeleteUser() error: %v", err)
	}
	for _, path := range paths {
		_, statErr := os.Stat(path)
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("SQLite path %s stat error = %v, want absence", path, statErr)
		}
	}
}

func TestSQLiteProvider_CanonicalizationStoreStateReportsAbsentAndPresentFiles(t *testing.T) {
	// Given
	dataDir := t.TempDir()
	provider := NewSQLiteProvider(NewUserDBPool(PoolConfig{DataDir: dataDir}))
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close provider: %v", err)
		}
	})
	const userID = 4245

	// When
	absent, err := provider.CanonicalizationStoreState(context.Background(), userID)
	if err != nil {
		t.Fatalf("inspect absent canonicalization store: %v", err)
	}
	if _, err := provider.ForUser(context.Background(), userID); err != nil {
		t.Fatalf("materialize user store: %v", err)
	}
	present, err := provider.CanonicalizationStoreState(context.Background(), userID)

	// Then
	if err != nil {
		t.Fatalf("inspect present canonicalization store: %v", err)
	}
	if !absent.Proven || !absent.Empty || !present.Proven || present.Empty {
		t.Fatalf("canonicalization states = absent:%+v present:%+v", absent, present)
	}
}
