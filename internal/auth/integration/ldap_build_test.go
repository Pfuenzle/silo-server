//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func (h *packagedLDAPHarness) usePrebuiltBuild(t *testing.T, cache string) {
	t.Helper()
	if got, want := sourceTreeHash(t, h.root), os.Getenv("SILO_LDAP_BUILD_SOURCE_HASH"); want == "" || got != want {
		t.Fatalf("LDAP build cache source hash mismatch: got %q want %q", got, want)
	}
	receipt, err := os.ReadFile(os.Getenv("SILO_LDAP_BUILD_RECEIPT"))
	if err != nil || string(receipt) != "source_hash="+os.Getenv("SILO_LDAP_BUILD_SOURCE_HASH")+" production_builds=1\n" {
		t.Fatalf("LDAP build receipt is invalid: %v", err)
	}
	for _, path := range []string{"silo", filepath.Join("web", "dist")} {
		from, to := filepath.Join(cache, path), filepath.Join(h.buildRoot, path)
		info, statErr := os.Stat(from)
		if statErr != nil || (path == "silo" && info.IsDir()) || (path != "silo" && !info.IsDir()) {
			t.Fatalf("LDAP build cache artifact unavailable at %q: %v", from, statErr)
		}
		runCommand(t, context.Background(), h.root, "cp", "-a", from, to)
	}
}
