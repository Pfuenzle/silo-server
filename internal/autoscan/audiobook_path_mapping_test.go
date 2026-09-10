package autoscan

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPathMappingConfig_MapsGenericRouterMetadataPath(t *testing.T) {
	// Given a generic request-router config with a Unicode source root
	config := map[string]any{
		"path_mappings": []any{
			map[string]any{
				"source":      "/media/书籍/Hörbücher",
				"destination": "/mnt/media/audiobooks",
			},
		},
		"silo_root": "/mnt/media/audiobooks",
	}

	// When the host constructs its mapper from the generic config
	mapper, err := NewPathMapperFromConfig(config)

	// Then the raw imported path maps into the contained Silo path
	if err != nil {
		t.Fatalf("construct generic mapper: %v", err)
	}
	got, err := mapper.MapFolder("/media/书籍/Hörbücher/作者/三体")
	if err != nil {
		t.Fatalf("map generic imported path: %v", err)
	}
	if want := "/mnt/media/audiobooks/作者/三体"; got != want {
		t.Fatalf("mapped path = %q, want %q", got, want)
	}
}

func TestPathMappingConfig_RejectsUntrustedConfigBeforeEnqueue(t *testing.T) {
	// Given generic router config with a traversal mapping
	config := map[string]any{
		"path_mappings": []any{map[string]any{
			"source":      "/media/books",
			"destination": "/mnt/media/audiobooks/../escape",
		}},
		"silo_root": "/mnt/media/audiobooks",
	}

	// When the host parses the config boundary
	_, err := NewPathMapperFromConfig(config)

	// Then the unsafe mapper is rejected before a scan can be enqueued
	if err == nil || !errors.Is(err, ErrAudiobookPathRejected) {
		t.Fatalf("error = %v, want ErrAudiobookPathRejected", err)
	}
}

func TestPathMappingConfig_UsesTypedBoundaryInput(t *testing.T) {
	// Given a JSON-shaped generic config from a router capability
	data := []byte(`{"path_mappings":[{"source":"/source","destination":"/mnt/books"}],"silo_root":"/mnt/books"}`)
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode config fixture: %v", err)
	}

	// When the generic config is parsed
	mapper, err := NewPathMapperFromConfig(config)

	// Then it produces a usable typed mapper without capability knowledge
	if err != nil {
		t.Fatalf("construct mapper: %v", err)
	}
	if _, err := mapper.MapFolder("/source/title"); err != nil {
		t.Fatalf("map typed config: %v", err)
	}
}

func TestAudiobookPathMapping_UnicodeAndNestedFolders(t *testing.T) {
	// Given
	mapper, err := NewAudiobookPathMapper(
		[]PathRewrite{{
			From: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted",
			To:   "/mnt/media/audiobooks",
		}},
		"/mnt/media/audiobooks",
	)
	if err != nil {
		t.Fatalf("construct mapper: %v", err)
	}

	// When
	got, err := mapper.MapFolder("/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted/作者/三体")

	// Then
	if err != nil {
		t.Fatalf("map nested Unicode folder: %v", err)
	}
	if want := "/mnt/media/audiobooks/作者/三体"; got != want {
		t.Fatalf("mapped folder = %q, want %q", got, want)
	}
}

func TestAudiobookPathMapping_RejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		mappings []PathRewrite
		siloRoot string
	}{
		{name: "empty mappings", mappings: nil, siloRoot: "/mnt/media/audiobooks"},
		{name: "empty source", mappings: []PathRewrite{{To: "/mnt/media/audiobooks"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "empty destination", mappings: []PathRewrite{{From: "/source"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "relative source", mappings: []PathRewrite{{From: "source", To: "/mnt/media/audiobooks"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "relative destination", mappings: []PathRewrite{{From: "/source", To: "target"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "source traversal", mappings: []PathRewrite{{From: "/source/../books", To: "/mnt/media/audiobooks"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "destination traversal", mappings: []PathRewrite{{From: "/source", To: "/mnt/media/audiobooks/../escape"}}, siloRoot: "/mnt/media/audiobooks"},
		{name: "empty Silo root", mappings: []PathRewrite{{From: "/source", To: "/mnt/media/audiobooks"}}, siloRoot: ""},
		{name: "overlapping sources", mappings: []PathRewrite{
			{From: "/media/books", To: "/mnt/media/audiobooks"},
			{From: "/media/books/audiobooks", To: "/mnt/media/audiobooks/nested"},
		}, siloRoot: "/mnt/media/audiobooks"},
		{name: "duplicate sources", mappings: []PathRewrite{
			{From: "/media/books", To: "/mnt/media/audiobooks"},
			{From: "/media/books/", To: "/mnt/media/audiobooks"},
		}, siloRoot: "/mnt/media/audiobooks"},
		{name: "destination outside Silo root", mappings: []PathRewrite{{From: "/source", To: "/mnt/media/other"}}, siloRoot: "/mnt/media/audiobooks"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := NewAudiobookPathMapper(tt.mappings, tt.siloRoot)

			// Then
			if err == nil {
				t.Fatal("expected invalid mapper configuration to be rejected")
			}
		})
	}
}

func TestAudiobookPathMapping_RejectsUnsafeOrUnmatchedPaths(t *testing.T) {
	// Given
	mapper, err := NewAudiobookPathMapper(
		[]PathRewrite{{
			From: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted",
			To:   "/mnt/media/audiobooks",
		}},
		"/mnt/media/audiobooks",
	)
	if err != nil {
		t.Fatalf("construct mapper: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: "作者/三体"},
		{name: "traversal", path: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted/../secret"},
		{name: "traversal at root", path: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted/../../secret"},
		{name: "sibling prefix", path: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted_old/作者/三体"},
		{name: "outside root", path: "/media/blyatflix/media/Books/Hörbücher/Other/作者/三体"},
		{name: "malformed NUL", path: "/media/blyatflix/media/Books/Hörbücher/Audiobooks_sorted/作者\x00/三体"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := mapper.MapFolder(tt.path)

			// Then
			if err == nil {
				t.Fatal("expected unsafe or unmatched path to be rejected")
			}
			if !errors.Is(err, ErrAudiobookPathRejected) {
				t.Fatalf("error = %v, want ErrAudiobookPathRejected", err)
			}
		})
	}
}

func TestAudiobookPathMapping_RejectsMappedOutputOutsideSiloRoot(t *testing.T) {
	// Given
	mapper := AudiobookPathMapper{
		mappings: []PathRewrite{{From: "/source", To: "/mnt/media/audiobooks"}},
		siloRoot: "/mnt/media/audiobooks/library",
	}

	// When
	_, err := mapper.MapFolder("/source/author/title")

	// Then
	if err == nil {
		t.Fatal("expected mapped output outside Silo root to be rejected")
	}
}

func TestAudiobookPathMapping_NormalizesDotSegments(t *testing.T) {
	// Given
	mapper, err := NewAudiobookPathMapper(
		[]PathRewrite{{From: "/source", To: "/mnt/media/audiobooks"}},
		"/mnt/media/audiobooks",
	)
	if err != nil {
		t.Fatalf("construct mapper: %v", err)
	}

	// When
	got, err := mapper.MapFolder("/source/author/./title")

	// Then
	if err != nil {
		t.Fatalf("map dot-segment folder: %v", err)
	}
	if want := "/mnt/media/audiobooks/author/title"; got != want {
		t.Fatalf("mapped folder = %q, want %q", got, want)
	}
}
