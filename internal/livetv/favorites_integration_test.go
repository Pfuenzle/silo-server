package livetv

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresLiveTVFavoritesProfilesAndSections(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set; run with the task-7 PostgreSQL proof command")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	migrationCtx, cancel := database.MigrationContext(ctx)
	if err := database.RunMigrations(migrationCtx, pool, migrations.FS, "sql"); err != nil {
		cancel()
		t.Fatalf("apply migrations: %v", err)
	}
	cancel()
	var favoritesTable string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.live_tv_favorites')::text`).Scan(&favoritesTable); err != nil {
		t.Fatalf("check task-7 migration: %v", err)
	}
	if favoritesTable != "live_tv_favorites" {
		t.Fatalf("task-7 migration is not applied: %q", favoritesTable)
	}

	fixture := seedFavoriteIntegrationFixture(t, pool)
	repo := NewPostgresRepository(pool)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	channelFavorite := Favorite{UserID: fixture.userID, ProfileID: fixture.profileA, LibraryID: fixture.libraryID, Kind: FavoriteKindChannel, StableID: fixture.channelStableID, AddedAt: now}
	programmeFavorite := Favorite{UserID: fixture.userID, ProfileID: fixture.profileA, LibraryID: fixture.libraryID, Kind: FavoriteKindProgramme, StableID: fixture.currentStableID, AddedAt: now}
	upcomingFavorite := programmeFavorite
	upcomingFavorite.StableID = fixture.upcomingStableID
	if err := repo.AddFavorite(ctx, channelFavorite); err != nil {
		t.Fatalf("add channel favorite: %v", err)
	}
	if err := repo.AddFavorite(ctx, programmeFavorite); err != nil {
		t.Fatalf("add programme favorite: %v", err)
	}
	if err := repo.AddFavorite(ctx, upcomingFavorite); err != nil {
		t.Fatalf("add upcoming programme favorite: %v", err)
	}
	profileBFavorite := channelFavorite
	profileBFavorite.ProfileID = fixture.profileB
	if err := repo.AddFavorite(ctx, profileBFavorite); err != nil {
		t.Fatalf("add profile-b favorite: %v", err)
	}

	listA, err := repo.ListFavorites(ctx, fixture.userID, fixture.profileA, fixture.libraryID, FavoriteKindChannel, 20, 0)
	if err != nil || len(listA) != 1 || listA[0].StableID != fixture.channelStableID {
		t.Fatalf("profile-a channel favorites = %#v, err %v", listA, err)
	}
	listB, err := repo.ListFavorites(ctx, fixture.userID, fixture.profileB, fixture.libraryID, FavoriteKindChannel, 20, 0)
	if err != nil || len(listB) != 1 || listB[0].StableID != fixture.channelStableID {
		t.Fatalf("profile-b channel favorites = %#v, err %v", listB, err)
	}
	programmeListB, err := repo.ListFavorites(ctx, fixture.userID, fixture.profileB, fixture.libraryID, FavoriteKindProgramme, 20, 0)
	if err != nil || len(programmeListB) != 0 {
		t.Fatalf("profile-b programme favorites = %#v, err %v", programmeListB, err)
	}
	ownedByA, err := repo.IsFavorite(ctx, channelFavorite)
	if err != nil || !ownedByA {
		t.Fatalf("profile-a favorite membership = %v, err %v", ownedByA, err)
	}
	removedB := channelFavorite
	removedB.ProfileID = fixture.profileB
	if err := repo.RemoveFavorite(ctx, removedB); err != nil {
		t.Fatalf("remove profile-b favorite: %v", err)
	}
	stillOwnedByA, err := repo.IsFavorite(ctx, channelFavorite)
	if err != nil || !stillOwnedByA {
		t.Fatalf("profile-a favorite after profile-b removal = %v, err %v", stillOwnedByA, err)
	}

	current, err := repo.ListFavoriteProgrammesNow(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, 20)
	if err != nil || len(current) != 1 || current[0].StableID != fixture.currentStableID {
		t.Fatalf("favorite current programmes = %#v, err %v", current, err)
	}
	channels, err := repo.ListFavoriteChannelsNow(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, 20)
	if err != nil || len(channels) != 1 || channels[0].StableID != fixture.channelStableID {
		t.Fatalf("favorite current channels = %#v, err %v", channels, err)
	}
	upcoming, err := repo.ListUpcomingFavoriteProgrammes(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, now.Add(24*time.Hour), 20)
	if err != nil || len(upcoming) != 1 || upcoming[0].StableID != fixture.upcomingStableID {
		t.Fatalf("upcoming favorite programmes = %#v, err %v", upcoming, err)
	}
	topRated, err := repo.ListFavoriteProgrammes(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, now.Add(24*time.Hour), true, 20)
	if err != nil || len(topRated) != 1 || topRated[0].StableID != fixture.currentStableID {
		t.Fatalf("top-rated favorite programmes = %#v, err %v", topRated, err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM live_tv_programmes WHERE stable_id = $1`, fixture.currentStableID); err != nil {
		t.Fatalf("remove current programme snapshot row: %v", err)
	}
	visibleAfterOrphan, err := repo.ListFavorites(ctx, fixture.userID, fixture.profileA, fixture.libraryID, FavoriteKindProgramme, 20, 0)
	if err != nil || len(visibleAfterOrphan) != 2 {
		t.Fatalf("orphan favorite persistence = %#v, err %v", visibleAfterOrphan, err)
	}
	currentAfterOrphan, err := repo.ListFavoriteProgrammesNow(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, 20)
	if err != nil || len(currentAfterOrphan) != 0 {
		t.Fatalf("orphan current section = %#v, err %v", currentAfterOrphan, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE live_tv_channels SET stable_id = $2 WHERE id = $1`, fixture.channelID, "playlist-a|retargeted"); err != nil {
		t.Fatalf("change channel stable identity: %v", err)
	}
	retargeted, err := repo.ListFavoriteChannelsNow(ctx, fixture.userID, fixture.profileA, fixture.libraryID, now, 20)
	if err != nil || len(retargeted) != 0 {
		t.Fatalf("stable-ID change retargeted favorite = %#v, err %v", retargeted, err)
	}
}

