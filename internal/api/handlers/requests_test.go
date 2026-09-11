package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

type fakeRequestService struct {
	listStudiosFn  func() ([]mediarequests.DiscoverBrandCard, error)
	listNetworksFn func() ([]mediarequests.DiscoverBrandCard, error)
	listGenresFn   func() ([]mediarequests.DiscoverBrandCard, error)
	listMineFn     func() ([]*mediarequests.Request, error)
	browseFn       func(kind, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
}

func (f *fakeRequestService) ListStudios(context.Context, mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listStudiosFn != nil {
		return f.listStudiosFn()
	}
	return nil, nil
}

func (f *fakeRequestService) ListNetworks(context.Context, mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listNetworksFn != nil {
		return f.listNetworksFn()
	}
	return nil, nil
}

func (f *fakeRequestService) ListGenres(context.Context, mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listGenresFn != nil {
		return f.listGenresFn()
	}
	return nil, nil
}

func (f *fakeRequestService) BrowseStudio(_ context.Context, _ mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("studio", slug, mediarequests.MediaTypeMovie, sort, page)
}

func (f *fakeRequestService) BrowseNetwork(_ context.Context, _ mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("network", slug, mediarequests.MediaTypeSeries, sort, page)
}

func (f *fakeRequestService) BrowseGenre(_ context.Context, _ mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("genre", slug, mediaType, sort, page)
}

func (f *fakeRequestService) Search(context.Context, mediarequests.Viewer, string, mediarequests.MediaType, int) (*mediarequests.MediaPage, error) {
	return nil, nil
}

func (f *fakeRequestService) Discover(context.Context, mediarequests.Viewer, string, int) (*mediarequests.DiscoverySection, error) {
	return nil, nil
}

func (f *fakeRequestService) DiscoverAll(context.Context, mediarequests.Viewer) ([]mediarequests.DiscoverySection, error) {
	return nil, nil
}

func (f *fakeRequestService) GetDetail(context.Context, mediarequests.Viewer, mediarequests.MediaType, int) (*mediarequests.MediaDetail, error) {
	return nil, nil
}

func (f *fakeRequestService) CreateRequest(context.Context, mediarequests.Viewer, mediarequests.CreateRequestInput) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) ListMine(context.Context, mediarequests.Viewer, mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	if f.listMineFn != nil {
		return f.listMineFn()
	}
	return nil, nil
}

func (f *fakeRequestService) ListAdmin(context.Context, mediarequests.Viewer, mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) GetRequest(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) Approve(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) Decline(context.Context, mediarequests.Viewer, string, string) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) Cancel(context.Context, mediarequests.Viewer, string, string) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) Retry(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}

func (f *fakeRequestService) GetFeatureStatus(context.Context, mediarequests.Viewer) (mediarequests.FeatureStatus, error) {
	return mediarequests.FeatureStatus{}, nil
}

func (f *fakeRequestService) GetSettings(context.Context, mediarequests.Viewer) (mediarequests.Settings, error) {
	return mediarequests.Settings{}, nil
}

func (f *fakeRequestService) UpdateSettings(context.Context, mediarequests.Viewer, mediarequests.Settings) (mediarequests.Settings, error) {
	return mediarequests.Settings{}, nil
}

func (f *fakeRequestService) GetUserLimit(context.Context, mediarequests.Viewer, int) (*mediarequests.UserLimit, error) {
	return nil, nil
}

func (f *fakeRequestService) UpsertUserLimit(context.Context, mediarequests.Viewer, mediarequests.UserLimit) (*mediarequests.UserLimit, error) {
	return nil, nil
}

func (f *fakeRequestService) ListIntegrations(context.Context, mediarequests.Viewer) ([]mediarequests.Integration, error) {
	return nil, nil
}

func (f *fakeRequestService) CreateIntegration(_ context.Context, _ mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error) {
	return &integration, nil
}

func (f *fakeRequestService) UpdateIntegration(_ context.Context, _ mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error) {
	return &integration, nil
}

func (f *fakeRequestService) DeleteIntegration(context.Context, mediarequests.Viewer, string) error {
	return nil
}

func (f *fakeRequestService) LoadIntegrationOptions(context.Context, mediarequests.Viewer, mediarequests.Integration) (map[string][]mediarequests.RouterOption, error) {
	return nil, nil
}

func authedRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	return req.WithContext(ctx)
}

