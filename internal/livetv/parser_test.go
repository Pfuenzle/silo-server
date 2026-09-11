package livetv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseM3U_readsChannelsAndReportsMalformedDuplicates(t *testing.T) {
	// Given a playlist with valid rows, a duplicate id, a missing URL, and a malformed row.
	playlist := `#EXTM3U
#EXTINF:-1 tvg-id="news-1" tvg-name="News" tvg-logo="https://img.invalid/news.png" group-title="News",News
https://stream.invalid/news
#EXTINF:-1 tvg-id="news-1" tvg-chno="2",Duplicate
https://stream.invalid/duplicate
#EXTINF:-1 tvg-id="sports-1" tvg-chno="3",Sports
#EXTINF:-1 tvg-id="broken",Broken
https://stream.invalid/broken
`

	// When the playlist is parsed.
	snapshot, diagnostics, err := ParseM3U(context.Background(), "playlist-a", strings.NewReader(playlist), 4096)

	// Then only complete unique rows are retained and diagnostics are explicit.
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(snapshot.Channels))
	}
	if snapshot.Channels[0].StableID.String() != "playlist-a|news-1" || snapshot.Channels[0].Artwork == nil || snapshot.Channels[0].Category != "News" {
		t.Fatalf("first channel = %#v", snapshot.Channels[0])
	}
	if snapshot.Channels[1].Category != "" {
		t.Fatalf("missing category = %q, want empty", snapshot.Channels[1].Category)
	}
	if !diagnostics.HasCode("duplicate_id") || !diagnostics.HasCode("missing_stream_url") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestParseM3U_parsesEscapedGroupTitle(t *testing.T) {
	// Given a group-title containing an escaped quote and an HTML entity.
	playlist := `#EXTM3U
#EXTINF:-1 tvg-id="culture" group-title="News \"&amp;\" Culture",Culture
https://stream.invalid/culture
`

	// When the playlist is parsed.
	snapshot, diagnostics, err := ParseM3U(context.Background(), "playlist-a", strings.NewReader(playlist), 4096)

	// Then the category is decoded deterministically without a diagnostic.
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 || len(snapshot.Channels) != 1 || snapshot.Channels[0].Category != `News "&" Culture` {
		t.Fatalf("snapshot = %#v, diagnostics = %#v", snapshot, diagnostics)
	}
}

func TestParseM3U_rejectsBodyOverLimitAndHonorsCancellation(t *testing.T) {
	// Given a playlist body exceeding the configured bound.
	// When it is parsed.
	_, _, err := ParseM3U(context.Background(), "playlist-a", strings.NewReader("#EXTM3U\n"+strings.Repeat("x", 32)), 16)
	// Then the typed size error is returned before unbounded allocation occurs.
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = ParseM3U(ctx, "playlist-a", strings.NewReader("#EXTM3U\n"), 4096)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want context.Canceled", err)
	}
}

func TestParseM3U_reportsTrailingEntryWithoutStreamURL(t *testing.T) {
	// Given a playlist ending immediately after an EXTINF row.
	playlist := "#EXTM3U\n#EXTINF:-1 tvg-id=\"trailing\",Trailing\n"

	// When the playlist is parsed.
	_, diagnostics, err := ParseM3U(context.Background(), "playlist-a", strings.NewReader(playlist), 4096)

	// Then EOF produces the documented missing stream URL diagnostic.
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostics.HasCode("missing_stream_url") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestParseXMLTV_mapsByProviderIDBeforeDisplayNameAndParsesMetadata(t *testing.T) {
	// Given XMLTV with provider ids, names, current/upcoming programmes, image, and rating.
	xml := `<tv><channel id="xml-news"><display-name>News</display-name></channel><channel id="xml-sports"><display-name>Sports</display-name></channel><programme channel="xml-news" start="20260910100000 +0000" stop="20260910110000 +0000"><title>Morning</title><desc>Headlines</desc><icon src="https://img.invalid/morning.png"/><rating><value>4</value></rating></programme></tv>`

	// When it is parsed against the playlist channel namespace.
	snapshot, diagnostics, err := ParseXMLTV(context.Background(), "epg-a", strings.NewReader(xml), 4096, map[string]ChannelMapping{"news-1": {ProviderID: "xml-news", DisplayName: "News"}})

	// Then the programme is source-qualified, mapped, and time-bounded.
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Programmes) != 1 || snapshot.Programmes[0].ChannelExternalID != "news-1" {
		t.Fatalf("snapshot = %#v, diagnostics = %#v", snapshot, diagnostics)
	}
	programme := snapshot.Programmes[0]
	if programme.Title != "Morning" || programme.Artwork == nil || string(programme.Rating) != `{"value":"4"}` {
		t.Fatalf("programme metadata = %#v", programme)
	}
	if !programme.StartsAt.Equal(time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)) || !programme.EndsAt.After(programme.StartsAt) {
		t.Fatalf("programme window = %v - %v", programme.StartsAt, programme.EndsAt)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestParseXMLTV_reportsInvalidTimesAndAmbiguousFallback(t *testing.T) {
	// Given an invalid time and a display-name matching multiple channels.
	xml := `<tv><channel id="xml"><display-name>xml</display-name></channel><programme channel="xml" start="bad" stop="also-bad"><title>Bad</title></programme></tv>`
	// When it is parsed with ambiguous fallback candidates.
	_, diagnostics, err := ParseXMLTV(context.Background(), "epg-a", strings.NewReader(xml), 4096, map[string]ChannelMapping{
		"one": {DisplayName: "xml"}, "two": {DisplayName: "xml"},
	})
	// Then parsing remains usable and diagnostics explain both issues.
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostics.HasCode("invalid_timestamp") || !diagnostics.HasCode("ambiguous_channel") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestParseXMLTV_reportsDuplicateProviderIDsDeterministically(t *testing.T) {
	// Given two playlist channels claiming the same provider identifier.
	xml := `<tv><channel id="xml-news"><display-name>News</display-name></channel><programme channel="xml-news" start="20260910100000 +0000" stop="20260910110000 +0000"><title>Morning</title></programme></tv>`
	mappings := map[string]ChannelMapping{
		"z-news": {ProviderID: "xml-news", DisplayName: "News"},
		"a-news": {ProviderID: "xml-news", DisplayName: "News"},
	}

	// When the XMLTV feed is parsed.
	snapshot, diagnostics, err := ParseXMLTV(context.Background(), "epg-a", strings.NewReader(xml), 4096, mappings)

	// Then the duplicate provider namespace is rejected rather than map-order selected.
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Programmes) != 0 {
		t.Fatalf("programmes = %#v, want no ambiguous mapping", snapshot.Programmes)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != "ambiguous_provider_id" || diagnostics[0].Message != "xml-news: a-news,z-news" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}
