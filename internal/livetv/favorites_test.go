package livetv

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFavoriteKind_rejectsCatalogLikeKinds(t *testing.T) {
	// Given a favorite kind outside the Live TV entity contract.
	kind := FavoriteKind("media_item")

	// When the kind is checked at the persistence boundary.
	valid := kind.Valid()

	// Then catalog favorite identities are rejected.
	if valid {
		t.Fatal("media item favorite kind was accepted")
	}
}

func TestFavorite_identityUsesStableIDAndProfile(t *testing.T) {
	// Given two profiles on one account favoring the same stable channel.
	stableID, err := NewSourceQualifiedID("news", "news-1")
	if err != nil {
		t.Fatal(err)
	}
	base := Favorite{UserID: 7, LibraryID: 11, Kind: FavoriteKindChannel, StableID: stableID, AddedAt: time.Unix(1, 0)}

	// When the profile identity changes.
	first := base
	first.ProfileID = "profile-a"
	second := base
	second.ProfileID = "profile-b"

	// Then the identities remain distinct without using transient row IDs.
	if first == second || first.StableID != "news|news-1" {
		t.Fatalf("favorites were not profile-scoped stable identities: %#v %#v", first, second)
	}
}

func TestLiveTVSectionKinds_coverTaskSevenRails(t *testing.T) {
	// Given the typed Live TV home-section registry.
	kinds := LiveTVSectionKinds()

	// When the supported section kinds are inspected.
	seen := make(map[SectionKind]bool, len(kinds))
	for _, kind := range kinds {
		seen[kind] = true
	}

	// Then every task-7 rail is registered without using catalog sections.
	for _, want := range []SectionKind{
		SectionCurrentlyAiring,
		SectionFavoriteChannelsAiring,
		SectionFavoriteProgrammesAiring,
		SectionTopRatedFavoriteProgrammes,
		SectionUpcomingFavoriteProgrammes,
	} {
		if !seen[want] || !want.Valid() {
			t.Fatalf("missing Live TV section kind %q", want)
		}
	}
}

func TestFavoriteRepositorySQL_contractsPreserveIsolationAndOrphans(t *testing.T) {
	// Given the SQL-backed favorite repository source.
	data, err := os.ReadFile("favorites.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)

	// When its query contracts are inspected.
	required := []string{
		"f.user_id = $1 AND f.profile_id = $2 AND f.library_id = $3",
		"JOIN live_tv_channels c ON c.library_id = f.library_id AND c.stable_id = f.stable_id",
		"JOIN live_tv_programmes p ON p.library_id = f.library_id AND p.stable_id = f.stable_id",
		"p.starts_at > $4 AND p.starts_at < $5",
		"p.rating <> '{}'::jsonb",
	}

	// Then every profile, stable-ID, upcoming, orphan, and rating rule is explicit.
	for _, fragment := range required {
		if !strings.Contains(source, fragment) {
			t.Errorf("favorite SQL contract missing %q", fragment)
		}
	}
}
