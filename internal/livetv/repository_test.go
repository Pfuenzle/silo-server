package livetv

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresRepository_roundTripsChannelsProgrammesAndMappings(t *testing.T) {
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, sourceID, epgSourceID := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel := Channel{
		LibraryID: libraryID, SourceID: sourceID, ExternalID: "news-1",
		StableID: "playlist-a|news-1", Name: "News", Number: "1",
		StreamURL: "http://example.invalid/live/news-1", Category: "News", Artwork: []byte(`{"logo":"key"}`), Rating: []byte(`{"value":4}`),
	}
	createdChannel, err := repo.CreateChannel(ctx, channel)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	programme := Programme{
		LibraryID: libraryID, SourceID: sourceID, ChannelID: createdChannel.ID, ExternalID: "programme-1",
		StableID: "playlist-a|programme-1", Title: "Morning News", StartsAt: time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		Description: "Daily news", Artwork: []byte(`{"image":"key"}`), Rating: []byte(`{"value":5}`),
	}
	createdProgramme, err := repo.CreateProgramme(ctx, programme)
	if err != nil {
		t.Fatalf("create programme: %v", err)
	}
	if err := repo.UpsertChannelEPGMapping(ctx, ChannelEPGMapping{LibraryID: libraryID, ChannelID: createdChannel.ID, EPGSourceID: epgSourceID, EPGChannelID: "xml-news-1"}); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	gotChannel, err := repo.GetChannel(ctx, libraryID, channel.StableID)
	if err != nil || gotChannel.Name != channel.Name || gotChannel.Category != channel.Category || !sameJSON(gotChannel.Artwork, channel.Artwork) {
		t.Fatalf("get channel = %#v, err %v", gotChannel, err)
	}
	gotProgrammes, err := repo.ListProgrammes(ctx, libraryID, createdChannel.ID, programme.StartsAt, programme.EndsAt)
	if err != nil || len(gotProgrammes) != 1 || gotProgrammes[0].ID != createdProgramme.ID {
		t.Fatalf("list programmes = %#v, err %v", gotProgrammes, err)
	}
	mappings, err := repo.ListChannelEPGMappings(ctx, libraryID, createdChannel.ID)
	if err != nil || len(mappings) != 1 || mappings[0].EPGChannelID != "xml-news-1" {
		t.Fatalf("list mappings = %#v, err %v", mappings, err)
	}
}

