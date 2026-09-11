package requests

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

var (
	ErrAudiobookScanFailed = errors.New("audiobook scan failed")
	ErrAudiobookNotFound   = errors.New("imported audiobook not found")
)

type AudiobookScanQueue interface {
	EnqueueScanRun(ctx context.Context, folderID int, mode, path, trigger string) (*models.ScanRun, bool, error)
	Wait(ctx context.Context, id string) (*models.ScanRun, error)
}

type PathMapper interface {
	MapFolder(sourcePath string) (string, error)
}

type AudiobookPathMapper = PathMapper

type AudiobookScanResolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
}

type AudiobookFileLookup interface {
	FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error)
}

type AudiobookItemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

type AudiobookImportLinker struct {
	queue    AudiobookScanQueue
	resolver AudiobookScanResolver
	files    AudiobookFileLookup
	items    AudiobookItemLookup
	mapper   PathMapper
	factory  PathMapperFactory
}

type PathMapperFactory func(config map[string]any) (PathMapper, error)

type AudiobookPathMapperFactory = PathMapperFactory

func (l *AudiobookImportLinker) SetPathMapperFactory(factory PathMapperFactory) {
	if l != nil {
		l.factory = factory
	}
}

func NewAudiobookImportLinker(queue AudiobookScanQueue, resolver AudiobookScanResolver, files AudiobookFileLookup, items AudiobookItemLookup, mapper PathMapper) *AudiobookImportLinker {
	return &AudiobookImportLinker{queue: queue, resolver: resolver, files: files, items: items, mapper: mapper}
}

func (l *AudiobookImportLinker) Link(ctx context.Context, request Request, status RouterTargetStatus) (RequestLifecycle, error) {
	return l.link(ctx, request, status, l.mapper)
}

func (l *AudiobookImportLinker) LinkWithConnection(ctx context.Context, request Request, status RouterTargetStatus, connection ResolvedRouterConnection) (RequestLifecycle, error) {
	mapper := l.mapper
	if l.factory != nil {
		var err error
		mapper, err = l.factory(connection.Config)
		if err != nil {
			return RequestLifecycle{}, fmt.Errorf("configure audiobook path mapper: %w", err)
		}
	}
	return l.link(ctx, request, status, mapper)
}

func (l *AudiobookImportLinker) link(ctx context.Context, request Request, status RouterTargetStatus, mapper PathMapper) (RequestLifecycle, error) {
	if l == nil || l.queue == nil || l.resolver == nil || l.files == nil || l.items == nil || mapper == nil {
		return RequestLifecycle{}, fmt.Errorf("audiobook import linker is not configured")
	}
	if request.MediaType != MediaTypeAudiobook {
		return RequestLifecycle{}, fmt.Errorf("audiobook import linker received %s", request.MediaType)
	}
	if request.SiloAudiobookID != "" && request.SiloAudiobookLink != "" {
		return RequestLifecycle{SiloAudiobookID: request.SiloAudiobookID, SiloAudiobookLink: request.SiloAudiobookLink}, nil
	}
	if status.Metadata == nil || strings.TrimSpace(status.Metadata.ImportedPath) == "" {
		return RequestLifecycle{}, fmt.Errorf("%w: moved status has no basePath", ErrAudiobookScanFailed)
	}
	mappedPath, err := mapper.MapFolder(status.Metadata.ImportedPath)
	if err != nil {
		return RequestLifecycle{}, fmt.Errorf("map imported path: %w", err)
	}
	resolved, err := l.resolver.Resolve(ctx, scantrigger.Request{Path: mappedPath, Trigger: "request_router_moved"})
	if err != nil {
		return RequestLifecycle{}, fmt.Errorf("resolve mapped audiobook path: %w", err)
	}
	if resolved == nil || resolved.Folder == nil {
		return RequestLifecycle{}, fmt.Errorf("resolve mapped audiobook path: missing library")
	}
	runID := request.ScanRunID
	if runID != "" {
		run, waitErr := l.queue.Wait(ctx, runID)
		if waitErr != nil {
			return RequestLifecycle{}, waitErr
		}
		if run.Status == "failed" || run.Status == "cancelled" {
			runID = ""
		} else if run.Status != "completed" {
			return RequestLifecycle{}, fmt.Errorf("%w: run %s ended in unexpected state %s", ErrAudiobookScanFailed, run.ID, run.Status)
		}
	}
	if runID == "" {
		run, _, enqueueErr := l.queue.EnqueueScanRun(ctx, resolved.Folder.ID, resolved.Mode, resolved.Path, "request_router_moved")
		if enqueueErr != nil {
			return RequestLifecycle{}, fmt.Errorf("enqueue audiobook scan: %w", enqueueErr)
		}
		runID = run.ID
		run, waitErr := l.queue.Wait(ctx, runID)
		if waitErr != nil {
			return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, waitErr
		}
		if run.Status != "completed" {
			return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, fmt.Errorf("%w: run %s ended %s: %s", ErrAudiobookScanFailed, run.ID, run.Status, run.ErrorMessage)
		}
	}
	contentID, err := l.files.FindContentIDByRootPath(ctx, resolved.Folder.ID, mappedPath, "audiobook")
	if err != nil {
		return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, fmt.Errorf("find imported audiobook: %w", err)
	}
	if contentID == "" {
		return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, ErrAudiobookNotFound
	}
	item, err := l.items.GetByID(ctx, contentID)
	if err != nil {
		return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, fmt.Errorf("load imported audiobook: %w", err)
	}
	if item == nil || item.Type != "audiobook" {
		return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, Retryable: true}, fmt.Errorf("%w: content %s is not an audiobook", ErrAudiobookNotFound, contentID)
	}
	return RequestLifecycle{ImportedPath: mappedPath, ScanRunID: runID, SiloAudiobookID: contentID, SiloAudiobookLink: "/api/v1/items/" + contentID}, nil
}
