package sections

import (
	"testing"
)

func TestParseQueryDefinitionCapsGeneratedRecentEpisodes(t *testing.T) {
	config := []byte(`{
		"media_scope":"episode",
		"match":"all",
		"groups":[],
		"sort":{"field":"release_date","order":"desc"},
		"generated_kind":"recently_released_episodes",
		"generated_source":"home_library_recent"
	}`)

	def, err := ParseQueryDefinition(config)
	if err != nil {
		t.Fatalf("ParseQueryDefinition() error = %v", err)
	}
	if len(def.Groups) != 1 || len(def.Groups[0].Rules) != 1 {
		t.Fatalf("generated recent episode groups = %+v, want one future-cap rule", def.Groups)
	}
	rule := def.Groups[0].Rules[0]
	if rule.Field != "release_date" || rule.Op != "in_next" || rule.Value != "4d" {
		t.Fatalf("generated recent episode rule = %+v, want release_date in_next 4d", rule)
	}
}
