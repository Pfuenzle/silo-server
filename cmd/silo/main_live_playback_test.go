package main

import "testing"

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
