package livetv

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReconciler_refreshSourceFetchesParsesAndAppliesSnapshot(t *testing.T) {
	// Given a configured playlist source and a server-side fetch ingestor.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"news-1\",News\nhttp://stream.invalid/news\n")
	}))
	t.Cleanup(server.Close)
	port := strings.TrimPrefix(server.URL, "http://")
	_, port, _ = net.SplitHostPort(port)
	fetcher := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})
	store := newMemorySnapshotStore()
	reconciler := NewReconcilerWithIngestor(store, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), fetcher)

	// When the source refresh is requested through the reconciliation seam.
	diagnostics, err := reconciler.RefreshSource(context.Background(), Source{
		ID: 7, Kind: SourceKindPlaylist, SourceKey: "playlist-a", Location: "http://10.100.0.2:" + port + "/playlist",
	}, nil)

	// Then the fetched and parsed snapshot is atomically marked ready.
	if err != nil || len(diagnostics) != 0 || store.state(7) != "ready" || len(store.snapshot(7).Channels) != 1 {
		t.Fatalf("diagnostics = %#v, error = %v, state = %q, snapshot = %#v", diagnostics, err, store.state(7), store.snapshot(7))
	}
}

func TestReconciler_replacesSnapshotAtomically(t *testing.T) {
	// Given a store containing the prior source snapshot.
	store := newMemorySnapshotStore()
	first := SourceSnapshot{Channels: []Channel{{ExternalID: "old", StableID: "playlist-a|old"}}}
	if err := store.ApplySnapshot(context.Background(), 7, first, "ready", "", nil); err != nil {
		t.Fatal(err)
	}

	// When a new complete snapshot is reconciled.
	reconciler := NewReconciler(store, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	second := SourceSnapshot{Channels: []Channel{{ExternalID: "new", StableID: "playlist-a|new"}}}
	if err := reconciler.Reconcile(context.Background(), Source{ID: 7, LibraryID: 3, SourceKey: "playlist-a"}, second, nil); err != nil {
		t.Fatal(err)
	}

	// Then old rows are absent and the source is marked ready in one commit.
	got := store.snapshot(7)
	if len(got.Channels) != 1 || got.Channels[0].ExternalID != "new" || store.state(7) != "ready" {
		t.Fatalf("snapshot = %#v, state = %q", got, store.state(7))
	}
}

func TestReconciler_preservesLastKnownGoodOnFailure(t *testing.T) {
	// Given a valid snapshot already committed.
	store := newMemorySnapshotStore()
	old := SourceSnapshot{Channels: []Channel{{ExternalID: "old", StableID: "playlist-a|old"}}}
	_ = store.ApplySnapshot(context.Background(), 7, old, "ready", "", nil)
	reconciler := NewReconciler(store, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	// When the source refresh fails.
	err := errors.New("malformed XMLTV")
	if reconcileErr := reconciler.Reconcile(context.Background(), Source{ID: 7, LibraryID: 3, SourceKey: "playlist-a"}, SourceSnapshot{}, err); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// Then the old rows remain and only refresh status changes to stale/error.
	got := store.snapshot(7)
	if len(got.Channels) != 1 || got.Channels[0].ExternalID != "old" || store.state(7) != "stale" || store.err(7) != err.Error() {
		t.Fatalf("snapshot = %#v, state = %q, error = %q", got, store.state(7), store.err(7))
	}
}

type memorySnapshotStore struct {
	snapshots map[int64]SourceSnapshot
	states    map[int64]string
	errors    map[int64]string
}

func newMemorySnapshotStore() *memorySnapshotStore {
	return &memorySnapshotStore{snapshots: map[int64]SourceSnapshot{}, states: map[int64]string{}, errors: map[int64]string{}}
}

func (s *memorySnapshotStore) ApplySnapshot(_ context.Context, sourceID int64, snapshot SourceSnapshot, state, message string, _ *time.Time) error {
	if state == "ready" {
		s.snapshots[sourceID] = snapshot
	}
	s.states[sourceID], s.errors[sourceID] = state, message
	return nil
}

func (s *memorySnapshotStore) snapshot(sourceID int64) SourceSnapshot { return s.snapshots[sourceID] }
func (s *memorySnapshotStore) state(sourceID int64) string            { return s.states[sourceID] }
func (s *memorySnapshotStore) err(sourceID int64) string              { return s.errors[sourceID] }
