package catalog

import (
	"strings"
	"testing"
)

func TestParseCatalogSectionQueryDefinitionCapsGeneratedRecentEpisodes(t *testing.T) {
	config := []byte(`{
		"media_scope":"episode",
		"match":"all",
		"groups":[],
		"sort":{"field":"release_date","order":"desc"},
		"generated_kind":"recently_released_episodes",
		"generated_source":"home_library_recent"
	}`)

	def, err := parseCatalogSectionQueryDefinition(config)
	if err != nil {
		t.Fatalf("parseCatalogSectionQueryDefinition() error = %v", err)
	}

	if len(def.Groups) != 1 || len(def.Groups[0].Rules) != 1 {
		t.Fatalf("recent episode groups = %+v, want one future-cap rule", def.Groups)
	}
	rule := def.Groups[0].Rules[0]
	if rule.Field != "release_date" || rule.Op != "in_next" || rule.Value != "4d" {
		t.Fatalf("recent episode rule = %+v, want release_date in_next 4d", rule)
	}
}

func TestBuildReleaseDateInNextCapsFutureEpisodes(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").WithMediaScope("episode").Build(QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{{Field: "release_date", Op: "in_next", Value: "4d"}},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
	if !strings.Contains(clause, "mi.episode_air_date <= (CURRENT_DATE + INTERVAL '4 days')::date") {
		t.Fatalf("future cap clause = %q", clause)
	}
	if strings.Contains(clause, "mi.episode_air_date >=") {
		t.Fatalf("future cap unexpectedly added a lower bound: %q", clause)
	}
}

func TestParseCatalogSectionQueryDefinitionLeavesRegularCustomFiltersUnchanged(t *testing.T) {
	config := []byte(`{
		"media_scope":"episode",
		"match":"all",
		"groups":[],
		"sort":{"field":"release_date","order":"desc"}
	}`)

	def, err := parseCatalogSectionQueryDefinition(config)
	if err != nil {
		t.Fatalf("parseCatalogSectionQueryDefinition() error = %v", err)
	}
	if len(def.Groups) != 0 {
		t.Fatalf("regular custom filter unexpectedly gained rules: %+v", def.Groups)
	}
}
