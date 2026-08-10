package userdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SQLiteProvider implements userstore.UserStoreProvider using the SQLite pool.
type SQLiteProvider struct {
	pool *UserDBPool
}

// NewSQLiteProvider wraps an existing UserDBPool as a UserStoreProvider.
func NewSQLiteProvider(pool *UserDBPool) *SQLiteProvider {
	return &SQLiteProvider{pool: pool}
}

// Compile-time interface check.
var _ userstore.UserStoreProvider = (*SQLiteProvider)(nil)
var _ userstore.ProvisioningCleanupProvider = (*SQLiteProvider)(nil)
var _ userstore.TransactionalProvisioningProvider = (*SQLiteProvider)(nil)
var _ userstore.CanonicalizationStoreStateProvider = (*SQLiteProvider)(nil)

// SupportsTransactionalProvisioning reports that per-user SQLite state cannot
// participate in the PostgreSQL external-login transaction.
func (*SQLiteProvider) SupportsTransactionalProvisioning() bool { return false }

// ForUser returns a SQLiteUserStore for the given user.
func (p *SQLiteProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	udb, err := p.pool.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	return NewSQLiteUserStore(udb.DB), nil
}

// Close closes the underlying pool.
func (p *SQLiteProvider) Close() error {
	return p.pool.Close()
}

// DeleteUser removes all SQLite state for a failed account-provisioning attempt.
func (p *SQLiteProvider) DeleteUser(ctx context.Context, userID int) error {
	return p.pool.Delete(ctx, userID)
}

// CanonicalizationStoreState proves whether the per-user SQLite database and
// its WAL sidecars are absent. A canonicalization may delete only proven-empty
// state; it must not open the database because opening it would create it.
func (p *SQLiteProvider) CanonicalizationStoreState(_ context.Context, userID int) (userstore.CanonicalizationStoreState, error) {
	if p == nil || p.pool == nil || userID <= 0 {
		return userstore.CanonicalizationStoreState{Kind: userstore.CanonicalizationStoreSQLite}, fmt.Errorf("SQLite canonicalization store is unavailable")
	}
	path := filepath.Join(p.pool.config.DataDir, fmt.Sprintf("%d.db", userID))
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(candidate); err == nil {
			return userstore.CanonicalizationStoreState{Proven: true, Kind: userstore.CanonicalizationStoreSQLite}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return userstore.CanonicalizationStoreState{}, fmt.Errorf("inspect SQLite canonicalization state: %w", err)
		}
	}
	return userstore.CanonicalizationStoreState{Proven: true, Empty: true, Kind: userstore.CanonicalizationStoreSQLite}, nil
}

// Pin marks a userID as having active playback (SQLite-specific).
func (p *SQLiteProvider) Pin(userID int) {
	p.pool.Pin(userID)
}

// Unpin removes the active-playback mark (SQLite-specific).
func (p *SQLiteProvider) Unpin(userID int) {
	p.pool.Unpin(userID)
}
