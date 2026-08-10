//go:build task4fixture

package auth_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type task4Ready struct {
	Port      int `json:"port"`
	PluginAID int `json:"plugin_a_id"`
	PluginBID int `json:"plugin_b_id"`
	UserID    int `json:"user_id"`
	ProcessID int `json:"process_id"`
}

func TestTask4HTTPFixture(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	readyPath := os.Getenv("SILO_TASK4_READY_FILE")
	if dsn == "" || readyPath == "" {
		t.Fatal("SILO_TEST_DATABASE_URL and SILO_TASK4_READY_FILE are required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect fixture database: %v", err)
	}
	defer pool.Close()
	fixture := seedTask4Fixture(t, ctx, pool)
	defer fixture.cleanup(t, ctx, pool)

	sessions := auth.NewSessionRepository(pool)
	users := auth.NewUserRepository(pool)
	jwt := auth.NewJWTService("task4-fixture-jwt-secret", time.Minute, time.Hour)
	service := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	service.RegisterProvider(auth.LoginProviderInfo{ID: "plugin-a", DisplayName: "Plugin A", Mode: "credentials", InstallationID: fixture.pluginAID}, auth.NewTask4FixturePluginProvider(auth.PluginProviderConfig{InstallationID: fixture.pluginAID, CapabilityID: "ldap"}, sessions, users, pool, "task4-a"))
	service.RegisterProvider(auth.LoginProviderInfo{ID: "plugin-b", DisplayName: "Plugin B", Mode: "credentials", InstallationID: fixture.pluginBID}, auth.NewTask4FixturePluginProvider(auth.PluginProviderConfig{InstallationID: fixture.pluginBID, CapabilityID: "oidc"}, sessions, users, pool, "task4-b"))

	authHandler := handlers.NewAuthHandler(service, jwt, nil)
	pluginHandler := handlers.NewPluginHandler(plugins.NewRepositoryStore(pool), plugins.NewInstallationStore(pool), plugins.NewRuntimeConfigStore(pool), nil, nil, nil, nil, nil, handlers.NewServerRestartStatusTracker())
	authMW := apimw.NewAuthMiddleware(jwt, sessions, nil, users)
	r := chi.NewRouter()
	r.Post("/api/v1/auth/login", authHandler.HandleLogin)
	r.Group(func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/api/v1/auth/sessions", authHandler.HandleListSessions)
		r.Group(func(r chi.Router) {
			r.Use(apimw.RequireAdmin)
			r.Put("/api/v1/admin/plugins/installations/{id}/auth-binding", pluginHandler.HandlePutAuthBinding)
		})
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind fixture listener: %v", err)
	}
	server := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	address := listener.Addr().(*net.TCPAddr)
	writeTask4Ready(t, readyPath, task4Ready{Port: address.Port, PluginAID: fixture.pluginAID, PluginBID: fixture.pluginBID, UserID: fixture.userID, ProcessID: os.Getpid()})

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown fixture server: %v", err)
	}
	if err := <-stopped; err != nil && err != http.ErrServerClosed {
		t.Fatalf("serve fixture: %v", err)
	}
}

type task4Fixture struct {
	prefix                       string
	userID, pluginAID, pluginBID int
}

func seedTask4Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) task4Fixture {
	t.Helper()
	prefix := "task4-" + uuid.NewString()
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{Email: prefix + "@example.invalid", Username: prefix, Password: "task4-local-password", Role: "admin"})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	fixture := task4Fixture{prefix: prefix, userID: user.ID}
	for _, plugin := range []struct {
		capability, subject string
		target              *int
	}{{"ldap", "task4-a", &fixture.pluginAID}, {"oidc", "task4-b", &fixture.pluginBID}} {
		if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ($1, '0', '/fixture') RETURNING id`, prefix+"-"+plugin.capability).Scan(plugin.target); err != nil {
			t.Fatalf("create fixture installation: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, $2, true)`, *plugin.target, plugin.capability); err != nil {
			t.Fatalf("create fixture binding: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`, *plugin.target, plugin.subject, user.ID); err != nil {
			t.Fatalf("create fixture identity: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Task 4', true)`, prefix, user.ID); err != nil {
		t.Fatalf("create fixture profile: %v", err)
	}
	return fixture
}

func (f task4Fixture) cleanup(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id IN ($1, $2)`, f.pluginAID, f.pluginBID); err != nil {
		t.Errorf("cleanup fixture installations: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID); err != nil {
		t.Errorf("cleanup fixture user: %v", err)
	}
}

func writeTask4Ready(t *testing.T, path string, ready task4Ready) {
	t.Helper()
	data, err := json.Marshal(ready)
	if err != nil {
		t.Fatalf("marshal readiness: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create readiness directory: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write readiness: %v", err)
	}
}
