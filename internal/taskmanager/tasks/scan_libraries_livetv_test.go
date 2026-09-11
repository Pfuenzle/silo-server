package tasks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type liveTVScanQueue struct{ calls int }

func (q *liveTVScanQueue) EnqueueLibraryScan(context.Context, int, string) (bool, error) {
	q.calls++
	return true, nil
}

func TestScanLibrariesTask_skipsLiveTVLibrary(t *testing.T) {
	// Given an enabled Live TV library and a filesystem media library.
	folders := &scanFolderRepoStub{folders: []*models.MediaFolder{
		{ID: 1, Type: "livetv", Name: "Channels", Enabled: true},
		{ID: 2, Type: "movies", Name: "Films", Enabled: true},
	}}
	queue := &liveTVScanQueue{}
	task := NewScanLibrariesTask(folders, queue, nil)

	// When the scheduled filesystem scan task executes.
	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatal(err)
	}

	// Then only the filesystem library is enqueued.
	if queue.calls != 1 {
		t.Fatalf("enqueue calls = %d, want 1", queue.calls)
	}
}

type noopProgressReporter struct{}

func (noopProgressReporter) Report(float64, string)        {}
func (noopProgressReporter) SetResultData(json.RawMessage) {}

type scanFolderRepoStub struct{ folders []*models.MediaFolder }

func (r *scanFolderRepoStub) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return r.folders, nil
}
