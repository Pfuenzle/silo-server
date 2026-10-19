package apiv2

import (
	"net/http"
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

func TestCreateAudiobookRequestV2(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	rec := do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"audiobook","provider":"audiobook-metadata","provider_item_id":"book-1","title":"Example audiobook"}`, requestOwner)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create audiobook: %d %s", rec.Code, rec.Body.String())
	}
	if svc.lastCreate.MediaType != mediarequests.MediaTypeAudiobook || svc.lastCreate.Provider != "audiobook-metadata" || svc.lastCreate.ProviderItemID != "book-1" || svc.lastCreate.TMDBID != 0 {
		t.Fatalf("audiobook create identity = %+v", svc.lastCreate)
	}
}

func TestAudiobookRequestV2IdentityAndLifecycle(t *testing.T) {
	req := &mediarequests.Request{
		ID: "book-request", Provider: "audiobook-metadata", ProviderItemID: "book-1", MediaType: mediarequests.MediaTypeAudiobook,
		Title: "Example audiobook", Status: mediarequests.StatusCompleted, Outcome: mediarequests.OutcomeActive,
		SiloAudiobookID: "library-book", SiloAudiobookLink: "/audiobooks/library-book", Retryable: true,
		ExternalLibraryID: "external-library", ExternalDownloadID: "external-download", ExternalDetail: "provider detail",
		ImportedPath: "/private/library", CreatedAt: fixedTime(), UpdatedAt: fixedTime(),
	}
	viewer := mediaRequestOf(req, mediarequests.Viewer{UserID: 1})
	if viewer.ProviderItemID != "book-1" || viewer.MediaType != "audiobook" || viewer.SiloAudiobookID != "library-book" || viewer.SiloAudiobookLink != "/audiobooks/library-book" || !viewer.Retryable || viewer.Targets == nil {
		t.Fatalf("audiobook wire identity/lifecycle = %+v", viewer)
	}
	if viewer.ExternalLibraryID != "" || viewer.ExternalDownloadID != "" || viewer.ExternalDetail != "" {
		t.Fatal("requester received provider lifecycle internals")
	}
	admin := mediaRequestOf(req, mediarequests.Viewer{IsAdmin: true})
	if admin.ExternalLibraryID != "external-library" || admin.ExternalDownloadID != "external-download" || admin.ExternalDetail != "provider detail" {
		t.Fatal("admin lost provider lifecycle details")
	}
	results := requestMediaResultsOf([]mediarequests.MediaResult{{
		MediaType: mediarequests.MediaTypeAudiobook, Provider: "audiobook-metadata", ProviderItemID: "book-1",
		Title: "Example audiobook", Request: mediarequests.RequestState{Requestable: true},
	}})
	if len(results) != 1 || results[0].Provider != "audiobook-metadata" || results[0].ProviderItemID != "book-1" || results[0].MediaType != "audiobook" {
		t.Fatalf("audiobook search identities = %+v", results)
	}
}

func TestAdminAudiobookRequestFilterV2(t *testing.T) {
	svc := &fakeQueueAdmin{fakeAdminRequests: fixtureAdminRequests()}
	rec := do(t, queueAdminHandler(svc), http.MethodGet, Prefix+"/admin/requests?media_type=audiobook", "", actingRequestAdmin)
	if rec.Code != http.StatusOK || svc.filter.MediaType != mediarequests.MediaTypeAudiobook {
		t.Fatalf("audiobook queue filter: status=%d filter=%+v", rec.Code, svc.filter)
	}
}
