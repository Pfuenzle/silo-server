package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/livetv"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/go-chi/chi/v5"
)

func TestRefreshLiveTVEPGTask_propertiesAndServerLocalTrigger(t *testing.T) {
	// Given a refresh task configured with a server-local clock.
	clock := func() time.Time {
		return time.Date(2026, 9, 10, 1, 59, 0, 0, time.FixedZone("fixture", 2*60*60))
	}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{Now: clock})

	// When its task metadata and default trigger are inspected.
	trigger := task.DefaultTriggers()

	// Then it is a metadata task with the stable key and local 02:00 schedule.
	if task.Key() != "refresh_livetv_epg" || task.Category() != taskmanager.TaskCategoryLibrary {
		t.Fatalf("task metadata = %q/%q, want refresh_livetv_epg/library", task.Key(), task.Category())
	}
	if len(trigger) != 1 || trigger[0].Type != taskmanager.TriggerTypeDaily || trigger[0].TimeOfDay != "02:00" {
		t.Fatalf("default trigger = %#v, want one daily 02:00 trigger", trigger)
	}
	if got := clock().Location().String(); got != "fixture" {
		t.Fatalf("clock location = %q, want fixture", got)
	}
}

func TestRefreshLiveTVEPGTask_refreshesEnabledSourcesAndContinuesAfterFailure(t *testing.T) {
	// Given enabled Live TV sources alongside disabled and non-Live-TV libraries.
	sources := &epgSourceRepoStub{sources: map[int][]livetv.Source{
		10: {
			{ID: 1, LibraryID: 10, SourceKey: "good", Enabled: true, RefreshState: "ready"},
			{ID: 2, LibraryID: 10, SourceKey: "bad", Enabled: true, RefreshState: "stale"},
			{ID: 3, LibraryID: 10, SourceKey: "disabled", Enabled: false, RefreshState: "ready"},
		},
	}}
	refresher := &epgRefresherStub{errors: map[int64]error{2: errors.New("fixture source failed")}}
	progress := &recordingProgress{}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders: &epgFolderRepoStub{folders: []*models.MediaFolder{
			{ID: 10, Type: "livetv", Enabled: true},
			{ID: 11, Type: "movies", Enabled: true},
		}},
		Sources: sources, Refresher: refresher, Now: time.Now,
	})

	// When the scheduled task executes.
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Then valid sources refresh, failures are reported, and execution completes.
	if got := refresher.calls(); got != 2 {
		t.Fatalf("refresh calls = %d, want 2 enabled sources", got)
	}
	if progress.lastMessage() != "Live TV EPG refresh completed with 1 stale sources" {
		t.Fatalf("last progress message = %q", progress.lastMessage())
	}
	var result liveTVRefreshResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	if result.Attempted != 2 || result.Refreshed != 1 || result.Failed != 1 || result.Stale != 1 {
		t.Fatalf("result = %#v, want attempted=2 refreshed=1 failed=1 stale=1", result)
	}
}

func TestRefreshLiveTVEPGTask_refreshesPlaylistsBeforeEPG(t *testing.T) {
	// Given repository ordering that places EPG sources before playlist sources.
	sources := &epgSourceRepoStub{sources: map[int][]livetv.Source{10: {
		{ID: 2, LibraryID: 10, Kind: livetv.SourceKindEPG, SourceKey: "epg", Enabled: true},
		{ID: 1, LibraryID: 10, Kind: livetv.SourceKindPlaylist, SourceKey: "playlist", Enabled: true},
	}}}
	refresher := &epgRefresherStub{}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders: &epgFolderRepoStub{folders: []*models.MediaFolder{{ID: 10, Type: "livetv", Enabled: true}}},
		Sources: sources, Refresher: refresher, Now: time.Now,
	})

	// When the scheduled task refreshes all enabled sources.
	if err := task.Execute(context.Background(), &recordingProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Then playlist channels exist before EPG rows are inserted and remain mapped.
	if got := refresher.order; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("refresh order = %v, want playlist 1 before EPG 2", got)
	}
}

func TestRefreshLiveTVEPGTask_passesPersistedMappingsToEPGRefresh(t *testing.T) {
	// Given an EPG source with a persisted playlist-to-provider mapping.
	sources := &epgSourceRepoStub{sources: map[int][]livetv.Source{10: {
		{ID: 1, LibraryID: 10, Kind: livetv.SourceKindPlaylist, SourceKey: "playlist", Enabled: true},
		{ID: 2, LibraryID: 10, Kind: livetv.SourceKindEPG, SourceKey: "epg", Enabled: true},
	}}}
	sources.mappings = map[int64]map[string]livetv.ChannelMapping{
		2: {"playlist-news": {ProviderID: "xml-news", DisplayName: "News"}},
	}
	refresher := &epgRefresherStub{}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders: &epgFolderRepoStub{folders: []*models.MediaFolder{{ID: 10, Type: "livetv", Enabled: true}}},
		Sources: sources, Refresher: refresher, Now: time.Now,
	})

	// When the scheduled refresh executes.
	if err := task.Execute(context.Background(), &recordingProgress{}); err != nil {
		t.Fatal(err)
	}

	// Then the persisted mapping reaches the EPG parser boundary.
	if got := refresher.mapping["playlist-news"]; got.ProviderID != "xml-news" {
		t.Fatalf("EPG mapping = %#v, want provider xml-news", refresher.mapping)
	}
}

