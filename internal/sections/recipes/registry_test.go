package recipes

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type stubRecipe struct {
	t   string
	cat Category
}

func (s *stubRecipe) Type() string { return s.t }
func (s *stubRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{Type: s.t, Category: s.cat}
}
func (s *stubRecipe) NewParams() any                                   { return &struct{}{} }
func (s *stubRecipe) Validate(_ json.RawMessage) error                 { return nil }
func (s *stubRecipe) Resolve(_ ResolverContext) (ResolvedItems, error) { return ResolvedItems{}, nil }
func (s *stubRecipe) DefaultCacheTTL() time.Duration                   { return time.Minute }

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubRecipe{t: "alpha", cat: CategoryDiscovery})

	got, ok := r.Get("alpha")
	if !ok {
		t.Fatal("alpha not found")
	}
	if got.Type() != "alpha" {
		t.Fatalf("got %s want alpha", got.Type())
	}

	if _, ok := r.Get("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate type")
		}
	}()
	r := NewRegistry()
	r.Register(&stubRecipe{t: "dup"})
	r.Register(&stubRecipe{t: "dup"})
}

func TestListByCategoryReturnsStableOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubRecipe{t: "b", cat: CategoryDiscovery})
	r.Register(&stubRecipe{t: "a", cat: CategoryDiscovery})
	r.Register(&stubRecipe{t: "c", cat: CategoryEditorial})

	disc := r.ListByCategory(CategoryDiscovery)
	if len(disc) != 2 {
		t.Fatalf("got %d want 2", len(disc))
	}
	if disc[0].Type() != "a" || disc[1].Type() != "b" {
		t.Fatalf("not sorted: %v %v", disc[0].Type(), disc[1].Type())
	}
}

func TestListByCategoryIncludesLiveTVCurrentlyAiringRecipe(t *testing.T) {
	recipes := ListByCategory(CategoryLibraryStaples)
	for _, recipe := range recipes {
		if recipe.Type() != "currently_airing" {
			continue
		}
		if got := recipe.Definition().RequiredLibraryType; got != "livetv" {
			t.Fatalf("currently_airing required library type = %q, want livetv", got)
		}
		return
	}
	t.Fatal("currently_airing recipe missing from library staples")
}

func TestListReturnsAllRegistered(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubRecipe{t: "x"})
	r.Register(&stubRecipe{t: "y"})
	if len(r.List()) != 2 {
		t.Fatalf("got %d want 2", len(r.List()))
	}
}

// Compile-time check that stubRecipe satisfies the interface and that ResolverContext compiles.
var _ Recipe = (*stubRecipe)(nil)
var _ context.Context = context.Background()
