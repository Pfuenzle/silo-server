package autoscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

var ErrPathMappingRejected = errors.New("path mapping rejected")

var ErrAudiobookPathRejected = ErrPathMappingRejected

type PathMapper struct {
	mappings []PathRewrite
	siloRoot string
}

type AudiobookPathMapper = PathMapper

func NewPathMapper(mappings []PathRewrite, siloRoot string) (*PathMapper, error) {
	root, err := normalizeAbsolutePath(siloRoot)
	if err != nil {
		return nil, fmt.Errorf("Silo audiobook root: %w", err)
	}
	if len(mappings) == 0 {
		return nil, fmt.Errorf("path mappings: %w", ErrAudiobookPathRejected)
	}

	normalized := make([]PathRewrite, 0, len(mappings))
	for i, mapping := range mappings {
		from, fromErr := normalizeAbsolutePath(mapping.From)
		to, toErr := normalizeAbsolutePath(mapping.To)
		if fromErr != nil || toErr != nil {
			return nil, fmt.Errorf("path mapping %d: %w", i, ErrAudiobookPathRejected)
		}
		if !audiobookPathWithinRoot(to, root) {
			return nil, fmt.Errorf("path mapping %d destination: %w", i, ErrAudiobookPathRejected)
		}
		for _, existing := range normalized {
			if audiobookPathWithinRoot(from, existing.From) || audiobookPathWithinRoot(existing.From, from) {
				return nil, fmt.Errorf("path mapping %d overlaps another source: %w", i, ErrAudiobookPathRejected)
			}
		}
		normalized = append(normalized, PathRewrite{From: from, To: to})
	}

	return &PathMapper{mappings: normalized, siloRoot: root}, nil
}

func NewAudiobookPathMapper(mappings []PathRewrite, siloRoot string) (*PathMapper, error) {
	return NewPathMapper(mappings, siloRoot)
}

func NewPathMapperFromConfig(config map[string]any) (*PathMapper, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode path mapping config: %w", err)
	}
	var parsed struct {
		PathMappings []struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		} `json:"path_mappings"`
		SiloRoot string `json:"silo_root"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode path mapping config: %w", err)
	}
	rewrites := make([]PathRewrite, 0, len(parsed.PathMappings))
	for _, mapping := range parsed.PathMappings {
		rewrites = append(rewrites, PathRewrite{From: mapping.Source, To: mapping.Destination})
	}
	root := parsed.SiloRoot
	if root == "" && len(rewrites) > 0 {
		root = rewrites[0].To
	}
	return NewPathMapper(rewrites, root)
}

func (m *PathMapper) MapFolder(sourcePath string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("nil mapper: %w", ErrAudiobookPathRejected)
	}
	path, err := normalizeAbsolutePath(sourcePath)
	if err != nil {
		return "", fmt.Errorf("source path: %w", ErrAudiobookPathRejected)
	}
	for _, mapping := range m.mappings {
		if !audiobookPathWithinRoot(path, mapping.From) {
			continue
		}
		mapped := normalizeAudiobookPath(mapping.To + strings.TrimPrefix(path, mapping.From))
		if !audiobookPathWithinRoot(mapped, m.siloRoot) {
			return "", fmt.Errorf("mapped path: %w", ErrAudiobookPathRejected)
		}
		return mapped, nil
	}
	return "", fmt.Errorf("source path has no mapping: %w", ErrAudiobookPathRejected)
}

func normalizeAbsolutePath(rawPath string) (string, error) {
	if strings.ContainsRune(rawPath, '\x00') {
		return "", ErrAudiobookPathRejected
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(rawPath), "\\", "/")
	if normalized == "" || !strings.HasPrefix(normalized, "/") {
		return "", ErrAudiobookPathRejected
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", ErrAudiobookPathRejected
		}
	}
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == "" || cleaned == "/" {
		return "", ErrAudiobookPathRejected
	}
	return cleaned, nil
}

func normalizeAudiobookPath(rawPath string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(rawPath), "\\", "/")
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	return path.Clean(normalized)
}

func audiobookPathWithinRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
