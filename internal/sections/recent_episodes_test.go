package sections

import (
	"testing"
)

func TestParseQueryDefinitionNormalizesGeneratedEpisodeCap(t *testing.T) {
	config := []byte(`{
		"media_scope":"episode",
		"match":"all",
		"groups":[{"match":"all","rules":[{"field":"release_date","op":"in_next","value":"4d"}]}],
		"sort":{"field":"release_date","order":"desc"},
		"generated_kind":"recently_released_episodes",
		"generated_source":"home_library_recent"
	}`)
	def, err := ParseQueryDefinition(config)
	if err != nil {
		t.Fatalf("ParseQueryDefinition() error = %v", err)
	}
	if len(def.Groups) != 1 || len(def.Groups[0].Rules) != 1 {
		t.Fatalf("generated episode groups = %+v, want one cap rule", def.Groups)
	}
	rule := def.Groups[0].Rules[0]
	if rule.Field != "release_date" || rule.Op != "in_next" || rule.Value != "1d" {
		t.Fatalf("generated episode cap = %+v, want release_date in_next 1d", rule)
	}
}