type favoriteIntegrationFixture struct {
	userID, libraryID, channelID                       int
	profileA, profileB                                 string
	channelStableID, currentStableID, upcomingStableID SourceQualifiedID
}

func seedFavoriteIntegrationFixture(t *testing.T, pool *pgxpool.Pool) favoriteIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	var fixture favoriteIntegrationFixture
	name := fmt.Sprintf("task7-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, name).Scan(&fixture.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	fixture.profileA, fixture.profileB = "task7-a", "task7-b"
	for _, profileID := range []string{fixture.profileA, fixture.profileB} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $1)`, profileID, fixture.userID); err != nil {
			t.Fatalf("seed profile %s: %v", profileID, err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('livetv', $1) RETURNING id`, name).Scan(&fixture.libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	var sourceID int64
	if err := pool.QueryRow(ctx, `INSERT INTO live_tv_sources (library_id, kind, source_key, name, location) VALUES ($1, 'playlist', 'playlist-a', 'Playlist', 'https://example.invalid/list') RETURNING id`, fixture.libraryID).Scan(&sourceID); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	fixture.channelStableID = SourceQualifiedID("playlist-a|channel-1")
	fixture.currentStableID = SourceQualifiedID("playlist-a|programme-current")
	fixture.upcomingStableID = SourceQualifiedID("playlist-a|programme-upcoming")
	if err := pool.QueryRow(ctx, `INSERT INTO live_tv_channels (library_id, source_id, external_id, stable_id, name, stream_url) VALUES ($1, $2, 'channel-1', $3, 'News', 'https://example.invalid/news') RETURNING id`, fixture.libraryID, sourceID, fixture.channelStableID).Scan(&fixture.channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	programmes := []struct {
		stableID     SourceQualifiedID
		starts, ends time.Time
		rating       string
	}{
		{fixture.currentStableID, now.Add(-time.Hour), now.Add(time.Hour), `{"value": "9"}`},
		{fixture.upcomingStableID, now.Add(2 * time.Hour), now.Add(3 * time.Hour), `{}`},
	}
	for _, programme := range programmes {
		if _, err := pool.Exec(ctx, `INSERT INTO live_tv_programmes (library_id, source_id, channel_id, external_id, stable_id, title, starts_at, ends_at, rating) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)`, fixture.libraryID, sourceID, fixture.channelID, string(programme.stableID), programme.stableID, string(programme.stableID), programme.starts, programme.ends, programme.rating); err != nil {
			t.Fatalf("seed programme %s: %v", programme.stableID, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, fixture.libraryID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_profiles WHERE user_id = $1`, fixture.userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, fixture.userID)
	})
	return fixture
}
