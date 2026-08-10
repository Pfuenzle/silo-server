//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentedAuthSetup_RuntimeAndAccountBoundariesMatchContract(t *testing.T) {
	// Given
	document, err := os.ReadFile(filepath.Join(repositoryRoot(t), "docs", "external-auth.md"))
	if err != nil {
		t.Fatalf("read external auth guide: %v", err)
	}

	// When
	values := documentedKeyValues(t, string(document), "runtime-boundaries")

	// Then
	want := map[string]string{
		"global_runtime_config":                 "lazy_plugin_reload",
		"binding_enable_or_mode":                "full_silo_restart",
		"external_local_password_login_enabled": "false",
		"break_glass_provider":                  "local",
		"oidc_authorization_mode":               "none",
	}
	for key, expected := range want {
		if got := values[key]; got != expected {
			t.Errorf("documented %s = %q, want %q", key, got, expected)
		}
	}
}

func documentedKeyValues(t *testing.T, document, name string) map[string]string {
	t.Helper()
	start := "<!-- auth-doc:" + name + ":start -->"
	end := "<!-- auth-doc:" + name + ":end -->"
	startIndex := strings.Index(document, start)
	if startIndex < 0 {
		t.Fatalf("documentation marker %q is missing", start)
	}
	after := document[startIndex+len(start):]
	block, _, found := strings.Cut(after, end)
	if !found {
		t.Fatalf("documentation marker %q is missing", end)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(block, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			values[key] = value
		}
	}
	return values
}
