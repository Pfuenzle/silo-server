package catalog

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestParseCatalogSectionQueryDefinitionNormalizesGeneratedEpisodeCap(t *testing.T) {
	config := []byte(`{
		"media_scope":"episode",
		"match":"any",
		"groups":[{"match":"any","rules":[{"field":"release_date","op":"in_next","value":"4d"}]}],
		"sort":{"field":"release_date","order":"desc"},
		"generated_kind":"recently_released_episodes",
		"generated_source":"home_library_recent"
	}`)

	def, err := parseCatalogSectionQueryDefinition(config)
	if err != nil {
		t.Fatalf("parseCatalogSectionQueryDefinition() error = %v", err)
	}
	if def.Match != "all" || len(def.Groups) != 1 || def.Groups[0].Match != "all" || len(def.Groups[0].Rules) != 1 {
		t.Fatalf("generated episode filter = %+v, want one ANDed cap group", def)
	}
	rule := def.Groups[0].Rules[0]
	if rule.Field != "release_date" || rule.Op != "in_next" || rule.Value != "1d" {
		t.Fatalf("generated episode cap = %+v, want release_date in_next 1d", rule)
	}
}

func TestParseCatalogSectionQueryDefinitionLeavesCustomEpisodeFiltersAlone(t *testing.T) {
	config := []byte(`{"media_scope":"episode","match":"all","groups":[],"sort":{"field":"release_date","order":"desc"}}`)
	def, err := parseCatalogSectionQueryDefinition(config)
	if err != nil {
		t.Fatalf("parseCatalogSectionQueryDefinition() error = %v", err)
	}
	if len(def.Groups) != 0 {
		t.Fatalf("ordinary custom episode filter changed: %+v", def.Groups)
	}
}

func TestGeneratedRecentEpisodesCutoffDatabase(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	suffix := fmt.Sprint(time.Now().UnixNano())
	seriesID := "recent-episode-series-" + suffix
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', $1)`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seriesID, libraryID); err != nil {
		t.Fatalf("seed series library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})

	var ids []string
	for offset, label := range []string{"yesterday", "today", "tomorrow", "day-after-tomorrow"} {
		id := seriesID + "-" + label
		ids = append(ids, id)
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, air_date)
			VALUES ($1, $2, 1, $3, $4, CURRENT_DATE + $5::integer)`, id, seriesID, offset+1, label, offset-1); err != nil {
			t.Fatalf("seed %s episode: %v", label, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, id, libraryID); err != nil {
			t.Fatalf("seed %s episode library: %v", label, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path) VALUES ($1, $2, $3, $4)`, seriesID, id, libraryID, "/recent-cutoff/"+id+".mkv"); err != nil {
			t.Fatalf("seed %s episode file: %v", label, err)
		}
	}

	config := []byte(`{
		"library_ids":[` + fmt.Sprint(libraryID) + `],
		"media_scope":"episode",
		"match":"any",
		"groups":[{"match":"any","rules":[
			{"field":"status","op":"is","value":"matched"},
			{"field":"release_date","op":"in_next","value":"4d"}
		]}],
		"sort":{"field":"release_date","order":"desc"},
		"generated_kind":"recently_released_episodes",
		"generated_source":"home_library_recent"
	}`)
	def, err := parseCatalogSectionQueryDefinition(config)
	if err != nil {
		t.Fatalf("parse generated episode section: %v", err)
	}
	executor := QueryExecutor{Pool: pool, Scope: "episode"}
	access := AccessFilter{AllowedLibraryIDs: []int{libraryID}}
	want := []string{ids[0], ids[1], ids[2]}
	for _, path := range []string{"offset", "cursor"} {
		t.Run(path, func(t *testing.T) {
			var got []string
			if path == "offset" {
				items, total, _, err := executor.PreviewPage(ctx, def, access, 10, 0, true)
				if err != nil {
					t.Fatalf("PreviewPage() error = %v", err)
				}
				if total != len(want) {
					t.Fatalf("PreviewPage() total = %d, want %d", total, len(want))
				}
				for _, item := range items {
					got = append(got, item.ContentID)
				}
			} else {
				page, err := executor.PreviewCursorPage(ctx, def, access, 10, nil, true)
				if err != nil {
					t.Fatalf("PreviewCursorPage() error = %v", err)
				}
				if page.Total != len(want) {
					t.Fatalf("PreviewCursorPage() total = %d, want %d", page.Total, len(want))
				}
				for _, item := range page.Items {
					got = append(got, item.ContentID)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("%s episode IDs = %v, want %v", path, got, want)
			}
		})
	}
}
