package plugins

import (
	"context"
	"strings"
	"testing"
)

func TestSetGlobalConfigWithFieldClearsSwitchesMutuallyExclusiveCAFields(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.auth.ldap", "v1.0.0")
	manifest.GlobalConfigSchema[0].JsonSchema = `{
		"type":"object",
		"properties":{
			"ca_pem":{"type":"string","minLength":1},
			"ca_file":{"type":"string","minLength":1},
			"bind_password":{"type":"string","format":"password"}
		},
		"required":["bind_password"],
		"oneOf":[
			{"required":["ca_pem"],"not":{"required":["ca_file"]}},
			{"required":["ca_file"],"not":{"required":["ca_pem"]}}
		],
		"additionalProperties":false
	}`
	installPath := writeInstalledPluginManifest(t, manifest)
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
		7: {{
			InstallationID: 7,
			Key:            "connection",
			Value: map[string]any{
				"ca_file":       "/etc/silo/ldap-ca/old.pem",
				"bind_password": "saved-secret",
			},
		}},
	}}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		configs: store,
	}

	err := service.SetGlobalConfigWithFieldClears(
		context.Background(),
		7,
		"connection",
		map[string]any{"ca_pem": "certificate"},
		nil,
		[]string{"ca_file"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 1 {
		t.Fatalf("put calls = %d, want 1", len(store.puts))
	}
	if _, present := store.puts[0].value["ca_file"]; present {
		t.Fatalf("cleared ca_file remained: %#v", store.puts[0].value)
	}
	if store.puts[0].value["ca_pem"] != "certificate" ||
		store.puts[0].value["bind_password"] != "saved-secret" {
		t.Fatalf("persisted config = %#v", store.puts[0].value)
	}
}

func TestValidatedPublicClearSetRejectsSecretAndUnknownFields(t *testing.T) {
	for _, fields := range [][]string{{"bind_password"}, {"not_declared"}} {
		_, err := validatedPublicClearSet("ldap", []string{"ca_file", "ca_pem"}, fields)
		if err == nil {
			t.Fatalf("validatedPublicClearSet(%q) returned nil error", fields[0])
		}
		if !strings.Contains(err.Error(), "is not a public field") {
			t.Fatalf("error = %q, want public-field rejection", err)
		}
	}
}
