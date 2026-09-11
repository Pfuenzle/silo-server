package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/livetv"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

var ErrLiveTVRefreshAlreadyRunning = errors.New("Live TV EPG refresh is already running")

type liveTVFolderSource interface {
	GetEnabled(context.Context) ([]*models.MediaFolder, error)
}

type liveTVSourceLister interface {
	ListSources(context.Context, int) ([]livetv.Source, error)
	GetSource(context.Context, int, string) (livetv.Source, error)
}

type liveTVSourceRefresher interface {
	RefreshSource(context.Context, livetv.Source, map[string]livetv.ChannelMapping) (livetv.Diagnostics, error)
}

type liveTVRefreshResult struct {
	Attempted   int                  `json:"sources_attempted"`
	Refreshed   int                  `json:"sources_refreshed"`
	Failed      int                  `json:"sources_failed"`
	Stale       int                  `json:"sources_stale"`
	CompletedAt time.Time            `json:"completed_at"`
	Sources     []liveTVSourceResult `json:"sources"`
}

type liveTVSourceResult struct {
	LibraryID int    `json:"library_id"`
	SourceKey string `json:"source_key"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type RefreshLiveTVEPGTask struct {
	folders   liveTVFolderSource
	sources   liveTVSourceLister
	refresher liveTVSourceRefresher
	now       func() time.Time
	runMu     sync.Mutex
	runActive bool
}

type RefreshLiveTVEPGTaskConfig struct {
	Folders   liveTVFolderSource
	Sources   liveTVSourceLister
	Refresher liveTVSourceRefresher
	Now       func() time.Time
}

func NewRefreshLiveTVEPGTask(config RefreshLiveTVEPGTaskConfig) *RefreshLiveTVEPGTask {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &RefreshLiveTVEPGTask{folders: config.Folders, sources: config.Sources, refresher: config.Refresher, now: config.Now}
}

func (t *RefreshLiveTVEPGTask) Key() string  { return "refresh_livetv_epg" }
func (t *RefreshLiveTVEPGTask) Name() string { return "Refresh Live TV EPG" }
func (t *RefreshLiveTVEPGTask) Description() string {
	return "Refreshes enabled Live TV playlist and EPG sources while preserving the last good guide on source failure"
}
func (t *RefreshLiveTVEPGTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *RefreshLiveTVEPGTask) IsHidden() bool { return false }
func (t *RefreshLiveTVEPGTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00"}}
}

func (t *RefreshLiveTVEPGTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if !t.tryStart() {
		return ErrLiveTVRefreshAlreadyRunning
	}
	defer t.finish()
	if t.folders == nil || t.sources == nil || t.refresher == nil {
		return errors.New("Live TV EPG refresh is not configured")
	}

	progress.Report(0, "Loading enabled Live TV libraries")
	folders, err := t.folders.GetEnabled(ctx)
	if err != nil {
		return fmt.Errorf("list enabled libraries: %w", err)
	}
	result := liveTVRefreshResult{}
	var targets []livetv.Source
	for _, folder := range folders {
		if folder == nil || !librarykind.IsLiveTV(folder.Type) {
			continue
		}
		sources, listErr := t.sources.ListSources(ctx, folder.ID)
		if listErr != nil {
			return fmt.Errorf("list Live TV sources for library %d: %w", folder.ID, listErr)
		}
		for _, source := range sources {
			if !source.Enabled {
				continue
			}
			targets = append(targets, source)
		}
	}
	for index, source := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Attempted++
		progress.Report(float64(index)/float64(len(targets))*100, fmt.Sprintf("Refreshing %s", source.Name))
		_, refreshErr := t.refresher.RefreshSource(ctx, source, nil)
		if refreshErr != nil {
			result.Failed++
			result.Stale++
			result.Sources = append(result.Sources, liveTVSourceResult{LibraryID: source.LibraryID, SourceKey: source.SourceKey, Status: "stale", Error: refreshErr.Error()})
			continue
		}
		latest, statusErr := t.sources.GetSource(ctx, source.LibraryID, source.SourceKey)
		if statusErr != nil {
			result.Failed++
			result.Stale++
			result.Sources = append(result.Sources, liveTVSourceResult{LibraryID: source.LibraryID, SourceKey: source.SourceKey, Status: "stale", Error: statusErr.Error()})
			continue
		}
		if latest.RefreshState == "stale" {
			result.Failed++
			result.Stale++
			result.Sources = append(result.Sources, liveTVSourceResult{LibraryID: source.LibraryID, SourceKey: source.SourceKey, Status: "stale", Error: latest.RefreshError})
			continue
		}
		result.Refreshed++
		result.Sources = append(result.Sources, liveTVSourceResult{LibraryID: source.LibraryID, SourceKey: source.SourceKey, Status: "ready"})
	}
	result.CompletedAt = t.now().UTC()
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return fmt.Errorf("marshal Live TV refresh result: %w", marshalErr)
	}
	progress.SetResultData(data)
	if result.Stale == 0 {
		progress.Report(100, fmt.Sprintf("Live TV EPG refresh completed (%d sources)", result.Refreshed))
	} else {
		progress.Report(100, fmt.Sprintf("Live TV EPG refresh completed with %d stale sources", result.Stale))
	}
	return nil
}

func (t *RefreshLiveTVEPGTask) tryStart() bool {
	t.runMu.Lock()
	defer t.runMu.Unlock()
	if t.runActive {
		return false
	}
	t.runActive = true
	return true
}

func (t *RefreshLiveTVEPGTask) finish() {
	t.runMu.Lock()
	t.runActive = false
	t.runMu.Unlock()
}