func TestPostgresRepository_CreateSourceDefaultsOmittedConfig(t *testing.T) {
	// Given a valid source request with no optional config payload.
	pool := liveTVTestPool(t)
	ctx := context.Background()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('livetv', 'Source config fixture') RETURNING id`).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID) })

	// When the repository persists the source with a nil config.
	source, err := NewPostgresRepository(pool).CreateSource(ctx, Source{
		LibraryID: libraryID,
		Kind:      SourceKindPlaylist,
		SourceKey: "playlist-a",
		Name:      "Playlist A",
		Location:  "http://example.invalid/playlist",
		Enabled:   true,
	})

	// Then PostgreSQL receives the schema's empty JSON object default.
	if err != nil {
		t.Fatal(err)
	}
	if string(source.Config) != `{}` {
		t.Fatalf("source config = %s, want {}", source.Config)
	}
}

func TestPostgresRepository_ApplyEPGSnapshotAttachesExistingChannels(t *testing.T) {
	// Given a playlist channel and an EPG source in the same library.
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, playlistSourceID, epgSourceID := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel, err := repo.CreateChannel(ctx, Channel{
		LibraryID: libraryID, SourceID: playlistSourceID, ExternalID: "news-1",
		StableID: "playlist-a|news-1", Name: "News", StreamURL: "http://example.invalid/news",
	})
	if err != nil {
		t.Fatal(err)
	}

	// When the EPG snapshot contains a programme but no channel rows.
	programme := ParsedProgramme{
		ChannelExternalID: "news-1",
		Programme: Programme{
			SourceID: epgSourceID, ExternalID: "programme-1", StableID: "epg-a|programme-1",
			Title: "Morning News", StartsAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC),
		},
	}
	if err := repo.ApplySnapshot(ctx, epgSourceID, SourceSnapshot{Programmes: []ParsedProgramme{programme}}, "ready", "", nil); err != nil {
		t.Fatal(err)
	}

	// Then the programme points at the existing playlist channel.
	programmes, err := repo.ListProgrammes(ctx, libraryID, channel.ID, programme.StartsAt, programme.EndsAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(programmes) != 1 || programmes[0].ChannelID != channel.ID {
		t.Fatalf("EPG programmes = %#v, want one programme for channel %d", programmes, channel.ID)
	}
}

func TestPostgresRepository_JSONBArtworkComparisonIgnoresDatabaseFormatting(t *testing.T) {
	// Given equivalent JSON with compact and PostgreSQL-style whitespace.
	compact := []byte(`{"logo":"key"}`)
	canonical := []byte(`{"logo": "key"}`)

	// When JSONB values are compared at the repository boundary.
	// Then formatting differences do not change the JSON value.
	if !sameJSON(compact, canonical) {
		t.Fatalf("JSON values were considered different: %s and %s", compact, canonical)
	}
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func TestPostgresRepository_CreateChannelPersistsCategoryAndDefaultsAbsentArtwork(t *testing.T) {
	// Given a configured Live TV PostgreSQL test database and a categorized channel without artwork.
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, sourceID, _ := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel := Channel{LibraryID: libraryID, SourceID: sourceID, ExternalID: "category-regression", StableID: "playlist-a|category-regression", Name: "Category Regression", Category: "News", StreamURL: "http://example.invalid/live/category-regression"}

	// When the direct repository insert is executed.
	created, err := repo.CreateChannel(ctx, channel)

	// Then the returned row contains the category and a non-null empty artwork object.
	if err != nil {
		t.Fatal(err)
	}
	if created.Category != channel.Category || string(created.Artwork) != `{}` {
		t.Fatalf("created category/artwork = %q/%s, want %q/{}", created.Category, created.Artwork, channel.Category)
	}
}

func TestPostgresRepository_rejectsDuplicateStableIdentities(t *testing.T) {
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, sourceID, _ := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel := Channel{LibraryID: libraryID, SourceID: sourceID, ExternalID: "duplicate", StableID: "source|duplicate", Name: "Duplicate", Artwork: []byte(`{}`), Rating: []byte(`{}`)}
	if _, err := repo.CreateChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateChannel(ctx, channel); err == nil {
		t.Fatal("duplicate stable identity was accepted")
	}
}

func TestPostgresRepository_rejectsCrossLibraryMapping(t *testing.T) {
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, sourceID, epgSourceID := seedLiveTVFixtures(t, pool)
	otherLibraryID, _, _ := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel, err := repo.CreateChannel(ctx, Channel{LibraryID: libraryID, SourceID: sourceID, ExternalID: "owned", StableID: "source|owned", Name: "Owned", Artwork: []byte(`{}`), Rating: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertChannelEPGMapping(ctx, ChannelEPGMapping{LibraryID: otherLibraryID, ChannelID: channel.ID, EPGSourceID: epgSourceID, EPGChannelID: "wrong-library"}); err == nil {
		t.Fatal("cross-library mapping was accepted")
	}
}

func TestPostgresRepository_cascadesOwnedRowsWhenLibraryDeleted(t *testing.T) {
	pool := liveTVTestPool(t)
	ctx := context.Background()
	libraryID, sourceID, epgSourceID := seedLiveTVFixtures(t, pool)
	repo := NewPostgresRepository(pool)
	channel, err := repo.CreateChannel(ctx, Channel{LibraryID: libraryID, SourceID: sourceID, ExternalID: "cascade", StableID: "source|cascade", Name: "Cascade", Artwork: []byte(`{}`), Rating: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	programme, err := repo.CreateProgramme(ctx, Programme{LibraryID: libraryID, SourceID: sourceID, ChannelID: channel.ID, ExternalID: "cascade-programme", StableID: "source|cascade-programme", Title: "Cascade", Artwork: []byte(`{}`), Rating: []byte(`{}`), StartsAt: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertChannelEPGMapping(ctx, ChannelEPGMapping{LibraryID: libraryID, ChannelID: channel.ID, EPGSourceID: epgSourceID, EPGChannelID: "cascade-epg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, libraryID); err != nil {
		t.Fatalf("delete library: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM live_tv_channels WHERE id = $1`, channel.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("channel %d survived library cascade", channel.ID)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM live_tv_programmes WHERE id = $1`, programme.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("programme %d survived library cascade", programme.ID)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM live_tv_channel_epg_mappings WHERE channel_id = $1`, channel.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("mapping for channel %d survived library cascade", channel.ID)
	}
}

func liveTVTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var tableName *string
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.live_tv_channel_epg_mappings')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check Live TV migration: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the Live TV persistence migration")
	}
	var hasLibraryID bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'live_tv_channel_epg_mappings' AND column_name = 'library_id')`).Scan(&hasLibraryID); err != nil {
		t.Fatalf("check Live TV mapping ownership column: %v", err)
	}
	if !hasLibraryID {
		t.Skip("test database has not applied the Live TV mapping ownership migration")
	}
	return pool
}

func seedLiveTVFixtures(t *testing.T, pool *pgxpool.Pool) (int, int64, int64) {
	t.Helper()
	ctx := context.Background()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('livetv', 'Live TV fixture') RETURNING id`).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	var sourceID, epgSourceID int64
	if err := pool.QueryRow(ctx, `INSERT INTO live_tv_sources (library_id, kind, source_key, name, location) VALUES ($1, 'playlist', 'playlist-a', 'Playlist', 'http://example.invalid/playlist') RETURNING id`, libraryID).Scan(&sourceID); err != nil {
		t.Fatalf("seed playlist source: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO live_tv_sources (library_id, kind, source_key, name, location) VALUES ($1, 'epg', 'epg-a', 'EPG', 'http://example.invalid/guide') RETURNING id`, libraryID).Scan(&epgSourceID); err != nil {
		t.Fatalf("seed EPG source: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID) })
	return libraryID, sourceID, epgSourceID
}
