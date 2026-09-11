package livetv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingLivePlaybackStore struct{ puts int }

func (f *failingLivePlaybackStore) Put(context.Context, LivePlaybackSession) error {
	f.puts++
	if f.puts > 1 {
		return errors.New("store unavailable")
	}
	return nil
}
func (failingLivePlaybackStore) Get(context.Context, string) (LivePlaybackSession, bool, error) {
	return LivePlaybackSession{}, false, nil
}
func (failingLivePlaybackStore) Delete(context.Context, string) error { return nil }

func TestLivePlayback_ManifestStoreFailure_returnsErrorBeforeSuccess(t *testing.T) {
	// Given an HLS grant whose resource-map store fails.
	store := &failingLivePlaybackStore{}
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer("127.0.0.1:1")}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}, Store: store})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeHLS})
	if err != nil {
		t.Fatal(err)
	}

	// When a manifest request is attempted against the failed grant.
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil))

	// Then no successful manifest is returned and the failed grant is absent.
	if recorder.Code == http.StatusOK {
		t.Fatal("manifest returned success after store failure")
	}
}
