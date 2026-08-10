package auth_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestJellycompatLoginResolverWritesLocalSessionProvenance(t *testing.T) {
	// Given
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	username := "jellycompat-local-provenance-" + time.Now().UTC().Format("20060102150405.000000000")
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{
		Email: username + "@example.invalid", Username: username,
		Password: "jellycompat-local-provenance-password", Role: "user",
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Fatalf("cleanup local user: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ('jellycompat-provenance', $1, 'Profile', true)`, user.ID); err != nil {
		t.Fatalf("create local profile: %v", err)
	}
	sessions := auth.NewSessionRepository(pool)
	service := auth.NewService(auth.NewLocalProvider(auth.NewUserRepository(pool), sessions), auth.NewJWTService("jellycompat-local-provenance", time.Minute, time.Hour), sessions, auth.NewUserRepository(pool), nil, nil, pgstore.NewPostgresProvider(pool))
	resolver := jellycompat.NewLoginResolver(service, pgstore.NewPostgresProvider(pool), jellycompat.NewSessionStore(time.Hour, time.Now), func() string { return "compat-provenance" }, time.Now)

	// When
	_, err = resolver.Resolve(ctx, username, "jellycompat-local-provenance-password", "Jellyfin", "127.0.0.1")

	// Then
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	var providerKey string
	if err := pool.QueryRow(ctx, `SELECT provider_key FROM auth_sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&providerKey); err != nil {
		t.Fatalf("query local session provenance: %v", err)
	}
	if providerKey != "local" {
		t.Fatalf("Jellyfin local-login provider key = %q, want local", providerKey)
	}
}
