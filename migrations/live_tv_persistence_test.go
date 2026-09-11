package migrations

import (
	"strings"
	"testing"
)

func TestLiveTVPersistenceMigrationDefinesNormalizedOwnership(t *testing.T) {
	// Given the Todo 1 migration source.
	data, err := FS.ReadFile("sql/20260910100000_live_tv_persistence.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	up := strings.Join(strings.Fields(strings.SplitN(string(data), "-- +goose Down", 2)[0]), " ")

	// When the migration contract is inspected.
	required := []string{
		"CREATE TABLE public.live_tv_sources",
		"REFERENCES public.media_folders(id) ON DELETE CASCADE",
		"UNIQUE (library_id, source_key)",
		"CREATE TABLE public.live_tv_channels",
		"UNIQUE (source_id, external_id)",
		"UNIQUE (library_id, stable_id)",
		"category text NOT NULL DEFAULT ''",
		"FOREIGN KEY (source_id, library_id) REFERENCES public.live_tv_sources(id, library_id)",
		"CREATE TABLE public.live_tv_programmes",
		"FOREIGN KEY (channel_id, library_id) REFERENCES public.live_tv_channels(id, library_id) ON DELETE CASCADE",
		"FOREIGN KEY (epg_source_id, library_id) REFERENCES public.live_tv_sources(id, library_id) ON DELETE CASCADE",
		"CHECK (ends_at > starts_at)",
		"CREATE TABLE public.live_tv_channel_epg_mappings",
		"library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE",
		"PRIMARY KEY (channel_id, epg_source_id)",
		"UNIQUE (epg_source_id, epg_channel_id)",
		"CREATE TABLE public.live_tv_favorites",
		"PRIMARY KEY (user_id, profile_id, library_id, entity_kind, stable_id)",
		"FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE",
		"entity_kind text NOT NULL CHECK (entity_kind IN ('channel', 'programme'))",
	}
	for _, fragment := range required {
		if !strings.Contains(up, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
	if strings.Contains(up, "user_favorites") || strings.Contains(up, "media_item_id") {
		t.Fatal("Live TV favorites must not reuse catalog media-item favorites")
	}

	// Then no filesystem or VOD persistence table is part of the migration.
	for _, forbidden := range []string{"media_folder_paths", "media_files", "media_items"} {
		if strings.Contains(up, "INSERT INTO public."+forbidden) {
			t.Errorf("migration creates fake %s rows", forbidden)
		}
	}
}

func TestLiveTVPersistenceMigrationDropsFavoritesBeforeLiveTVEntities(t *testing.T) {
	data, err := FS.ReadFile("sql/20260910100000_live_tv_persistence.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	down := strings.SplitN(string(data), "-- +goose Down", 2)[1]
	if strings.Index(down, "DROP TABLE IF EXISTS public.live_tv_favorites") > strings.Index(down, "DROP TABLE IF EXISTS public.live_tv_sources") {
		t.Fatal("favorites table must be dropped before Live TV parent tables")
	}
}
