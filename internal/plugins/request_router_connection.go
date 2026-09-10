package plugins

import (
	"context"
	"fmt"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"google.golang.org/protobuf/types/known/structpb"
)

var runRequestRouterConnectionCheck = func(ctx context.Context, client *pluginhost.RequestRouterClient, manifest *pluginv1.PluginManifest, value map[string]any) error {
	capabilityID, err := requestRouterConnectionCheckCapabilityID(manifest)
	if err != nil {
		return err
	}
	baseURL, _ := value["base_url"].(string)
	apiKey, _ := value["api_key"].(string)
	config, err := structpb.NewStruct(value)
	if err != nil {
		return &ConnectionTestError{Message: "Invalid request-router configuration", Cause: err}
	}
	response, err := client.TestConnection(ctx, &pluginv1.TestConnectionRequest{
		CapabilityId: capabilityID,
		Connection:   &pluginv1.RouterConnection{Id: "admin-test", BaseUrl: baseURL, ApiKey: apiKey, Config: config},
	})
	if err != nil {
		return &ConnectionTestError{Message: "Request-router connection check failed", Cause: err}
	}
	if !response.GetOk() {
		return &ConnectionTestError{Message: response.GetMessage()}
	}
	return nil
}

func requestRouterConnectionCheckCapabilityID(manifest *pluginv1.PluginManifest) (string, error) {
	for _, capability := range manifest.GetCapabilities() {
		if capability != nil && capability.GetType() == "request_router.v1" {
			if strings.TrimSpace(capability.GetId()) == "" {
				return "", fmt.Errorf("request-router capability id is required")
			}
			return capability.GetId(), nil
		}
	}
	return "", ErrConnectionTestUnsupported
}
