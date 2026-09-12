package livetv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type SnapshotStore interface {
	ApplySnapshot(context.Context, int64, SourceSnapshot, string, string, *time.Time) error
}

type Reconciler struct {
	store    SnapshotStore
	now      func() time.Time
	ingestor SourceIngestor
	artwork  ArtworkCacher
}

var ErrEmptySnapshot = fmt.Errorf("Live TV source returned an empty snapshot")

func NewReconciler(store SnapshotStore, now time.Time) *Reconciler {
	return NewReconcilerWithClock(store, func() time.Time { return now })
}

func NewReconcilerWithIngestor(store SnapshotStore, now time.Time, ingestor SourceIngestor) *Reconciler {
	return NewReconcilerWithIngestorAndClock(store, func() time.Time { return now }, ingestor)
}

func NewReconcilerWithClock(store SnapshotStore, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{store: store, now: now}
}

func NewReconcilerWithIngestorAndClock(store SnapshotStore, now func() time.Time, ingestor SourceIngestor) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{store: store, now: now, ingestor: ingestor}
}

func (r *Reconciler) SetArtworkCacher(cacher ArtworkCacher) {
	if r != nil {
		r.artwork = cacher
	}
}

func (r *Reconciler) RefreshSource(ctx context.Context, source Source, mappings map[string]ChannelMapping) (Diagnostics, error) {
	if r == nil || r.ingestor == nil {
		return nil, fmt.Errorf("Live TV source ingestor is not configured")
	}
	snapshot, diagnostics, err := r.ingestor.Ingest(ctx, source, mappings)
	if reconcileErr := r.Reconcile(ctx, source, snapshot, err); reconcileErr != nil {
		return diagnostics, reconcileErr
	}
	return diagnostics, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, source Source, snapshot SourceSnapshot, refreshErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if refreshErr != nil {
		refreshedAt := r.now()
		if err := r.store.ApplySnapshot(ctx, source.ID, SourceSnapshot{}, "stale", refreshErr.Error(), &refreshedAt); err != nil {
			return fmt.Errorf("mark Live TV source stale: %w", err)
		}
		return nil
	}
	if len(snapshot.Channels) == 0 && len(snapshot.Programmes) == 0 {
		refreshedAt := r.now()
		if err := r.store.ApplySnapshot(ctx, source.ID, SourceSnapshot{}, "stale", ErrEmptySnapshot.Error(), &refreshedAt); err != nil {
			return fmt.Errorf("mark empty Live TV source stale: %w", err)
		}
		return nil
	}
	if r.artwork != nil {
		r.cacheArtwork(ctx, source, &snapshot)
	}
	refreshedAt := r.now()
	if err := r.store.ApplySnapshot(ctx, source.ID, snapshot, "ready", "", &refreshedAt); err != nil {
		return fmt.Errorf("apply Live TV snapshot: %w", err)
	}
	return nil
}

func (r *Reconciler) cacheArtwork(ctx context.Context, source Source, snapshot *SourceSnapshot) {
	for index := range snapshot.Channels {
		channel := &snapshot.Channels[index]
		channel.Artwork = r.cacheArtworkValue(ctx, source, channel.Artwork, "channels", fmt.Sprintf("%d|%s", source.LibraryID, channel.StableID))
	}
	for index := range snapshot.Programmes {
		programme := &snapshot.Programmes[index].Programme
		programme.Artwork = r.cacheArtworkValue(ctx, source, programme.Artwork, "programmes", fmt.Sprintf("%d|%s", source.LibraryID, programme.StableID))
	}
}

func (r *Reconciler) cacheArtworkValue(ctx context.Context, source Source, raw []byte, kind, identity string) []byte {
	var value map[string]string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	for key, url := range value {
		cached, err := r.artwork.CacheLiveTVArtwork(ctx, url, kind, source.SourceKey+"|"+identity+"|"+key)
		if err != nil || cached == "" {
			return nil
		}
		value[key] = cached
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}
