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
	EnqueueScanRun(context.Context, int, string, string, string) (*models.ScanRun, bool, error)
	Wait(context.Context, string) (*models.ScanRun, error)
}
type PathMapper interface{ MapFolder(string) (string, error) }
type AudiobookPathMapper = PathMapper
type AudiobookScanResolver interface {
	Resolve(context.Context, scantrigger.Request) (*scantrigger.Target, error)
}
type AudiobookFileLookup interface {
	FindContentIDByRootPath(context.Context, int, string, string) (string, error)
}
type AudiobookItemLookup interface {
	GetByID(context.Context, string) (*models.MediaItem, error)
}
type PathMapperFactory func(map[string]any) (PathMapper, error)
type AudiobookPathMapperFactory = PathMapperFactory

type AudiobookImportLinker struct {
	queue    AudiobookScanQueue
	resolver AudiobookScanResolver
	files    AudiobookFileLookup
	items    AudiobookItemLookup
	mapper   PathMapper
	factory  PathMapperFactory
}

func NewAudiobookImportLinker(queue AudiobookScanQueue, resolver AudiobookScanResolver, files AudiobookFileLookup, items AudiobookItemLookup, mapper PathMapper) *AudiobookImportLinker {
	return &AudiobookImportLinker{queue: queue, resolver: resolver, files: files, items: items, mapper: mapper}
}
func (l *AudiobookImportLinker) SetPathMapperFactory(factory PathMapperFactory) {
	if l != nil {
		l.factory = factory
	}
}
func (l *AudiobookImportLinker) Link(ctx context.Context, req Request, status RouterTargetStatus) (RequestLifecycle, error) {
	return l.link(ctx, req, status, l.mapper)
}
func (l *AudiobookImportLinker) LinkWithConnection(ctx context.Context, req Request, status RouterTargetStatus, conn ResolvedRouterConnection) (RequestLifecycle, error) {
	mapper := l.mapper
	if l.factory != nil {
		var err error
		mapper, err = l.factory(conn.Config)
		if err != nil {
			return RequestLifecycle{}, fmt.Errorf("configure audiobook path mapper: %w", err)
		}
	}
	return l.link(ctx, req, status, mapper)
}
func (l *AudiobookImportLinker) link(ctx context.Context, req Request, status RouterTargetStatus, mapper PathMapper) (RequestLifecycle, error) {
	if l == nil || l.queue == nil || l.resolver == nil || l.files == nil || l.items == nil || mapper == nil {
		return RequestLifecycle{}, errors.New("audiobook import linker is not configured")
	}
	if req.MediaType != MediaTypeAudiobook {
		return RequestLifecycle{}, fmt.Errorf("audiobook import linker received %s", req.MediaType)
	}
	if req.SiloAudiobookID != "" && req.SiloAudiobookLink != "" {
		return RequestLifecycle{SiloAudiobookID: req.SiloAudiobookID, SiloAudiobookLink: req.SiloAudiobookLink}, nil
	}
	if status.Metadata == nil || strings.TrimSpace(status.Metadata.ImportedPath) == "" {
		return RequestLifecycle{}, fmt.Errorf("%w: moved status has no basePath", ErrAudiobookScanFailed)
	}
	mapped, err := mapper.MapFolder(status.Metadata.ImportedPath)
	if err != nil {
		return RequestLifecycle{}, fmt.Errorf("map imported path: %w", err)
	}
	resolved, err := l.resolver.Resolve(ctx, scantrigger.Request{Path: mapped, Trigger: "request_router_moved"})
	if err != nil {
		return RequestLifecycle{}, fmt.Errorf("resolve mapped audiobook path: %w", err)
	}
	if resolved == nil || resolved.Folder == nil {
		return RequestLifecycle{}, errors.New("resolve mapped audiobook path: missing library")
	}
	runID := req.ScanRunID
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
			return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, waitErr
		}
		if run.Status != "completed" {
			return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, fmt.Errorf("%w: run %s ended %s: %s", ErrAudiobookScanFailed, run.ID, run.Status, run.ErrorMessage)
		}
	}
	id, err := l.files.FindContentIDByRootPath(ctx, resolved.Folder.ID, mapped, "audiobook")
	if err != nil {
		return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, fmt.Errorf("find imported audiobook: %w", err)
	}
	if id == "" {
		return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, ErrAudiobookNotFound
	}
	item, err := l.items.GetByID(ctx, id)
	if err != nil {
		return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, fmt.Errorf("load imported audiobook: %w", err)
	}
	if item == nil || item.Type != "audiobook" {
		return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, Retryable: true}, fmt.Errorf("%w: content %s is not an audiobook", ErrAudiobookNotFound, id)
	}
	return RequestLifecycle{ImportedPath: mapped, ScanRunID: runID, SiloAudiobookID: id, SiloAudiobookLink: "/item/" + id}, nil
}
