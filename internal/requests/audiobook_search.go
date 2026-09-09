package requests

import (
	"context"
	"strings"
	"unicode"
)

type AudiobookSearchFunc func(context.Context, Viewer, string) ([]AudiobookSearchResult, error)

func (f AudiobookSearchFunc) SearchAudiobooks(ctx context.Context, viewer Viewer, query string) ([]AudiobookSearchResult, error) {
	return f(ctx, viewer, query)
}

func AudiobookTitlesMatch(left, right string) bool {
	leftTokens := audiobookTitleTokens(left)
	rightTokens := audiobookTitleTokens(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(leftTokens))
	for _, token := range leftTokens {
		seen[token] = struct{}{}
	}
	overlap := 0
	for _, token := range rightTokens {
		if _, ok := seen[token]; ok {
			overlap++
		}
	}
	shorter := len(leftTokens)
	if len(rightTokens) < shorter {
		shorter = len(rightTokens)
	}
	if shorter <= 2 {
		return overlap == shorter
	}
	return overlap >= 3 && overlap*2 >= shorter
}

func audiobookTitleTokens(title string) []string {
	var builder strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) {
			builder.WriteRune(r)
		} else {
			builder.WriteByte(' ')
		}
	}
	ignored := map[string]struct{}{"season": {}, "teil": {}, "part": {}, "folge": {}}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, token := range strings.Fields(builder.String()) {
		if _, skip := ignored[token]; skip {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}
