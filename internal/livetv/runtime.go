package livetv

import (
	"context"
	"time"
)

type Runtime struct {
	reconciler *Reconciler
}

func NewRuntime(store SnapshotStore, now time.Time, fetchConfig FetchConfig) *Runtime {
	return &Runtime{
		reconciler: NewReconcilerWithIngestor(store, now, NewFetchService(fetchConfig)),
	}
}

func NewRuntimeWithClock(store SnapshotStore, now func() time.Time, fetchConfig FetchConfig) *Runtime {
	return &Runtime{
		reconciler: NewReconcilerWithIngestorAndClock(store, now, NewFetchService(fetchConfig)),
	}
}

func (r *Runtime) RefreshSource(ctx context.Context, source Source, mappings map[string]ChannelMapping) (Diagnostics, error) {
	if r == nil || r.reconciler == nil {
		return nil, ErrRuntimeNotConfigured
	}
	return r.reconciler.RefreshSource(ctx, source, mappings)
}
