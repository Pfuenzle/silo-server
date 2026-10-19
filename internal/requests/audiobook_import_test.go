package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type audiobookImportQueue struct {
	runs    []*models.ScanRun
	waitErr error
	waited  []string
}

func (q *audiobookImportQueue) EnqueueScanRun(_ context.Context, folderID int, mode, path, trigger string) (*models.ScanRun, bool, error) {
	run := &models.ScanRun{ID: "run-new", Status: "completed", MediaFolderID: folderID}
	q.runs = append(q.runs, run)
	return run, true, nil
}
func (q *audiobookImportQueue) Wait(_ context.Context, id string) (*models.ScanRun, error) {
	q.waited = append(q.waited, id)
	if q.waitErr != nil {
		return nil, q.waitErr
	}
	return &models.ScanRun{ID: id, Status: "completed"}, nil
}

type audiobookImportResolver struct {
	target *scantrigger.Target
	err    error
}

func (r audiobookImportResolver) Resolve(context.Context, scantrigger.Request) (*scantrigger.Target, error) {
	return r.target, r.err
}

type audiobookImportFiles struct {
	id  string
	err error
}

func (f audiobookImportFiles) FindContentIDByRootPath(context.Context, int, string, string) (string, error) {
	return f.id, f.err
}

type audiobookImportItems struct {
	item *models.MediaItem
	err  error
}

func (i audiobookImportItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return i.item, i.err
}

type audiobookImportMapper string

func (m audiobookImportMapper) MapFolder(string) (string, error) { return string(m), nil }

func TestAudiobookImportLinkerScansAndLinksImportedBook(t *testing.T) {
	folder := &models.MediaFolder{ID: 7}
	queue := &audiobookImportQueue{}
	linker := NewAudiobookImportLinker(queue, audiobookImportResolver{target: &scantrigger.Target{Folder: folder, Mode: scantrigger.ModeSubtree, Path: "/library/book"}}, audiobookImportFiles{id: "book-1"}, audiobookImportItems{item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"}}, audiobookImportMapper("/library/book"))
	got, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, RouterTargetStatus{Metadata: &RouterMetadata{ImportedPath: "/incoming/book"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ScanRunID != "run-new" || got.ImportedPath != "/library/book" || got.SiloAudiobookID != "book-1" || got.SiloAudiobookLink != "/item/book-1" {
		t.Fatalf("lifecycle = %+v", got)
	}
	if len(queue.runs) != 1 || queue.runs[0].MediaFolderID != 7 {
		t.Fatalf("queued scans = %+v", queue.runs)
	}
}

func TestAudiobookImportLinkerWaitsExistingScan(t *testing.T) {
	folder := &models.MediaFolder{ID: 7}
	queue := &audiobookImportQueue{}
	linker := NewAudiobookImportLinker(queue, audiobookImportResolver{target: &scantrigger.Target{Folder: folder, Mode: scantrigger.ModeSubtree, Path: "/library/book"}}, audiobookImportFiles{id: "book-1"}, audiobookImportItems{item: &models.MediaItem{Type: "audiobook"}}, audiobookImportMapper("/library/book"))
	got, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook, ScanRunID: "existing"}, RouterTargetStatus{Metadata: &RouterMetadata{ImportedPath: "/incoming/book"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ScanRunID != "existing" || len(queue.runs) != 0 {
		t.Fatalf("lifecycle=%+v queued=%d", got, len(queue.runs))
	}
}

func TestAudiobookImportLinkerReturnsRetryableScanFailure(t *testing.T) {
	folder := &models.MediaFolder{ID: 7}
	queue := &audiobookImportQueue{waitErr: errors.New("scan wait failed")}
	linker := NewAudiobookImportLinker(queue, audiobookImportResolver{target: &scantrigger.Target{Folder: folder, Mode: scantrigger.ModeSubtree, Path: "/library/book"}}, audiobookImportFiles{id: "book-1"}, audiobookImportItems{item: &models.MediaItem{Type: "audiobook"}}, audiobookImportMapper("/library/book"))
	got, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, RouterTargetStatus{Metadata: &RouterMetadata{ImportedPath: "/incoming/book"}})
	if err == nil || !got.Retryable || got.ScanRunID != "run-new" {
		t.Fatalf("lifecycle=%+v err=%v", got, err)
	}
}
