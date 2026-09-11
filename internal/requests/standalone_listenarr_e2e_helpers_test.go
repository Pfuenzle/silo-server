package requests

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type standaloneRouterResolver struct{ client RouterClient }

func (r standaloneRouterResolver) RequestRouterClient(context.Context, int, string) (RouterClient, error) {
	return r.client, nil
}

type clearAPIKeyRouterClient struct {
	client *pluginhost.RequestRouterClient
}

func (c clearAPIKeyRouterClient) Fulfill(ctx context.Context, request *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	request = proto.Clone(request).(*pluginv1.FulfillRequest)
	for _, connection := range request.GetConnections() {
		connection.ApiKey = ""
	}
	return c.client.Fulfill(ctx, request)
}

func (c clearAPIKeyRouterClient) CheckStatus(ctx context.Context, request *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	request = proto.Clone(request).(*pluginv1.CheckStatusRequest)
	for _, connection := range request.GetConnections() {
		connection.ApiKey = ""
	}
	return c.client.CheckStatus(ctx, request)
}

func (c clearAPIKeyRouterClient) ListConfigOptions(ctx context.Context, request *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	return c.client.ListConfigOptions(ctx, request)
}

func (c clearAPIKeyRouterClient) TestConnection(ctx context.Context, request *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	return c.client.TestConnection(ctx, request)
}

func (c clearAPIKeyRouterClient) Validate(ctx context.Context, request *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	return c.client.Validate(ctx, request)
}

func standaloneListenarrManifest(t *testing.T, binaryPath string) *pluginv1.PluginManifest {
	t.Helper()
	output, err := exec.Command(binaryPath, "manifest").Output()
	if err != nil {
		t.Fatalf("read standalone manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(output, manifest); err != nil {
		t.Fatalf("decode standalone manifest: %v", err)
	}
	return manifest
}

func pathMapperFromE2EConfig(map[string]any) (PathMapper, error) {
	return e2ePathMapper{source: "/listenarr/imports", destination: "/silo/audiobooks"}, nil
}

type e2ePathMapper struct{ source, destination string }

func (m e2ePathMapper) MapFolder(sourcePath string) (string, error) {
	if !strings.HasPrefix(sourcePath, m.source+"/") {
		return "", errors.New("source path has no mapping")
	}
	return m.destination + strings.TrimPrefix(sourcePath, m.source), nil
}

func privateListenarrServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(privateInterfaceIPv4(t), "0"))
	if err != nil {
		t.Fatalf("listen private interface: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func privateInterfaceIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("list interfaces: %v", err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch typed := address.(type) {
			case *net.IPNet:
				ip = typed.IP
			case *net.IPAddr:
				ip = typed.IP
			}
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 interface available for process E2E")
	return ""
}
