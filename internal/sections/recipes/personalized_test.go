package recipes

import (
	"encoding/json"
	"testing"
)

func TestPersonalizedRecipesRegistered(t *testing.T) {
	for _, typ := range []string{"recommended_for_you", "because_you_watched", "similar_users_liked", "taste_match"} {
		rec, ok := Get(typ)
		if !ok {
			t.Errorf("recipe %q not registered", typ)
			continue
		}
		if rec.Definition().Category != CategoryPersonalized {
			t.Errorf("%q category = %v want personalized", typ, rec.Definition().Category)
		}
	}
}

func TestCurrentlyAiringRecipeIsLiveTVOnly(t *testing.T) {
	rec, ok := Get("currently_airing")
	if !ok {
		t.Fatal("currently_airing recipe not registered")
	}
	def := rec.Definition()
	if def.Category != CategoryLibraryStaples {
		t.Fatalf("category = %v, want library staples", def.Category)
	}
	if def.RequiredLibraryType != "livetv" {
		t.Fatalf("required library type = %q, want livetv", def.RequiredLibraryType)
	}
	if len(def.Presets) != 1 {
		t.Fatalf("preset count = %d, want 1", len(def.Presets))
	}
	if def.Presets[0].DisplayName != "Currently airing" {
		t.Errorf("display name = %q, want Currently airing", def.Presets[0].DisplayName)
	}
	if def.Presets[0].DescriptionShort == "" {
		t.Error("expected localized-capable description metadata")
	}
}

func TestBecauseYouWatchedAcceptsAnchorParam(t *testing.T) {
	rec, _ := Get("because_you_watched")
	good := json.RawMessage(`{"anchor_item_id":"abc123"}`)
	if err := rec.Validate(good); err != nil {
		t.Fatalf("validate good: %v", err)
	}
	auto := json.RawMessage(`{"anchor_item_id":""}`)
	if err := rec.Validate(auto); err != nil {
		t.Fatalf("validate empty anchor (auto): %v", err)
	}
}
