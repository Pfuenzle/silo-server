package watchsync

import (
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GetMediaTitles reads a movie's own title and year, and an episode's title
// with its series' title and year (an episode's year is its series').
func TestGetMediaTitlesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := uuid.NewString()
	movie, series, episode := "titles-movie-"+suffix, "titles-series-"+suffix, "titles-episode-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,year,genres) VALUES ($1,'movie','17 Again',2009,'{}'),($2,'series','Breaking Bad',2008,'{}')`, movie, series); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id IN ($1,$2)`, movie, series) }()
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,air_date) VALUES ($1,$2,1,1,'Pilot','2010-01-20')`, episode, series); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE content_id=$1`, episode) }()

	titles, err := NewPostgresRepository(pool, nil).GetMediaTitles(ctx, []string{movie, episode, "titles-missing-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]MediaTitles{
		movie:   {Kind: "movie", Title: "17 Again", Year: 2009},
		episode: {Kind: "episode", Title: "Pilot", Year: 2008, SeriesTitle: "Breaking Bad", SeriesYear: 2008, AbsoluteEpisodeNumber: 1, SeriesEpisodeCount: 1},
	}
	if len(titles) != len(want) || titles[movie] != want[movie] || titles[episode] != want[episode] {
		t.Fatalf("titles = %+v, want %+v", titles, want)
	}
}

// Numbering resets at each season, while AniList progress is series-wide.
// Missing catalog rows must never be silently compressed into a lower rank.
func TestGetMediaTitlesEpisodeOrderingDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, tc := range []struct {
		name     string
		seasons  [][]int
		target   string
		absolute int
		total    int
	}{
		{"season reset", [][]int{{1, 2, 3}, {1, 2}}, "2-1", 4, 5},
		{"missing episode", [][]int{{1, 3}, {1, 2}}, "2-1", 0, 0},
		{"missing first episode", [][]int{{2, 3}, {1, 2}}, "2-1", 0, 0},
		{"missing season", [][]int{{1, 2}, nil, {1}}, "3-1", 0, 0},
		{"only later season", [][]int{nil, {1, 2}}, "2-1", 0, 0},
		{"later gap preserves known prefix", [][]int{{1, 2}, {1, 3}}, "1-2", 2, 0},
		{"special excluded", [][]int{{1, 2}, {1}}, "0-1", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			series := "episode-ordering-" + uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,genres) VALUES ($1,'series','Anime','{}')`, series); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, series) }()
			if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number) VALUES ($1,$2,0,1)`, series+"-0-1", series); err != nil {
				t.Fatal(err)
			}
			for i, episodes := range tc.seasons {
				for _, episode := range episodes {
					id := fmt.Sprintf("%s-%d-%d", series, i+1, episode)
					if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number) VALUES ($1,$2,$3,$4)`, id, series, i+1, episode); err != nil {
						t.Fatal(err)
					}
				}
			}
			target := series + "-" + tc.target
			titles, err := NewPostgresRepository(pool, nil).GetMediaTitles(ctx, []string{target})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := titles[target]
			if !ok || got.AbsoluteEpisodeNumber != tc.absolute || got.SeriesEpisodeCount != tc.total {
				t.Fatalf("episode ordering = %+v, want absolute %d, total %d", got, tc.absolute, tc.total)
			}
		})
	}
}
