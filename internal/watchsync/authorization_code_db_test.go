package watchsync

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestAuthorizationCodeSessionsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "authorization-code-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'Authorization code')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("authorization-code-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	otherRepo := NewPostgresRepository(pool, cipher)
	now := time.Now().UTC().Truncate(time.Second)
	newSession := func() DeviceAuthSession {
		t.Helper()
		row, err := deviceAuthSessionFromAuthorizationCode(AuthorizationCodeSession{
			ID: uuid.NewString(), Provider: testPluginProviderKey, UserID: userID, ProfileID: profileID,
			RedirectURI: testAuthorizationRedirectURI, ProviderState: "pkce-verifier-secret", ExpiresAt: now.Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := repo.UpsertAuthSession(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	t.Run("encrypted flow state and provider binding", func(t *testing.T) {
		session := newSession()
		var deviceCode, userCode, verificationURL string
		if err := pool.QueryRow(ctx, `SELECT device_code, user_code, verification_url
			FROM watch_provider_auth_sessions WHERE id=$1::uuid`, session.ID).Scan(&deviceCode, &userCode, &verificationURL); err != nil {
			t.Fatal(err)
		}
		if deviceCode == session.DeviceCode || strings.Contains(deviceCode, "pkce-verifier-secret") ||
			strings.Contains(deviceCode, testAuthorizationRedirectURI) || userCode != AuthMethodAuthorizationCode || verificationURL != "" {
			t.Fatalf("authorization secrets were not confined to encrypted state: %q %q %q", deviceCode, userCode, verificationURL)
		}
		if _, err := otherRepo.ConsumeAuthorizationCodeAuthSession(ctx, session.ID, "plugin:5:anilist", now); !errors.Is(err, ErrAuthSessionMismatch) {
			t.Fatalf("wrong provider error = %v", err)
		}
		claimed, err := repo.ConsumeAuthorizationCodeAuthSession(ctx, session.ID, testPluginProviderKey, now)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := authorizationCodeSessionFromDeviceAuthSession(claimed)
		if err != nil || decoded.ProviderState != "pkce-verifier-secret" || decoded.RedirectURI != testAuthorizationRedirectURI || claimed.CompletedAt == nil {
			t.Fatalf("claimed state = %#v, err = %v", decoded, err)
		}
		if _, err := otherRepo.ConsumeAuthorizationCodeAuthSession(ctx, session.ID, testPluginProviderKey, now); !errors.Is(err, ErrAuthSessionCompleted) {
			t.Fatalf("replay error = %v", err)
		}
	})

	t.Run("concurrent claims across repositories", func(t *testing.T) {
		session := newSession()
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, claimant := range []*PostgresRepository{repo, otherRepo} {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				_, err := claimant.ConsumeAuthorizationCodeAuthSession(ctx, session.ID, testPluginProviderKey, now)
				results <- err
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		var accepted, rejected int
		for err := range results {
			if err == nil {
				accepted++
			} else if errors.Is(err, ErrAuthSessionCompleted) {
				rejected++
			} else {
				t.Fatalf("claim error = %v", err)
			}
		}
		if accepted != 1 || rejected != 1 {
			t.Fatalf("accepted = %d, rejected = %d", accepted, rejected)
		}
	})

	t.Run("expiry and device flow cannot be consumed", func(t *testing.T) {
		for _, kind := range []string{"expired", "device"} {
			session := newSession()
			want := ErrAuthSessionExpired
			if kind == "expired" {
				session.ExpiresAt = now
			} else {
				session.IntervalSeconds, session.UserCode = 5, "DEVICE"
				want = ErrAuthSessionMismatch
			}
			if _, err := repo.UpsertAuthSession(ctx, session); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ConsumeAuthorizationCodeAuthSession(ctx, session.ID, testPluginProviderKey, now); !errors.Is(err, want) {
				t.Fatalf("%s claim error = %v", kind, err)
			}
			stored, err := repo.GetAuthSession(ctx, session.ID)
			if err != nil || stored.CompletedAt != nil {
				t.Fatalf("%s rejected session was consumed: %#v, err = %v", kind, stored, err)
			}
		}
		if _, err := repo.ConsumeAuthorizationCodeAuthSession(ctx, uuid.NewString(), testPluginProviderKey, now); !errors.Is(err, ErrAuthSessionNotFound) {
			t.Fatalf("unknown session error = %v", err)
		}
	})
}
