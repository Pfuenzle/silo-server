package requests

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type gatedAudiobookScanQueue struct {
	waiting chan struct{}
	release chan struct{}
	status  string
}

func (q *gatedAudiobookScanQueue) EnqueueScanRun(_ context.Context, folderID int, _, _, _ string) (*models.ScanRun, bool, error) {
	return &models.ScanRun{ID: "import-scan", Status: "running", MediaFolderID: folderID}, true, nil
}

func (q *gatedAudiobookScanQueue) Wait(ctx context.Context, id string) (*models.ScanRun, error) {
	close(q.waiting)
	select {
	case <-q.release:
		return &models.ScanRun{ID: id, Status: q.status, ErrorMessage: "scan failed"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAudiobookRouterCompletionWaitsForImportScan(t *testing.T) {
	for _, scanStatus := range []string{"completed", "failed"} {
		t.Run(scanStatus, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			t.Cleanup(cancel)
			store := newFakeStore()
			connection := routerInst("book-router")
			connection.SupportedMediaTypes = []string{"audiobook"}
			store.integrations = []Integration{connection}
			store.requests["book-request"] = &Request{
				ID: "book-request", Provider: "audiobook-metadata", ProviderItemID: "book-1", MediaType: MediaTypeAudiobook,
				Status: StatusQueued, Outcome: OutcomeActive, SubmissionState: SubmissionStateFulfilled,
			}
			store.candidates = []*Request{store.requests["book-request"]}
			if _, err := store.CreateTarget(ctx, Target{
				RequestID: "book-request", IntegrationID: connection.ID, Quality: Quality1080p,
				Status: StatusQueued, ExternalID: "download-1",
			}); err != nil {
				t.Fatal(err)
			}
			queue := &gatedAudiobookScanQueue{waiting: make(chan struct{}), release: make(chan struct{}), status: scanStatus}
			service := newTestService(store)
			service.SetRouterProvider(&fakeRouterProvider{statuses: []RouterTargetStatus{{
				Quality: Quality1080p, ConnectionID: connection.ID, Status: StatusCompleted, ExternalStatus: "moved",
				Metadata: &RouterMetadata{ImportedPath: "/incoming/book"},
			}}})
			service.SetAudiobookImportLinker(NewAudiobookImportLinker(queue,
				audiobookImportResolver{target: &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: "/library/book"}},
				audiobookImportFiles{id: "library-book"}, audiobookImportItems{item: &models.MediaItem{ContentID: "library-book", Type: "audiobook"}},
				audiobookImportMapper("/library/book")))
			type outcome struct {
				result ReconcileResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := service.ReconcileRequests(ctx, 100)
				done <- outcome{result: result, err: err}
			}()
			select {
			case <-queue.waiting:
			case result := <-done:
				t.Fatalf("reconcile returned before awaiting import scan: %+v", result)
			case <-ctx.Done():
				t.Fatal("reconcile never awaited the import scan")
			}
			pending, err := store.GetRequest(ctx, "book-request")
			if err != nil || pending.Status != StatusQueued || pending.SiloAudiobookID != "" || pending.SiloAudiobookLink != "" {
				t.Fatalf("request while scan runs = %+v, err = %v; want queued without a library link", pending, err)
			}
			targets, err := store.ListTargets(ctx, "book-request")
			if err != nil || len(targets) != 1 || targets[0].Status != StatusQueued {
				t.Fatalf("targets while scan runs = %+v, err = %v; want queued", targets, err)
			}
			close(queue.release)
			var finished outcome
			select {
			case finished = <-done:
			case <-ctx.Done():
				t.Fatal("reconcile did not finish after the scan")
			}
			if finished.err != nil {
				t.Fatalf("reconcile: %v", finished.err)
			}
			stored, err := store.GetRequest(ctx, "book-request")
			if err != nil {
				t.Fatal(err)
			}
			if scanStatus == "completed" {
				if stored.Status != StatusCompleted || stored.SiloAudiobookID != "library-book" || stored.SiloAudiobookLink != "/item/library-book" || finished.result.Completed != 1 {
					t.Fatalf("after successful scan: request=%+v result=%+v", stored, finished.result)
				}
			} else if stored.Outcome != OutcomeFailed || !stored.Retryable || stored.SiloAudiobookID != "" || stored.SiloAudiobookLink != "" || finished.result.Failed != 1 {
				t.Fatalf("after failed scan: request=%+v result=%+v", stored, finished.result)
			}
		})
	}
}
