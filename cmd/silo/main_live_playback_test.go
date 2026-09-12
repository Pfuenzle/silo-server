package main

import "testing"

func TestIntegratedLivePlaybackOrigin_isNativeAPIPath(t *testing.T) {
	// Given the two API-capable server modes.

	// When each mode selects its local Live TV origin.
	integrated := integratedLivePlaybackOrigin("integrated")
	api := integratedLivePlaybackOrigin("api")

	// Then only the integrated server advertises a locally served origin.
	if integrated != "/api/v1" {
		t.Fatalf("integrated origin = %q, want /api/v1", integrated)
	}
	if api != "" {
		t.Fatalf("api origin = %q, want empty", api)
	}
}

func TestNewLivePlaybackStore_configuresIntegrityKeyMaterial(t *testing.T) {
	// Given each production role's shared Redis grant-store helper.
	apiStore := newLivePlaybackStore(nil, "api-secret")
	proxyStore := newLivePlaybackStore(nil, "proxy-secret")

	// When the constructors are inspected without requiring Redis.

	// Then both stores have non-empty integrity key material.
	if !apiStore.IntegrityConfigured() || !proxyStore.IntegrityConfigured() {
		t.Fatalf("api configured = %v, proxy configured = %v", apiStore.IntegrityConfigured(), proxyStore.IntegrityConfigured())
	}
}
