package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPluginIdentityRepository_TransactionLockReleasesWhenWaiterCancelled(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	secondPool, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("create waiter pool: %v", err)
	}
	t.Cleanup(secondPool.Close)
	repository := NewPluginIdentityRepository(pool)
	waiterRepository := NewPluginIdentityRepository(secondPool)
	key := PluginIdentityKey{InstallationID: 987, ExternalSubject: "cancelled-waiter"}
	locked := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	holderPID := make(chan int, 1)
	go func() {
		firstDone <- repository.withProvisioningTransaction(ctx, key, func(ctx context.Context, tx pgx.Tx) error {
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return fmt.Errorf("load holder backend PID: %w", err)
			}
			holderPID <- pid
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	firstPID := <-holderPID
	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	defer cancelWaiter()
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- waiterRepository.withProvisioningTransaction(waiterCtx, key, func(context.Context, pgx.Tx) error {
			return errors.New("waiter unexpectedly acquired the advisory lock")
		})
	}()
	waiterPID, err := waitForAdvisoryLockWaiter(ctx, pool, pluginIdentityLockKey(key))
	if err != nil {
		close(release)
		<-firstDone
		t.Fatal(err)
	}
	if firstPID == waiterPID {
		close(release)
		<-firstDone
		t.Fatalf("holder and waiting backend PIDs = %d, want distinct connections", firstPID)
	}
	t.Logf("holder PID=%d waiter PID=%d observed blocked on advisory lock", firstPID, waiterPID)

	// When
	cancelWaiter()
	err = <-waiterDone
	close(release)
	if firstErr := <-firstDone; firstErr != nil {
		t.Fatalf("lock holder error: %v", firstErr)
	}

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock waiter error = %v, want context.Canceled", err)
	}
	verifyCtx, cancelVerify := context.WithTimeout(ctx, time.Second)
	defer cancelVerify()
	if err := repository.withProvisioningTransaction(verifyCtx, key, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("lock remained after cancellation: %v", err)
	}
}

func waitForAdvisoryLockWaiter(ctx context.Context, pool *pgxpool.Pool, lockKey int64) (int, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID int
		err := pool.QueryRow(deadlineCtx, `
			SELECT COALESCE(MIN(pid), 0)
			FROM pg_locks
			WHERE locktype = 'advisory'
			  AND classid = $1::bigint::oid
			  AND objid = $2::bigint::oid
			  AND NOT granted`, uint32(uint64(lockKey)>>32), uint32(lockKey),
		).Scan(&waiterPID)
		if err != nil {
			return 0, fmt.Errorf("observe advisory lock waiter: %w", err)
		}
		if waiterPID != 0 {
			return waiterPID, nil
		}
		select {
		case <-deadlineCtx.Done():
			return 0, fmt.Errorf("wait for advisory lock waiter: %w", deadlineCtx.Err())
		case <-ticker.C:
		}
	}
}
