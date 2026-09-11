package livetv

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntime_refreshesConfiguredSourceOnlyWhenInvoked(t *testing.T) {
	// Given a runtime constructed with the production runtime constructor.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"fixture-1\",Fixture News\nhttp://stream.invalid/news\n")
	}))
	t.Cleanup(server.Close)
	store := &runtimeTestStore{}
	runtime := NewRuntime(store, time.Unix(1700000000, 0).UTC(), FetchConfig{
		Policy:   NetworkPolicy{MaxBodyBytes: 256},
		Resolver: runtimeTestResolver{},
		Dialer: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})

	if requests != 0 {
		t.Fatalf("construction requests = %d, want 0", requests)
	}

	// When the owning refresh path is explicitly invoked.
	_, err := runtime.RefreshSource(context.Background(), Source{
		ID:           42,
		Kind:         SourceKindPlaylist,
		SourceKey:    "fixture-source",
		Location:     "http://source.example:8080/playlist",
		RefreshState: "pending",
	}, nil)

	// Then configured-source ingestion executes and applies a ready snapshot.
	if err != nil {
		t.Fatalf("RefreshSource: %v", err)
	}
	if requests != 1 || store.state != "ready" || len(store.snapshot.Channels) != 1 {
		t.Fatalf("requests = %d, state = %q, channels = %d, want 1, ready, 1", requests, store.state, len(store.snapshot.Channels))
	}
}

type runtimeTestResolver struct{}

func (runtimeTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("198.51.100.2")}}, nil
}

type runtimeTestStore struct {
	snapshot SourceSnapshot
	state    string
}

func (s *runtimeTestStore) ApplySnapshot(_ context.Context, _ int64, snapshot SourceSnapshot, state, _ string, _ *time.Time) error {
	s.snapshot = snapshot
	s.state = state
	return nil
}
