package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const settingsPublishTestResponseTimeout = time.Second + 500*time.Millisecond

type blockingSettingsEventBus struct {
	mu sync.Mutex

	published chan cache.Event
	count     int
}

func newBlockingSettingsEventBus() *blockingSettingsEventBus {
	return &blockingSettingsEventBus{published: make(chan cache.Event, 2)}
}

func (b *blockingSettingsEventBus) Publish(ctx context.Context, _ string, event cache.Event) error {
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	b.published <- event
	<-ctx.Done()
	return ctx.Err()
}

func (*blockingSettingsEventBus) Subscribe(context.Context, string, cache.EventHandler) error {
	return nil
}

func (*blockingSettingsEventBus) Close() error {
	return nil
}

func (b *blockingSettingsEventBus) publishCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

func TestHandleUpdateSettings_returnsOKWithinSharedPublishBudget_whenEventBusBlocks(t *testing.T) {
	// Given
	requestContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	bus := newBlockingSettingsEventBus()
	updates := make(chan string, 2)
	restarts := NewServerRestartStatusTracker()
	handler := &AdminHandler{
		SettingsRepo:  settings,
		EventBus:      bus,
		RestartStatus: restarts,
		OnServerSettingUpdated: func(_ context.Context, key, _ string) {
			updates <- key
		},
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"database.max_connections":"40","branding.server_name":"Casa"}}`),
	).WithContext(requestContext)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleUpdateSettings(response, req)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// When
	firstPublished := <-bus.published
	timer := time.NewTimer(settingsPublishTestResponseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("handler did not return within the shared publication budget")
	}

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if firstPublished.Payload != "branding.server_name" {
		t.Fatalf("first published key = %q, want sorted branding.server_name", firstPublished.Payload)
	}
	if bus.publishCount() != 1 {
		t.Fatalf("publish calls = %d, want one shared-budget attempt", bus.publishCount())
	}
	if !restarts.Snapshot().RestartRequired {
		t.Fatal("restart requirement was not recorded before publication completed")
	}
	for range 2 {
		select {
		case <-updates:
		default:
			t.Fatal("setting callback was not applied before publication completed")
		}
	}
}

func TestHandleUpdateSetting_returnsOKWithinPublishBudget_whenEventBusBlocks(t *testing.T) {
	// Given
	requestContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	bus := newBlockingSettingsEventBus()
	updates := make(chan string, 1)
	restarts := NewServerRestartStatusTracker()
	handler := &AdminHandler{
		SettingsRepo:  settings,
		EventBus:      bus,
		RestartStatus: restarts,
		OnServerSettingUpdated: func(_ context.Context, key, _ string) {
			updates <- key
		},
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/database.max_connections",
		strings.NewReader(`{"value":"40"}`),
	)
	req = withChiParam(req.WithContext(requestContext), "key", "database.max_connections")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleUpdateSetting(response, req)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// When
	<-bus.published
	timer := time.NewTimer(settingsPublishTestResponseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("handler did not return within the publication budget")
	}

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if bus.publishCount() != 1 {
		t.Fatalf("publish calls = %d, want 1", bus.publishCount())
	}
	if !restarts.Snapshot().RestartRequired {
		t.Fatal("restart requirement was not recorded before publication completed")
	}
	select {
	case <-updates:
	default:
		t.Fatal("setting callback was not applied before publication completed")
	}
}
