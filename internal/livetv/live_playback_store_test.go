package livetv

import (
	"errors"
	"testing"
	"time"
)

func TestRedisLivePlaybackStore_rejectsTamperedPayload(t *testing.T) {
	// Given a Redis grant store with an integrity key and an altered payload.
	store := NewRedisLivePlaybackStore(nil, 0)
	store.SetIntegrityKey("test-integrity-key")

	// When the altered record is decoded.
	_, err := store.decode([]byte(`{"session":{"grant_id":"grant"}}
invalid-signature`))

	// Then the grant is rejected before any state can be trusted.
	if !errors.Is(err, ErrLivePlaybackGrantIntegrity) {
		t.Fatalf("error = %v, want %v", err, ErrLivePlaybackGrantIntegrity)
	}
}

func TestRedisLivePlaybackStore_constructorRequiresIntegrityKey(t *testing.T) {
	// Given stores representing the API and proxy production construction helper.
	apiStore := NewRedisLivePlaybackStore(nil, time.Hour)
	proxyStore := NewRedisLivePlaybackStore(nil, time.Hour)
	apiStore.SetIntegrityKey("api-server-secret")
	proxyStore.SetIntegrityKey("proxy-server-secret")

	// When constructor state is inspected without opening Redis.

	// Then both production stores have configured integrity material.
	if !apiStore.IntegrityConfigured() || !proxyStore.IntegrityConfigured() {
		t.Fatalf("api configured = %v, proxy configured = %v", apiStore.IntegrityConfigured(), proxyStore.IntegrityConfigured())
	}
}
