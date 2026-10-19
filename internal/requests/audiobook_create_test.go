package requests

import (
	"context"
	"errors"
	"testing"
)

type audiobookCreateStore struct {
	*fakeStore
	activeBook *Request
}

func (s *audiobookCreateStore) ListActiveByProviderItem(context.Context, string, string) (*Request, error) {
	return s.activeBook, nil
}

func (s *audiobookCreateStore) DeleteFailedByProviderItem(context.Context, string, string) (int, error) {
	return 0, nil
}

func TestCreateAudiobookUsesProviderIdentity(t *testing.T) {
	store := &audiobookCreateStore{fakeStore: newFakeStore()}
	store.settings.GlobalAutoApprovalEnabled = true
	service := NewService(store, nil, nil)
	service.SetUserRepository(requestUserRepo{})
	input := CreateRequestInput{MediaType: MediaTypeAudiobook, ProviderItemID: "book-1", Title: "Example audiobook"}
	if _, err := service.CreateRequest(t.Context(), testViewer(1), input); err != nil {
		t.Fatalf("create audiobook without TMDB: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1", len(store.created))
	}
	created := store.created[0]
	if created.Input.Provider != "audiobook-metadata" || created.Input.ProviderItemID != "book-1" || created.Input.TMDBID != 0 || created.Status != StatusPending {
		t.Fatalf("audiobook create record = %+v", created)
	}
	store.activeBook = &Request{ID: "existing", Provider: "audiobook-metadata", ProviderItemID: "book-1", MediaType: MediaTypeAudiobook}
	if _, err := service.CreateRequest(t.Context(), testViewer(1), input); !errors.Is(err, ErrAlreadyRequested) {
		t.Fatalf("duplicate audiobook error = %v", err)
	}
	if len(store.created) != 1 {
		t.Fatal("duplicate audiobook was persisted")
	}
}

func TestSearchAudiobooksWithoutTMDB(t *testing.T) {
	service := NewService(newFakeStore(), nil, nil)
	service.SetAudiobookSearcher(AudiobookSearchFunc(func(_ context.Context, viewer Viewer, query string) ([]AudiobookSearchResult, error) {
		if viewer.UserID != 1 || query != "Example" {
			t.Fatalf("search identity/query = %+v/%q", viewer, query)
		}
		return []AudiobookSearchResult{{ProviderItemID: "book-1", Title: "Example audiobook"}}, nil
	}))
	page, err := service.Search(t.Context(), testViewer(1), " Example ", MediaTypeAudiobook, 1)
	if err != nil {
		t.Fatalf("audiobook search: %v", err)
	}
	if len(page.Results) != 1 || page.Results[0].Provider != "audiobook-metadata" || page.Results[0].ProviderItemID != "book-1" || page.Results[0].MediaType != MediaTypeAudiobook || !page.Results[0].Request.Requestable {
		t.Fatalf("search results = %+v", page.Results)
	}
}
