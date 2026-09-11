package livetv

import (
	"bytes"
	"context"
	"fmt"
)

type SourceIngestor interface {
	Ingest(context.Context, Source, map[string]ChannelMapping) (SourceSnapshot, Diagnostics, error)
}

func (s *FetchService) Ingest(ctx context.Context, source Source, mappings map[string]ChannelMapping) (SourceSnapshot, Diagnostics, error) {
	result, err := s.Fetch(ctx, source)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	switch source.Kind {
	case SourceKindPlaylist:
		snapshot, diagnostics, parseErr := ParseM3U(ctx, source.SourceKey, bytes.NewReader(result.Body), s.policy.MaxBodyBytes)
		return snapshot, diagnostics, parseErr
	case SourceKindEPG:
		snapshot, diagnostics, parseErr := ParseXMLTV(ctx, source.SourceKey, bytes.NewReader(result.Body), s.policy.MaxBodyBytes, mappings)
		return snapshot, diagnostics, parseErr
	default:
		return SourceSnapshot{}, nil, fmt.Errorf("unsupported Live TV source kind %q", source.Kind)
	}
}
