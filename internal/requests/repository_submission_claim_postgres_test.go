package requests

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryClaimRequestSubmission_serializesConcurrentClaimsAndTakesOverStaleLease(t *testing.T) {
	// Given a real PostgreSQL media request with no submission claim.
	ctx := context.Background()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UTC().UnixNano()
	username := fmt.Sprintf("todo4-claim-%d", suffix)
	requestID := fmt.Sprintf("todo4-request-%d", suffix)
	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, 'test-only', 'user')
		RETURNING id`, username, username+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_requests WHERE id = $1`, requestID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_requests (
			id, provider, media_type, provider_item_id, title, status, outcome,
			requested_by_user_id, is_anime
		) VALUES ($1, 'audiobook-metadata', 'audiobook', $2, 'Claim test', 'approved', 'active', $3, false)`,
		requestID, requestID, userID); err != nil {
		t.Fatalf("seed media request: %v", err)
	}

	repo := NewRepository(pool, nil)
	claimTime := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, claimErr := repo.ClaimRequestSubmission(ctx, requestID, requestID, claimTime, 5*time.Minute)
			if claimErr != nil {
				t.Errorf("concurrent claim: %v", claimErr)
			}
			results <- claimed
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	// Then exactly one database transaction wins the simultaneous claim.
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("simultaneous claim winners = %d, want 1", winners)
	}

	// When the winning lease becomes stale, another process reclaims the same key.
	staleTime := claimTime.Add(5*time.Minute + time.Second)
	_, claimed, err := repo.ClaimRequestSubmission(ctx, requestID, requestID, staleTime, 5*time.Minute)
	if err != nil {
		t.Fatalf("stale lease takeover: %v", err)
	}
	if !claimed {
		t.Fatal("stale lease was not reclaimable")
	}

	// Then the durable key and in-flight state remain stable for provider replay.
	var key, state string
	if err := pool.QueryRow(ctx, `SELECT fulfillment_key, submission_state FROM media_requests WHERE id = $1`, requestID).Scan(&key, &state); err != nil {
		t.Fatalf("read submission claim: %v", err)
	}
	if key != requestID || state != SubmissionStateInFlight {
		t.Fatalf("stored claim = key:%q state:%q, want %q/%q", key, state, requestID, SubmissionStateInFlight)
	}
}
