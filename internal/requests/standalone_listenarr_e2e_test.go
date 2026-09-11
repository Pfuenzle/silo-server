package requests

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestStandaloneListenarr_approvalMovedScanAndLinkAcrossProcess(t *testing.T) {
	// Given an isolated Listenarr HTTPS fixture and a real standalone plugin process.
	binaryPath := os.Getenv("SILO_LISTENARR_PLUGIN_BINARY")
	if binaryPath == "" {
		t.Skip("SILO_LISTENARR_PLUGIN_BINARY is not set")
	}
	manifest := standaloneListenarrManifest(t, binaryPath)
	var mu sync.Mutex
	calls := map[string]int{}
	keys := map[string]bool{}
	server := privateListenarrServer(t, func(w http.ResponseWriter, r *http.Request) {
		operation := r.Method + " " + r.URL.Path
		mu.Lock()
		calls[operation]++
		if key := r.Header.Get("X-Idempotency-Key"); key != "" {
			keys[operation+":"+key] = true
		}
		mu.Unlock()
		switch operation {
		case "POST /api/v1/library/add":
			_, _ = w.Write([]byte(`{"id":41}`))
		case "POST /api/v1/download/search-and-download":
			_, _ = w.Write([]byte(`{"success":true,"downloadId":"dl-7"}`))
		case "GET /api/v1/downloads/dl-7":
			_, _ = w.Write([]byte(`{"id":"dl-7","audiobookId":41,"status":"Moved","finalPath":"/listenarr/imports/作者/Title"}`))
		case "GET /api/v1/library/41":
			_, _ = w.Write([]byte(`{"id":41,"title":"The Book","basePath":"/listenarr/imports"}`))
		default:
			http.NotFound(w, r)
		}
	})

	config := map[string]any{
		"base_url":               server.URL,
		"allow_private_networks": true,
		"timeout":                "2s",
		"poll_interval":          "1ms",
		"poll_timeout":           "2s",
		"max_polls":              1,
		"max_body_bytes":         1 << 20,
	}
	configValue, err := structpb.NewStruct(config)
	if err != nil {
		t.Fatalf("encode plugin config: %v", err)
	}
	host := pluginhost.NewHost(pluginhost.Config{})
	t.Cleanup(func() {
		if err := host.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown plugin host: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := host.Start(ctx, pluginhost.StartRequest{
		InstallationID: 901,
		BinaryPath:     binaryPath,
		Manifest:       manifest,
		Config:         []*pluginv1.ConfigEntry{{Key: "listenarr", Value: configValue}},
	})
	if err != nil {
		t.Fatalf("start standalone plugin: %v", err)
	}
	routerClient, err := client.RequestRouter("listenarr")
	if err != nil {
		t.Fatalf("bind request router: %v", err)
	}
	provider := NewPluginRouterProvider(standaloneRouterResolver{client: clearAPIKeyRouterClient{client: routerClient}})

	store := newFakeStore()
	store.integrations = []Integration{{
		ID: "listenarr-connection", Name: "isolated Listenarr", Enabled: true,
		BaseURL: server.URL, APIKeyRef: "test-api-key", CapabilityID: "listenarr",
		InstallationID: intPtr(901), SupportedMediaTypes: []string{string(MediaTypeAudiobook)},
		PluginConfig: map[string]any{
			"path_mappings": []any{map[string]any{"source": "/listenarr/imports", "destination": "/silo/audiobooks"}},
			"silo_root":     "/silo/audiobooks",
		},
	}}
	request := &Request{ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1", MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending, Outcome: OutcomeActive, RequestedByUserID: 1}
	store.requests[request.ID] = request
	service := newTestService(store)
	service.SetRouterProvider(provider)
	service.SetEntitlementResolver(fixedCeiling{q: "1080p"})
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := NewAudiobookImportLinker(queue, importResolver{target: &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: "/silo/audiobooks/作者/Title"}}, importFiles{contentID: "book-1"}, importItems{item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"}}, nil)
	linker.SetPathMapperFactory(func(value map[string]any) (PathMapper, error) {
		return pathMapperFromE2EConfig(value)
	})
	service.SetAudiobookImportLinker(linker)

	// When an administrator approves the request through the generic provider.
	approved, err := service.Approve(ctx, Viewer{UserID: 1, IsAdmin: true}, request.ID)
	if err != nil {
		t.Fatalf("approve request: %v", err)
	}
	if approved.Status != StatusQueued || approved.Outcome != OutcomeActive {
		t.Fatalf("approved request = %+v, want queued active request", approved)
	}
	if err := host.Stop(901); err != nil {
		t.Fatalf("restart preparation: stop plugin: %v", err)
	}
	client, err = host.Start(ctx, pluginhost.StartRequest{
		InstallationID: 901,
		BinaryPath:     binaryPath,
		Manifest:       manifest,
		Config:         []*pluginv1.ConfigEntry{{Key: "listenarr", Value: configValue}},
	})
	if err != nil {
		t.Fatalf("restart plugin: %v", err)
	}
	routerClient, err = client.RequestRouter("listenarr")
	if err != nil {
		t.Fatalf("bind router after restart: %v", err)
	}
	service.SetRouterProvider(NewPluginRouterProvider(standaloneRouterResolver{client: clearAPIKeyRouterClient{client: routerClient}}))

	// Then reconciliation crosses the real plugin RPC, Moved metadata, mapping, one scan, and catalog link.
	store.candidates = []*Request{store.requests[request.ID]}
	result, err := service.ReconcileRequests(ctx, 100)
	if err != nil {
		t.Fatalf("reconcile request: %v", err)
	}
	got := store.requests[request.ID]
	if result.Completed != 1 || got.Status != StatusCompleted || got.Outcome != OutcomeActive {
		t.Fatalf("reconcile result=%+v request=%+v, want one completed request", result, got)
	}
	if got.ImportedPath != "/silo/audiobooks/作者/Title" || got.ScanRunID != "scan-1" || got.SiloAudiobookID != "book-1" || got.SiloAudiobookLink != "/api/v1/items/book-1" {
		t.Fatalf("durable lifecycle = %+v, want mapped path, scan, and audiobook link", got)
	}
	if queue.enqueues != 1 || queue.waits != 1 {
		t.Fatalf("scan queue = %+v, want exactly one enqueue and one wait", queue)
	}

	// Replay/reconcile is a no-op after durable completion and cannot enqueue a second scan.
	if _, err := service.ReconcileRequests(ctx, 100); err != nil {
		t.Fatalf("replay reconcile: %v", err)
	}
	if queue.enqueues != 1 || got.SiloAudiobookLink != "/api/v1/items/book-1" {
		t.Fatalf("replay changed lifecycle: queue=%+v request=%+v", queue, got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["POST /api/v1/library/add"] != 1 || calls["POST /api/v1/download/search-and-download"] != 1 || !keys["POST /api/v1/library/add:book-1"] || !keys["POST /api/v1/download/search-and-download:book-1"] {
		t.Fatalf("Listenarr mutation receipts = calls=%+v keys=%+v, want one stable-key add/search", calls, keys)
	}
}

func TestStandaloneListenarr_malformedMovedMetadataNeverEnqueuesScan(t *testing.T) {
	// Given a real plugin process returning Moved without an imported path.
	binaryPath := os.Getenv("SILO_LISTENARR_PLUGIN_BINARY")
	if binaryPath == "" {
		t.Skip("SILO_LISTENARR_PLUGIN_BINARY is not set")
	}
	manifest := standaloneListenarrManifest(t, binaryPath)
	server := privateListenarrServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/downloads/dl-7" {
			_, _ = w.Write([]byte(`{"id":"dl-7","audiobookId":41,"status":"Moved"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":41}`))
	})
	configValue, err := structpb.NewStruct(map[string]any{"base_url": server.URL, "allow_private_networks": true, "max_polls": 1})
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	host := pluginhost.NewHost(pluginhost.Config{})
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 902, BinaryPath: binaryPath, Manifest: manifest, Config: []*pluginv1.ConfigEntry{{Key: "listenarr", Value: configValue}}})
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	routerClient, err := client.RequestRouter("listenarr")
	if err != nil {
		t.Fatalf("bind router: %v", err)
	}
	status, err := routerClient.CheckStatus(ctx, &pluginv1.CheckStatusRequest{Request: &pluginv1.RequestDescriptor{ExternalIds: map[string]string{"external_library_id": "41"}}, Targets: []*pluginv1.TargetRef{{Quality: "1080p", ConnectionId: "c1", ExternalId: "dl-7"}}, Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: server.URL}}})
	if err != nil {
		t.Fatalf("check status: %v", err)
	}
	if len(status.GetStatuses()) != 1 || status.GetStatuses()[0].GetMetadata().GetImportedPath() != "" {
		t.Fatalf("status = %s, want completed status with absent imported path", status)
	}
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	linker := NewAudiobookImportLinker(queue, importResolver{target: &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}}}, importFiles{}, importItems{}, importMapper{path: "/silo/audiobooks"})
	_, err = linker.Link(ctx, Request{MediaType: MediaTypeAudiobook}, RouterTargetStatus{Status: StatusCompleted, Metadata: &RouterMetadata{}})
	if !errors.Is(err, ErrAudiobookScanFailed) || queue.enqueues != 0 {
		t.Fatalf("malformed Moved metadata err=%v queue=%+v, want retryable failure before scan", err, queue)
	}
}
