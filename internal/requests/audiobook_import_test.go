package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type importMapper struct {
	path string
	err  error
}

func (m importMapper) MapFolder(string) (string, error) { return m.path, m.err }

type importResolver struct {
	target *scantrigger.Target
	err    error
}

func (r importResolver) Resolve(context.Context, scantrigger.Request) (*scantrigger.Target, error) {
	return r.target, r.err
}

type importQueue struct {
	run       *models.ScanRun
	enqueues  int
	waits     int
	waitError error
}

func (q *importQueue) EnqueueScanRun(context.Context, int, string, string, string) (*models.ScanRun, bool, error) {
	q.enqueues++
	return q.run, true, nil
}

func (q *importQueue) Wait(context.Context, string) (*models.ScanRun, error) {
	q.waits++
	return q.run, q.waitError
}

type importFiles struct {
	contentID string
	err       error
}

func (f importFiles) FindContentIDByRootPath(context.Context, int, string, string) (string, error) {
	return f.contentID, f.err
}

type importItems struct {
	item *models.MediaItem
	err  error
}

func (i importItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return i.item, i.err
}

func newImportLinker(queue *importQueue, files importFiles, items importItems) *AudiobookImportLinker {
	return NewAudiobookImportLinker(queue, importResolver{target: &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: "/mnt/books/Author/Title"}}, files, items, importMapper{path: "/mnt/books/Author/Title"})
}

func movedStatus() RouterTargetStatus {
	return RouterTargetStatus{Status: StatusCompleted, ExternalStatus: "Moved", Metadata: &RouterMetadata{ImportedPath: "/provider/Author/Title"}}
}

func TestAudiobookImportLinker_success_persistsOneScanAndLink(t *testing.T) {
	// Given
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := newImportLinker(queue, importFiles{contentID: "book-1"}, importItems{item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"}})

	// When
	lifecycle, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, movedStatus())

	// Then
	if err != nil || queue.enqueues != 1 || queue.waits != 1 || lifecycle.ScanRunID != "scan-1" || lifecycle.SiloAudiobookLink != "/api/v1/items/book-1" {
		t.Fatalf("lifecycle=%+v err=%v queue=%+v", lifecycle, err, queue)
	}
}

func TestAudiobookImportLinker_duplicateActiveRun_reusesRun(t *testing.T) {
	// Given
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := newImportLinker(queue, importFiles{contentID: "book-1"}, importItems{item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"}})
	request := Request{MediaType: MediaTypeAudiobook}

	// When
	first, firstErr := linker.Link(context.Background(), request, movedStatus())
	request.ScanRunID = first.ScanRunID
	_, secondErr := linker.Link(context.Background(), request, movedStatus())

	// Then
	if firstErr != nil || secondErr != nil || queue.enqueues != 1 || queue.waits != 2 {
		t.Fatalf("errors=%v/%v queue=%+v", firstErr, secondErr, queue)
	}
}

func TestAudiobookImportLinker_scanFailure_isRetryable(t *testing.T) {
	// Given
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "failed", ErrorMessage: "scanner failed"}}
	linker := newImportLinker(queue, importFiles{}, importItems{})

	// When
	lifecycle, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, movedStatus())

	// Then
	if !errors.Is(err, ErrAudiobookScanFailed) || !lifecycle.Retryable || lifecycle.ScanRunID != "scan-1" {
		t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
	}
}

func TestAudiobookImportLinker_rejectsMappedPathBeforeEnqueue(t *testing.T) {
	// Given
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := NewAudiobookImportLinker(queue, importResolver{target: &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}}}, importFiles{}, importItems{}, importMapper{err: errors.New("audiobook path rejected")})

	// When
	_, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, movedStatus())

	// Then
	if err == nil || queue.enqueues != 0 {
		t.Fatalf("err=%v enqueues=%d", err, queue.enqueues)
	}
}

func TestAudiobookImportLinker_missingLibraryRecord_isRetryable(t *testing.T) {
	// Given
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := newImportLinker(queue, importFiles{}, importItems{})

	// When
	lifecycle, err := linker.Link(context.Background(), Request{MediaType: MediaTypeAudiobook}, movedStatus())

	// Then
	if !errors.Is(err, ErrAudiobookNotFound) || !lifecycle.Retryable {
		t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
	}
}
