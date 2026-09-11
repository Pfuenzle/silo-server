package livetv

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestLivePlayback_Start_rejectsNodeExecutionUntilControlledNodeAuthorityExists(t *testing.T) {
	// Given a valid channel request that asks for a transcode-node source.
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}})

	// When the request attempts to hand source execution to a node.
	_, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeHLS, NodeReference: "node-source-1"})

	// Then the supported server-proxy path refuses the unsupported node path.
	if !errors.Is(err, ErrLivePlaybackUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrLivePlaybackUnavailable)
	}
}

func TestLivePlayback_Reconnect_isBoundedAndCreatesFreshGrant(t *testing.T) {
	// Given a valid server-proxy live playback request.
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}})
	request := LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeHLS}

	// When the client reconnects repeatedly after provider/session loss.
	first, err := service.Reconnect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	service.Revoke(first.GrantID)
	second, err := service.Reconnect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	service.Revoke(second.GrantID)
	third, err := service.Reconnect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	service.Revoke(third.GrantID)
	_, err = service.Reconnect(context.Background(), request)

	// Then reconnect remains bounded and every successful attempt uses a new grant.
	if !errors.Is(err, ErrLivePlaybackReconnectLimit) || first.GrantID == second.GrantID || second.GrantID == third.GrantID {
		t.Fatalf("reconnect error = %v, grants = %q/%q/%q", err, first.GrantID, second.GrantID, third.GrantID)
	}
}

func TestLivePlayback_Sweep_expiresIdleSessionAndEmitsEnd(t *testing.T) {
	// Given a live session and an observer recording lifecycle termination.
	observer := &livePlaybackObserver{}
	now := time.Unix(100, 0).UTC()
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}}), ProxyOrigin: "https://silo.example", Now: func() time.Time { return now }, IdleTimeout: time.Minute, Observer: observer, Authority: liveTestAuthority{}})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}

	// When the abandoned-session policy runs after the idle timeout.
	removed := service.Sweep(now.Add(2 * time.Minute))

	// Then the opaque grant is gone and lifecycle telemetry records abandonment.
	if removed != 1 || observer.ended != "abandoned" {
		t.Fatalf("removed = %d, end reason = %q", removed, observer.ended)
	}
	if _, ok := service.Lookup(grant.GrantID); ok {
		t.Fatal("expired grant remains active")
	}
}

type liveTestResolver struct{ IP net.IP }

func (r liveTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	if r.IP == nil {
		r.IP = net.ParseIP("198.51.100.2")
	}
	return []net.IPAddr{{IP: r.IP}}, nil
}

func liveDialer(address string) DialContextFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

type livePlaybackObserver struct{ ended string }

func (*livePlaybackObserver) Started(LivePlaybackSession)                  {}
func (*livePlaybackObserver) Activity(LivePlaybackSession)                 {}
func (o *livePlaybackObserver) Ended(_ LivePlaybackSession, reason string) { o.ended = reason }

type liveTestAuthority struct{ StreamURL string }

func (a liveTestAuthority) ResolveLiveChannel(_ context.Context, libraryID int, stableID SourceQualifiedID) (Channel, error) {
	if libraryID != 4 || stableID != "fixture|channel-1" {
		return Channel{}, ErrLivePlaybackUnavailable
	}
	streamURL := a.StreamURL
	if streamURL == "" {
		streamURL = "http://upstream.example:8080/live.m3u8"
	}
	return Channel{ID: 9, LibraryID: 4, SourceID: 2, StableID: stableID, StreamURL: streamURL}, nil
}