func TestHandleListStudiosReturnsJSON(t *testing.T) {
	logo := "https://image.tmdb.org/t/p/w300/x.png"
	svc := &fakeRequestService{
		listStudiosFn: func() ([]mediarequests.DiscoverBrandCard, error) {
			return []mediarequests.DiscoverBrandCard{
				{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", LogoURL: &logo},
			}, nil
		},
	}
	h := NewRequestsHandler(svc)

	rec := httptest.NewRecorder()
	h.HandleListStudios(rec, authedRequest("GET", "/api/v1/requests/discover/studios"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Studios []mediarequests.DiscoverBrandCard `json:"studios"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Studios) != 1 || body.Studios[0].Slug != "marvel-studios" {
		t.Errorf("studios = %+v", body.Studios)
	}
}

func TestRequestIntegrationResponsePreservesGenericPluginConfig(t *testing.T) {
	// Given a saved generic integration with plugin-owned configuration
	integration := mediarequests.Integration{
		CapabilityID: "custom-router",
		PluginConfig: map[string]any{
			"base_url": "https://router.example",
			"secret":   "plugin-owned-secret",
		},
	}

	// When it is rendered for the admin API
	response := requestIntegrationResponseFrom(integration)

	// Then the generic core does not interpret or rewrite plugin-owned fields
	if response.PluginConfig["secret"] != "plugin-owned-secret" {
		t.Fatalf("response changed plugin-owned config: %#v", response.PluginConfig)
	}
	if response.PluginConfig["base_url"] != "https://router.example" {
		t.Fatalf("response base_url = %#v", response.PluginConfig["base_url"])
	}
}

func TestHandleCreateIntegrationTreatsCapabilityAndConfigAsOpaque(t *testing.T) {
	// Given a capability name that must not select a host-side provider parser
	capabilityID := strings.Join([]string{"listen", "arr"}, "")
	integration := mediarequests.Integration{
		CapabilityID: capabilityID,
		PluginConfig: map[string]any{
			"timeout": "provider-defined",
			"nested":  map[string]any{"value": "unchanged"},
		},
	}

	// When the integration crosses the HTTP boundary
	h := NewRequestsHandler(&fakeRequestService{})
	body, err := json.Marshal(integration)
	if err != nil {
		t.Fatalf("marshal integration: %v", err)
	}
	req := authedRequest("POST", "/api/v1/requests/integrations")
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.HandleCreateIntegration(rec, req)

	// Then the provider-owned shape is accepted without host interpretation
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var response requestIntegrationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.CapabilityID != capabilityID || response.PluginConfig["timeout"] != "provider-defined" {
		t.Fatalf("response changed opaque integration: %+v", response)
	}
}

func TestHandleListMineExposesNullableAudiobookLifecycleFields(t *testing.T) {
	// Given an audiobook request with external status and final Silo link.
	request := &mediarequests.Request{
		ID:                 "request-1",
		MediaType:          mediarequests.MediaTypeAudiobook,
		Title:              "A Book",
		ExternalStatus:     "scanning",
		ExternalDetail:     "Silo is scanning the imported folder",
		ExternalLibraryID:  "external-library-1",
		ExternalDownloadID: "external-download-1",
		SiloAudiobookID:    "silo-book-1",
		SiloAudiobookLink:  "/api/v1/items/silo-book-1",
	}

	// When the request list is rendered through the user-facing API.
	h := NewRequestsHandler(&fakeRequestService{listMineFn: func() ([]*mediarequests.Request, error) {
		return []*mediarequests.Request{request}, nil
	}})
	rec := httptest.NewRecorder()
	h.HandleListMine(rec, authedRequest("GET", "/api/v1/requests/mine"))

	// Then stable nullable fields are present without exposing configuration secrets.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Requests) != 1 {
		t.Fatalf("requests = %+v, want one request", body.Requests)
	}
	row := body.Requests[0]
	for _, field := range []string{"external_status", "external_detail", "external_library_id", "external_download_id", "silo_audiobook_link"} {
		if _, present := row[field]; !present {
			t.Fatalf("field %q missing from response: %+v", field, row)
		}
	}
	if row["external_status"] != "scanning" || row["silo_audiobook_link"] != "/api/v1/items/silo-book-1" {
		t.Fatalf("lifecycle fields = %+v", row)
	}
	if strings.Contains(rec.Body.String(), "api_key") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("response contains configuration secret material: %s", rec.Body.String())
	}
}

func TestHandleListMineSerializesMissingAudiobookLifecycleFieldsAsNull(t *testing.T) {
	// Given an audiobook request that has not reached an external provider.
	request := &mediarequests.Request{ID: "request-2", MediaType: mediarequests.MediaTypeAudiobook, Title: "Pending"}
	h := NewRequestsHandler(&fakeRequestService{listMineFn: func() ([]*mediarequests.Request, error) {
		return []*mediarequests.Request{request}, nil
	}})

	// When the request list is rendered.
	rec := httptest.NewRecorder()
	h.HandleListMine(rec, authedRequest("GET", "/api/v1/requests/mine"))

	// Then clients receive explicit nulls rather than an unstable missing shape.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row := body.Requests[0]
	for _, field := range []string{"external_status", "external_detail", "external_library_id", "external_download_id", "silo_audiobook_link"} {
		if value, present := row[field]; !present || value != nil {
			t.Fatalf("field %q = (%v, %t), want explicit null", field, value, present)
		}
	}
}

func TestHandleBrowseStudioRejectsUnknownSort(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(kind, slug string, _ mediarequests.MediaType, sort string, _ int) (*mediarequests.DiscoverBrowseResponse, error) {
			return nil, mediarequests.ErrInvalidInput
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/studio/marvel-studios?sort=garbage")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "marvel-studios")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseStudio(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBrowseStudioUnknownSlugReturns404(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(string, string, mediarequests.MediaType, string, int) (*mediarequests.DiscoverBrowseResponse, error) {
			return nil, mediarequests.ErrNotFound
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/studio/ghosts")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "ghosts")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseStudio(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleBrowseGenreRequiresMediaType(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(_ string, _ string, mt mediarequests.MediaType, _ string, _ int) (*mediarequests.DiscoverBrowseResponse, error) {
			if strings.TrimSpace(string(mt)) == "" {
				return nil, mediarequests.ErrInvalidInput
			}
			return &mediarequests.DiscoverBrowseResponse{Kind: "genre"}, nil
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/genre/action")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "action")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseGenre(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
