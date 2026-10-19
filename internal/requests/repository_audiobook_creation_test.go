package requests

import (
	"errors"
	"testing"
)

func TestAudiobookRequestCreationDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	input := CreateRequestRecord{
		ID: "audiobook-created",
		Input: CreateRequestInput{
			MediaType: MediaTypeAudiobook, Provider: "audiobook-metadata", ProviderItemID: "book-created", Title: "Example audiobook",
		},
		Requester: Viewer{UserID: 1, ProfileID: "profile"},
		Status:    StatusPending, Outcome: OutcomeActive, ReplaceFailed: true,
	}
	created, err := repo.CreateRequest(ctx, input)
	if err != nil {
		t.Fatalf("create audiobook: %v", err)
	}
	if created.Provider != input.Input.Provider || created.ProviderItemID != input.Input.ProviderItemID || created.MediaType != MediaTypeAudiobook || created.TMDBID != 0 || created.FulfillmentKey != input.ID {
		t.Fatalf("created identity = %+v", created)
	}
	var nullTMDB bool
	if err := pool.QueryRow(ctx, `SELECT tmdb_id IS NULL FROM media_requests WHERE id = $1`, input.ID).Scan(&nullTMDB); err != nil || !nullTMDB {
		t.Fatalf("stored NULL TMDB = %v, err = %v", nullTMDB, err)
	}
	active, err := repo.ListActiveByProviderItem(ctx, input.Input.Provider, input.Input.ProviderItemID)
	if err != nil || active == nil || active.ID != input.ID {
		t.Fatalf("active provider lookup = %+v, %v", active, err)
	}
	input.ID = "duplicate"
	if _, err := repo.CreateRequest(ctx, input); !errors.Is(err, ErrAlreadyRequested) {
		t.Fatalf("duplicate error = %v, want ErrAlreadyRequested", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	input.ID = "replacement"
	replacement, err := repo.CreateRequest(ctx, input)
	if err != nil || replacement.ProviderItemID != "book-created" {
		t.Fatalf("replacement = %+v, %v", replacement, err)
	}
	if _, err := repo.GetRequest(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replaced failed request error = %v, want ErrNotFound", err)
	}
	updated, err := repo.UpdateRequestLifecycle(ctx, replacement.ID, RequestLifecycle{
		ExternalLibraryID: "library-1", ExternalDownloadID: "download-1", ExternalStatus: "importing",
		ExternalDetail: "Importing", ImportedPath: "/library/example", ScanRunID: "scan-1",
		SiloAudiobookID: "book-in-library", SiloAudiobookLink: "/audiobooks/book-in-library", Retryable: true,
	})
	if err != nil {
		t.Fatalf("persist audiobook lifecycle: %v", err)
	}
	if updated.ExternalLibraryID != "library-1" || updated.ExternalDownloadID != "download-1" || updated.ExternalStatus != "importing" || updated.ExternalDetail != "Importing" || updated.ImportedPath != "/library/example" || updated.ScanRunID != "scan-1" || updated.SiloAudiobookID != "book-in-library" || updated.SiloAudiobookLink != "/audiobooks/book-in-library" || !updated.Retryable {
		t.Fatalf("lifecycle did not round-trip: %+v", updated)
	}
}