func TestRefreshLiveTVEPGTask_resultUsesInjectedClock(t *testing.T) {
	// Given a task with a deterministic server clock and no configured sources.
	when := time.Date(2026, 9, 10, 2, 0, 0, 0, time.FixedZone("fixture", 2*60*60))
	progress := &recordingProgress{}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders:   &epgFolderRepoStub{folders: []*models.MediaFolder{}},
		Sources:   &epgSourceRepoStub{sources: map[int][]livetv.Source{}},
		Refresher: &epgRefresherStub{}, Now: func() time.Time { return when },
	})

	// When the task completes.
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Then the result records the injected local time, not wall-clock time.
	var result liveTVRefreshResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	if !result.CompletedAt.Equal(when.UTC()) {
		t.Fatalf("completed_at = %v, want %v", result.CompletedAt, when.UTC())
	}
}

func TestRefreshLiveTVEPGTask_deduplicatesConcurrentExecutions(t *testing.T) {
	// Given a refresh that remains active until the test releases it.
	refresher := &epgRefresherStub{started: make(chan struct{}), release: make(chan struct{})}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders:   &epgFolderRepoStub{folders: []*models.MediaFolder{{ID: 10, Type: "livetv", Enabled: true}}},
		Sources:   &epgSourceRepoStub{sources: map[int][]livetv.Source{10: {{ID: 1, LibraryID: 10, Enabled: true}}}},
		Refresher: refresher, Now: time.Now,
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- task.Execute(context.Background(), &recordingProgress{}) }()
	<-refresher.started

	// When a second execution is requested while the first is running.
	secondErr := task.Execute(context.Background(), &recordingProgress{})
	close(refresher.release)
	firstErr := <-firstDone

	// Then exactly one source refresh runs and the duplicate is rejected.
	if !errors.Is(secondErr, ErrLiveTVRefreshAlreadyRunning) {
		t.Fatalf("second error = %v, want ErrLiveTVRefreshAlreadyRunning", secondErr)
	}
	if firstErr != nil || refresher.calls() != 1 {
		t.Fatalf("first error/calls = %v/%d, want nil/1", firstErr, refresher.calls())
	}
}

