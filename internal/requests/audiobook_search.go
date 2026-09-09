package requests

import (
	"context"
)

type AudiobookSearchFunc func(context.Context, Viewer, string) ([]AudiobookSearchResult, error)

func (f AudiobookSearchFunc) SearchAudiobooks(ctx context.Context, viewer Viewer, query string) ([]AudiobookSearchResult, error) {
	return f(ctx, viewer, query)
}
