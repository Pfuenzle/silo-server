package api

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/livetv"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	declareNativeMediaRoutes()
	minimal := NewRouter(Dependencies{Config: cfg})
	maximal := NewRouter(Dependencies{DB: pool, Config: cfg, FileRepo: scanner.NewFileRepository(pool), FolderRepo: catalog.NewFolderRepository(pool), SessionMgr: playback.NewSessionManager(0, 0)})
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{minimal, maximal}, nativeMediaRoutes)
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/media_routes.txt"
	if *updateRouteManifest {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != actual {
		t.Fatalf("route manifest changed; inspect it and run go test . -update-route-manifest")
	}
	for _, route := range nativeMediaRoutes {
		if !route.Enrolled {
			t.Fatalf("native route not enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}

func TestNewRouter_mountsLiveTVSourcesAtTopLevelNativePath(t *testing.T) {
	// Given the production router composition with database and library dependencies.
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	router := NewRouter(Dependencies{
		DB:                 pool,
		Config:             cfg,
		FolderRepo:         catalog.NewFolderRepository(pool),
		LivePlayback:       livetv.NewLivePlaybackService(livetv.LivePlaybackConfig{}),
		LivePlaybackOrigin: "/api/v1",
	})
	wanted := map[string]bool{
		"GET /api/v1/livetv/libraries/{library_id}/sources/":                      true,
		"POST /api/v1/livetv/libraries/{library_id}/sources/":                     true,
		"PUT /api/v1/livetv/libraries/{library_id}/sources/{source_key}":          true,
		"DELETE /api/v1/livetv/libraries/{library_id}/sources/{source_key}":       true,
		"POST /api/v1/livetv/libraries/{library_id}/sources/{source_key}/refresh": true,
		"GET /api/v1/stream/live/{grant_id}/manifest":                             true,
		"GET /api/v1/stream/live/{grant_id}/segment/{name}":                       true,
	}
	seen := make(map[string]bool)

	// When the final NewRouter routes are walked.
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Then the documented routes exist and no /libraries prefix was inserted.
	for route := range wanted {
		if !seen[route] {
			t.Fatalf("missing final NewRouter route %q; registered=%v", route, seen)
		}
	}
	for route := range seen {
		if strings.Contains(route, "/libraries/livetv/") {
			t.Fatalf("final NewRouter nested Live TV source route under /libraries: %q", route)
		}
	}
}

func TestNewRouter_liveTVSourceMutationsAreNotRejectedAsMethodNotAllowed(t *testing.T) {
	// Given the production router composition with Live TV dependencies and no credentials.
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	router := NewRouter(Dependencies{DB: pool, Config: cfg, FolderRepo: catalog.NewFolderRepository(pool)})

	requests := []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "source update",
			method: http.MethodPut,
			path:   "/api/v1/livetv/libraries/42/sources/playlist-main",
		},
		{
			name:   "source refresh",
			method: http.MethodPost,
			path:   "/api/v1/livetv/libraries/42/sources/playlist-main/refresh",
		},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			// When the exact client mutation request reaches the final production router.
			recording := httptest.NewRecorder()
			router.ServeHTTP(recording, httptest.NewRequest(request.method, request.path, strings.NewReader(`{}`)))

			// Then routing reaches authentication rather than returning chi's 405.
			if recording.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s returned HTTP 405; route was not matched by final NewRouter", request.method, request.path)
			}
			if recording.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s returned HTTP %d, want HTTP 401 after route match", request.method, request.path, recording.Code)
			}
		})
	}
}

func TestNewRouterRegistersTranscodeShutdownWork(t *testing.T) {
	registered := make(chan (<-chan struct{}), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewRouter(Dependencies{
		Config:     &config.Config{},
		AppContext: ctx,
		SessionMgr: playback.NewSessionManager(0, 0),
		RegisterShutdownWork: func(done <-chan struct{}) {
			registered <- done
		},
	})

	select {
	case done := <-registered:
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("registered transcode cleanup did not finish after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("router did not register transcode shutdown work")
	}
}

func TestNativeRejectedAndMissingRequestsRemainProvisional(t *testing.T) {
	for _, route := range nativeMediaRoutes {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
			t.Run(route.Method+" "+route.Pattern+" "+http.StatusText(status), func(t *testing.T) {
				cfg := streamtelemetry.DefaultConfig("test")
				cfg.Enabled = true
				registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
				handler := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(route.Method, "/", nil))
				snapshot := registry.Sweep()
				if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
					t.Fatalf("status %d created logical activity: %+v", status, snapshot)
				}
			})
		}
	}
}