func TestRefreshLiveTVEPGTask_adminRunPersistsResultAndRejectsDuplicate(t *testing.T) {
	// Given a production TaskManager with the registered Live TV refresh task.
	triggerRepo := &taskTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{}}
	historyRepo := &recordingExecutionRepository{done: make(chan struct{})}
	refresher := &epgRefresherStub{started: make(chan struct{}), release: make(chan struct{})}
	task := NewRefreshLiveTVEPGTask(RefreshLiveTVEPGTaskConfig{
		Folders:   &epgFolderRepoStub{folders: []*models.MediaFolder{{ID: 10, Type: "livetv", Enabled: true}}},
		Sources:   &epgSourceRepoStub{sources: map[int][]livetv.Source{10: {{ID: 1, LibraryID: 10, Enabled: true}}}},
		Refresher: refresher, Now: func() time.Time { return time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC) },
	})
	manager := taskmanager.New(triggerRepo, historyRepo, func(taskmanager.TriggerConfig) taskmanager.Trigger { return nil }, nil)
	manager.Register(task)
	registered := manager.ListTasks(true)
	if len(registered) != 1 || registered[0].Key != task.Key() || registered[0].Category != taskmanager.TaskCategoryLibrary {
		t.Fatalf("registered tasks = %#v, want refresh_livetv_epg/library", registered)
	}
	observer := &taskObserver{}
	manager.AddObserver(observer)
	handler := handlers.NewTaskHandler(manager, historyRepo, nil)
	router := chi.NewRouter()
	router.Post("/api/v1/admin/tasks/{key}/run", handler.HandleRunTask)
	router.Get("/api/v1/admin/tasks/{key}/history", handler.HandleGetHistory)

	// When the admin starts the task twice while the first run is active.
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/v1/admin/tasks/refresh_livetv_epg/run", nil))
	<-refresher.started
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/v1/admin/tasks/refresh_livetv_epg/run", nil))
	close(refresher.release)

	// Then the first request is accepted, the duplicate is rejected, and history has the result.
	if first.Code != http.StatusAccepted || second.Code != http.StatusConflict {
		t.Fatalf("run statuses = %d/%d, want 202/409", first.Code, second.Code)
	}
	select {
	case <-historyRepo.done:
	case <-time.After(time.Second):
		t.Fatal("task execution was not persisted")
	}
	history := httptest.NewRecorder()
	router.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/refresh_livetv_epg/history", nil))
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"task_key":"refresh_livetv_epg"`) || !strings.Contains(history.Body.String(), `"status":"completed"`) {
		t.Fatalf("history response = %d %s", history.Code, history.Body.String())
	}
	if !strings.Contains(history.Body.String(), `"sources_attempted":1`) || observer.runningProgress() == 0 {
		t.Fatalf("history/progress = %s/%v, want result data and live progress", history.Body.String(), observer.runningProgress())
	}
	if historyRepo.insertCount() != 1 {
		t.Fatalf("history rows = %d, want exactly one deduplicated execution", historyRepo.insertCount())
	}
}

type epgFolderRepoStub struct{ folders []*models.MediaFolder }

type taskTriggerRepository struct {
	mu       sync.Mutex
	triggers map[string][]taskmanager.TriggerConfig
}

func (r *taskTriggerRepository) GetTriggers(_ context.Context, key string) ([]taskmanager.TriggerConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]taskmanager.TriggerConfig(nil), r.triggers[key]...), nil
}

func (r *taskTriggerRepository) SetTriggers(_ context.Context, key string, triggers []taskmanager.TriggerConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triggers[key] = append([]taskmanager.TriggerConfig(nil), triggers...)
	return nil
}

type recordingExecutionRepository struct {
	mu      sync.Mutex
	inserts []taskmanager.ExecutionResult
	done    chan struct{}
}

type taskObserver struct {
	mu       sync.Mutex
	progress float64
}

func (o *taskObserver) TaskUpdated(info taskmanager.TaskInfo) {
	o.mu.Lock()
	if info.Key == "refresh_livetv_epg" && info.Progress > o.progress {
		o.progress = info.Progress
	}
	o.mu.Unlock()
}

func (o *taskObserver) runningProgress() float64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.progress
}

func (r *recordingExecutionRepository) Insert(_ context.Context, result taskmanager.ExecutionResult) error {
	r.mu.Lock()
	r.inserts = append(r.inserts, result)
	if len(r.inserts) == 1 {
		close(r.done)
	}
	r.mu.Unlock()
	return nil
}

func (r *recordingExecutionRepository) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}

func (r *recordingExecutionRepository) List(_ context.Context, key string, _ int) ([]taskmanager.ExecutionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := make([]taskmanager.ExecutionResult, 0, len(r.inserts))
	for _, result := range r.inserts {
		if result.TaskKey == key {
			results = append(results, result)
		}
	}
	return results, nil
}

func (r *recordingExecutionRepository) insertCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inserts)
}

func (r *epgFolderRepoStub) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return r.folders, nil
}

type epgSourceRepoStub struct {
	sources  map[int][]livetv.Source
	mappings map[int64]map[string]livetv.ChannelMapping
}

func (r *epgSourceRepoStub) ListEPGChannelMappings(_ context.Context, _ int, sourceID int64) (map[string]livetv.ChannelMapping, error) {
	return r.mappings[sourceID], nil
}

func (r *epgSourceRepoStub) ListSources(_ context.Context, libraryID int) ([]livetv.Source, error) {
	return r.sources[libraryID], nil
}

func (r *epgSourceRepoStub) GetSource(_ context.Context, libraryID int, sourceKey string) (livetv.Source, error) {
	for _, source := range r.sources[libraryID] {
		if source.SourceKey == sourceKey {
			return source, nil
		}
	}
	return livetv.Source{}, errors.New("source not found")
}

type epgRefresherStub struct {
	mu      sync.Mutex
	count   int
	order   []int64
	errors  map[int64]error
	started chan struct{}
	release chan struct{}
	mapping map[string]livetv.ChannelMapping
}

func (r *epgRefresherStub) RefreshSource(ctx context.Context, source livetv.Source, mapping map[string]livetv.ChannelMapping) (livetv.Diagnostics, error) {
	r.mu.Lock()
	r.count++
	r.order = append(r.order, source.ID)
	if r.started != nil {
		select {
		case <-r.started:
		default:
			close(r.started)
		}
	}
	release := r.release
	err := r.errors[source.ID]
	if source.Kind == livetv.SourceKindEPG {
		r.mapping = mapping
	}
	r.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, err
}

func (r *epgRefresherStub) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

type recordingProgress struct {
	mu      sync.Mutex
	message string
	result  json.RawMessage
}

func (p *recordingProgress) Report(_ float64, message string) {
	p.mu.Lock()
	p.message = message
	p.mu.Unlock()
}

func (p *recordingProgress) SetResultData(data json.RawMessage) {
	p.mu.Lock()
	p.result = append(json.RawMessage(nil), data...)
	p.mu.Unlock()
}

func (p *recordingProgress) lastMessage() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.message
}
