package pluginhost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestStandaloneListenarrProcess_bindsGenericRequestRouter(t *testing.T) {
	// Given
	binaryPath := os.Getenv("SILO_LISTENARR_PLUGIN_BINARY")
	if binaryPath == "" {
		t.Skip("SILO_LISTENARR_PLUGIN_BINARY is not set")
	}
	manifestBytes, err := exec.Command(binaryPath, "manifest").Output()
	if err != nil {
		t.Fatalf("read standalone manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(manifestBytes, manifest); err != nil {
		t.Fatalf("decode standalone manifest: %v", err)
	}
	config, err := structpb.NewStruct(map[string]any{"base_url": "https://listenarr.example"})
	if err != nil {
		t.Fatalf("encode standalone config: %v", err)
	}
	host := pluginhost.NewHost(pluginhost.Config{})
	t.Cleanup(func() {
		if err := host.Shutdown(context.Background()); err != nil {
			t.Errorf("cleanup standalone plugin: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// When
	client, err := host.Start(ctx, pluginhost.StartRequest{
		InstallationID: 901,
		BinaryPath:     binaryPath,
		Manifest:       manifest,
		Config:         []*pluginv1.ConfigEntry{{Key: "listenarr", Value: config}},
	})
	if err != nil {
		t.Fatalf("start standalone plugin: %v", err)
	}
	router, err := client.RequestRouter("listenarr")
	if err != nil {
		t.Fatalf("bind request-router capability: %v", err)
	}
	response, err := router.Validate(ctx, &pluginv1.ValidateRequest{
		CapabilityId: "listenarr",
		Connection:   &pluginv1.RouterConnection{BaseUrl: "not-a-url"},
	})

	// Then
	if err != nil {
		t.Fatalf("validate standalone connection: %v", err)
	}
	t.Logf("request: {\"capability_id\":\"listenarr\",\"connection\":{\"base_url\":\"not-a-url\"}} response: %s", response.String())
	if response.GetFieldErrors()["base_url"] == "" {
		t.Fatalf("validation response = %s, want base_url error", response.String())
	}
}

func TestStandaloneListenarrProcess_rejectsLoopbackConnectionThroughHTTP(t *testing.T) {
	// Given
	binaryPath := os.Getenv("SILO_LISTENARR_PLUGIN_BINARY")
	if binaryPath == "" {
		t.Skip("SILO_LISTENARR_PLUGIN_BINARY is not set")
	}
	manifestBytes, err := exec.Command(binaryPath, "manifest").Output()
	if err != nil {
		t.Fatalf("read standalone manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(manifestBytes, manifest); err != nil {
		t.Fatalf("decode standalone manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/status" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "listenarr-test"})
	}))
	t.Cleanup(server.Close)
	config, err := structpb.NewStruct(map[string]any{"base_url": server.URL})
	if err != nil {
		t.Fatalf("encode standalone config: %v", err)
	}
	host := pluginhost.NewHost(pluginhost.Config{})
	t.Cleanup(func() {
		if err := host.Shutdown(context.Background()); err != nil {
			t.Errorf("cleanup standalone plugin: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// When
	client, err := host.Start(ctx, pluginhost.StartRequest{
		InstallationID: 902,
		BinaryPath:     binaryPath,
		Manifest:       manifest,
		Config:         []*pluginv1.ConfigEntry{{Key: "listenarr", Value: config}},
	})
	if err != nil {
		t.Fatalf("start standalone plugin: %v", err)
	}
	router, err := client.RequestRouter("listenarr")
	if err != nil {
		t.Fatalf("bind request-router capability: %v", err)
	}
	response, err := router.TestConnection(ctx, &pluginv1.TestConnectionRequest{
		CapabilityId: "listenarr",
		Connection:   &pluginv1.RouterConnection{BaseUrl: server.URL},
	})

	// Then
	if err != nil {
		t.Fatalf("test standalone connection: %v", err)
	}
	t.Logf("request: {\"capability_id\":\"listenarr\",\"connection\":{\"base_url\":%q}} response: %s", server.URL, response.String())
	if response.GetOk() || response.GetMessage() != "loopback, link-local, unspecified, and multicast endpoints are not allowed" {
		t.Fatalf("connection response = %s, want loopback rejection", response.String())
	}
}
