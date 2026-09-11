package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/livetv"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/go-chi/chi/v5"
)

func TestLiveTVCapability_describesNativeWebOnlyContract(t *testing.T) {
	// Given a capability handler without a database dependency.
	handler := &LiveTVHandler{}
	recording := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/capability", nil)

	// When capability discovery is requested.
	handler.HandleCapability(recording, request)

	// Then the response is versioned and advertises the supported native features.
	if recording.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recording.Code)
	}
	var response liveTVCapabilityResponse
	if err := json.Unmarshal(recording.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || response.Enabled {
		t.Fatalf("capability = %#v, want schema 1 and disabled without repository", response)
	}
	for _, feature := range []string{"channels", "guide_window"} {
		found := false
		for _, advertised := range response.Features {
			if advertised == feature {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("feature %q missing from %#v", feature, response.Features)
		}
	}
	for _, feature := range response.Features {
		if feature == "playback_resolution" {
			t.Fatal("unavailable playback was advertised as a capability")
		}
	}
}

func TestLiveTVCapability_route_returnsJSONOverHTTP(t *testing.T) {
	// Given a native route mounted on a real chi router.
	router := chi.NewRouter()
	router.Get("/api/v1/livetv/capability", (&LiveTVHandler{}).HandleCapability)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/capability", nil)
	recording := httptest.NewRecorder()

	// When the HTTP request is served.
	router.ServeHTTP(recording, request)

	// Then the route returns the versioned JSON capability contract.
	if recording.Code != http.StatusOK || recording.Header().Get("Content-Type") == "" {
		t.Fatalf("HTTP capability response = %d %q, want 200 with content type", recording.Code, recording.Header().Get("Content-Type"))
	}
}

func TestLiveTVAdminRoutes_mountExactTopLevelPathsAndMethods(t *testing.T) {
	// Given the production Live TV admin route mount helper.
	router := chi.NewRouter()
	MountLiveTVAdminRoutes(router, &LiveTVHandler{})
	wanted := map[string]bool{
		"GET /livetv/libraries/{library_id}/sources/":                      true,
		"POST /livetv/libraries/{library_id}/sources/":                     true,
		"PUT /livetv/libraries/{library_id}/sources/{source_key}":          true,
		"DELETE /livetv/libraries/{library_id}/sources/{source_key}":       true,
		"POST /livetv/libraries/{library_id}/sources/{source_key}/refresh": true,
	}
	seen := make(map[string]bool)

	// When the registered production paths are enumerated.
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...string) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Then every documented method/path pair exists at the top-level Live TV path.
	for route := range wanted {
		if !seen[route] {
			t.Fatalf("missing exact Live TV admin route %q; registered=%v", route, seen)
		}
	}
	for route := range seen {
		if strings.Contains(route, "/libraries/livetv/") {
			t.Fatalf("Live TV admin route was nested under /libraries: %q", route)
		}
	}
}

func TestLiveTVLibraryAccess_rejectsEmptyRestrictedAllowList(t *testing.T) {
	// Given a profile scope that explicitly restricts libraries to none.
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	contextWithScope := access.SetScope(context.Background(), access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{}})
	contextWithScope = apimw.SetClaims(contextWithScope, &auth.Claims{UserID: 7})
	request = request.WithContext(contextWithScope)

	// When access is evaluated for any Live TV library.
	allowed := libraryAccessible(request, 42)

	// Then the empty restricted set denies access rather than widening it.
	if allowed {
		t.Fatal("empty restricted library scope granted access")
	}
}

func TestLiveTVLibraryVisible_rejectsDisabledViewerLibrary(t *testing.T) {
	// Given a disabled Live TV library and an otherwise permitted viewer.
	library := &models.MediaFolder{Type: "livetv", Enabled: false}

	// When viewer visibility is evaluated for reads and playback resolution.
	visible := liveTVLibraryVisible(library, true, true)

	// Then the disabled library is unavailable.
	if visible {
		t.Fatal("disabled Live TV library was visible to viewer")
	}
	if !liveTVLibraryVisible(library, false, true) {
		t.Fatal("disabled Live TV library was hidden from admin source management")
	}
}

func TestLiveTVPagination_capsLimitAndNormalizesOffset(t *testing.T) {
	// Given a request with an oversized limit and a negative offset.
	request := httptest.NewRequest(http.MethodGet, "/?limit=999&offset=-4", nil)

	// When pagination parameters are parsed.
	limit, offset := parseLiveTVPage(request)

	// Then the server applies the documented bounded page shape.
	if limit != liveTVMaxPageSize || offset != 0 {
		t.Fatalf("pagination = %d/%d, want %d/0", limit, offset, liveTVMaxPageSize)
	}
}

func TestLiveTVGuideWindow_rejectsWindowsLongerThanOneDay(t *testing.T) {
	// Given a guide request whose window exceeds the API maximum.
	request := httptest.NewRequest(http.MethodGet, "/?from=2026-09-10T00:00:00Z&to=2026-09-11T00:00:01Z", nil)

	// When the guide window is parsed.
	_, _, err := parseGuideWindow(request)

	// Then the invalid window is rejected.
	if err == nil {
		t.Fatal("guide window longer than 24 hours was accepted")
	}
}

func TestLiveTVSourceResponse_redactsConfiguredLocationAndConfig(t *testing.T) {
	// Given a source containing private configuration fields.
	source := livetv.Source{ID: 3, LibraryID: 7, Kind: livetv.SourceKindPlaylist, SourceKey: "main", Name: "Main", Location: "https://user:secret@example.invalid/list.m3u", Config: []byte(`{"token":"secret"}`), Enabled: true, LastRefreshAt: time.Now().UTC()}

	// When the source is mapped to the public response.
	encoded, err := json.Marshal(sourceResponse(source))

	// Then provider location and credentials are absent.
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsAny(string(encoded), "example.invalid", "secret", "location", "config") {
		t.Fatalf("redacted source response leaked private fields: %s", encoded)
	}
}

func TestLiveTVSourceResponse_redactsRefreshDiagnostic(t *testing.T) {
	// Given a stale source whose stored diagnostic contains provider details.
	source := livetv.Source{RefreshState: "stale", RefreshError: "https://user:secret@example.invalid/feed"}

	// When the source is mapped to the public response.
	encoded, err := json.Marshal(sourceResponse(source))

	// Then only a generic refresh diagnostic is exposed.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "example.invalid") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("refresh diagnostic leaked provider details: %s", encoded)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
