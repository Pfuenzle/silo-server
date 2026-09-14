package livetv

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newQualityTestService(t *testing.T, master string) *LivePlaybackService {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = response.Write([]byte(master))
	}))
	t.Cleanup(server.Close)
	return NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{IP: net.ParseIP("198.51.100.2")}, Dialer: liveDialer(server.Listener.Addr().String())}),
		ProxyOrigin: "https://silo.example",
		Authority:   liveTestAuthority{StreamURL: server.URL},
	})
}

func qualityTestRequest() LivePlaybackRequest {
	return LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, SessionID: "session-a", Mode: LivePlaybackModeHLS, ChannelID: SourceQualifiedID("fixture|channel-1")}
}

func TestLivePlayback_QualityOptions_returnsServerOwnedVariants(t *testing.T) {
	// Given a live HLS grant whose provider exposes a master playlist.
	service := newQualityTestService(t, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720\nhigh.m3u8\n")
	grant, err := service.Start(context.Background(), qualityTestRequest())
	if err != nil {
		t.Fatal(err)
	}

	// When the client asks for the server-owned quality ladder.
	identity := WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"})
	options, err := service.QualityOptions(identity, grant.GrantID)

	// Then only opaque quality IDs and public metadata are returned.
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].ID == "" || options[1].Height != 720 {
		t.Fatalf("options = %#v", options)
	}
}

func TestParseLiveAudioTracks_readsProviderAudioRenditions(t *testing.T) {
	// Given a provider master playlist with two declared audio renditions.
	body := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",LANGUAGE="de",NAME="Deutsch",DEFAULT=YES
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="commentary",LANGUAGE="en",NAME="Commentary",DEFAULT=NO
`

	// When the provider audio inventory is parsed.
	tracks := parseLiveAudioTracks(body)

	// Then both provider-owned tracks and their defaults are preserved.
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Language != "de" || tracks[0].Name != "Deutsch" || !tracks[0].Default {
		t.Fatalf("first track = %#v, want German default", tracks[0])
	}
	if tracks[1].Language != "en" || tracks[1].Name != "Commentary" || tracks[1].Default {
		t.Fatalf("second track = %#v, want English non-default", tracks[1])
	}
}

func TestLivePlayback_SelectQuality_rebindsGrantWithoutExposingSource(t *testing.T) {
	// Given a live HLS grant with two server-known variants.
	service := newQualityTestService(t, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720\nhigh.m3u8\n")
	grant, err := service.Start(context.Background(), qualityTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	identity := WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"})
	options, err := service.QualityOptions(identity, grant.GrantID)
	if err != nil {
		t.Fatal(err)
	}

	// When the client selects the high variant.
	selected, err := service.SelectQuality(identity, grant.GrantID, options[1].ID)

	// Then the grant remains live and its provider URL remains private.
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != options[1].ID || selected.Label != "720p" {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestLivePlayback_SelectQuality_rejectsUnknownVariant(t *testing.T) {
	service := newQualityTestService(t, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nlow.m3u8\n")
	grant, err := service.Start(context.Background(), qualityTestRequest())
	if err != nil {
		t.Fatal(err)
	}

	identity := WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"})
	_, err = service.SelectQuality(identity, grant.GrantID, "provider-url")
	if !errors.Is(err, ErrLivePlaybackQualityUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestLivePlayback_QualityState_reportsActiveSelection(t *testing.T) {
	service := newQualityTestService(t, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720\nhigh.m3u8\n")
	grant, err := service.Start(context.Background(), qualityTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	identity := WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"})
	option, err := service.SelectQuality(identity, grant.GrantID, "q-720-2400000")
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.QualityState(identity, grant.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveID != option.ID || state.TranscodingSupported || len(state.Options) != 1 {
		t.Fatalf("state = %#v", state)
	}
}
