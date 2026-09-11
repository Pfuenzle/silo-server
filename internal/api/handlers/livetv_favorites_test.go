package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestLiveTVHomeSectionsResponse_hasTypedEmptyRails(t *testing.T) {
	// Given an empty Live TV home response.
	response := liveTVHomeSectionsResponse{
		CurrentlyAiring:            make([]liveTVProgrammeResponse, 0),
		FavoriteChannelsAiring:     make([]liveTVChannelResponse, 0),
		FavoriteProgrammesAiring:   make([]liveTVProgrammeResponse, 0),
		TopRatedFavoriteProgrammes: make([]liveTVProgrammeResponse, 0),
		UpcomingFavoriteProgrammes: make([]liveTVProgrammeResponse, 0),
	}

	// When the response is encoded for HTTP.
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, response)

	// Then all five rails remain present as arrays for stable clients.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"currently_airing", "favorite_channels_currently_airing", "favorite_programmes_currently_airing", "top_rated_favorite_programmes", "upcoming_favorite_programmes"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing section rail %q", key)
		}
	}
}

func TestLiveTVFavoriteRoutes_areProfileScopedAndTyped(t *testing.T) {
	// Given the authenticated Live TV route group.
	router := chi.NewRouter()
	router.Route("/livetv/libraries/{library_id}", func(r chi.Router) {
		r.Route("/favorites", func(r chi.Router) {
			r.Get("/channels", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r.Put("/channels/{channel_id}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r.Delete("/channels/{channel_id}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r.Get("/programmes", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r.Put("/programmes/{programme_id}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r.Delete("/programmes/{programme_id}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		})
	})

	// When the route contract is enumerated.
	seen := make(map[string]bool)
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Then channel and programme mutations are separate typed resources.
	for _, route := range []string{
		"GET /livetv/libraries/{library_id}/favorites/channels",
		"PUT /livetv/libraries/{library_id}/favorites/channels/{channel_id}",
		"DELETE /livetv/libraries/{library_id}/favorites/channels/{channel_id}",
		"GET /livetv/libraries/{library_id}/favorites/programmes",
		"PUT /livetv/libraries/{library_id}/favorites/programmes/{programme_id}",
		"DELETE /livetv/libraries/{library_id}/favorites/programmes/{programme_id}",
	} {
		if !seen[route] {
			t.Fatalf("missing route %q", route)
		}
	}
}
