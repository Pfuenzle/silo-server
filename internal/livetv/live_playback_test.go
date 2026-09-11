package livetv

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLivePlayback_Start_returnsSiloOnlyOpaqueGrant_boundToOwnership(t *testing.T) {
	// Given a configured Live TV channel and a profile-owned playback request.
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{}}),
		Now:         func() time.Time { return time.Unix(100, 0).UTC() },
		ProxyOrigin: "https://silo.example",
		Authority:   liveTestAuthority{},
	})

	// When a live session is started.
	grant, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4,
		SessionID: "session-a", Mode: LivePlaybackModeHLS,
		ChannelID: SourceQualifiedID("fixture|channel-1"),
	})

	// Then the browser receives only a Silo URL and the grant is fully bound.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(grant.ManifestURL, "upstream.example") || strings.Contains(grant.ManifestURL, "fixture") {
		t.Fatalf("manifest URL leaks provider identity: %q", grant.ManifestURL)
	}
	if grant.ManifestURL == "" || grant.GrantID == "" || grant.IsLive != true || grant.Seekable {
		t.Fatalf("grant = %#v, want opaque infinite non-seekable grant", grant)
	}
	stored, ok := service.Lookup(grant.GrantID)
	if !ok {
		t.Fatal("grant was not stored")
	}
	if stored.UserID != 7 || stored.ProfileID != "profile-a" || stored.LibraryID != 4 || stored.ChannelID != 9 || stored.SessionID != "session-a" {
		t.Fatalf("stored ownership = %#v", stored)
	}
}

func TestLivePlayback_Start_withoutProxyOrigin_isUnavailable(t *testing.T) {
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:     NewFetchService(FetchConfig{Resolver: liveTestResolver{}}),
		Authority: liveTestAuthority{},
	})

	_, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4,
		SessionID: "session-a", Mode: LivePlaybackModeHLS,
		ChannelID: SourceQualifiedID("fixture|channel-1"),
	})

	if !errors.Is(err, ErrLivePlaybackUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrLivePlaybackUnavailable)
	}
}

func TestLivePlayback_Start_withRegisteredProxyOrigin_isAvailable(t *testing.T) {
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{}}),
		ProxyOrigin: "http://proxy:8080/",
		Authority:   liveTestAuthority{},
	})

	grant, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4,
		SessionID: "session-a", Mode: LivePlaybackModeHLS,
		ChannelID: SourceQualifiedID("fixture|channel-1"),
	})

	if err != nil {
		t.Fatal(err)
	}
	if grant.ManifestURL != "http://proxy:8080/stream/live/"+grant.GrantID+"/manifest" {
		t.Fatalf("manifest URL = %q, provider origin must not be used", grant.ManifestURL)
	}
}

func TestLivePlayback_Start_rejectsContextIdentityMismatch(t *testing.T) {
	// Given an authenticated profile context and a request for another profile.
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}})
	ctx := WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"})

	// When the mismatched request attempts to create a live grant.
	_, err := service.Start(ctx, LivePlaybackRequest{UserID: 8, ProfileID: "profile-b", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})

	// Then authorization fails before source validation or any upstream access.
	if !errors.Is(err, ErrLivePlaybackForbidden) {
		t.Fatalf("error = %v, want %v", err, ErrLivePlaybackForbidden)
	}
}

func TestLivePlayback_ServeHTTP_rejectsSameOwnerDifferentSession(t *testing.T) {
	// Given a grant bound to one authenticated login session.
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}})
	grant, err := service.Start(WithLivePlaybackIdentity(context.Background(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-a"}), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}

	// When the same profile presents a different login session.
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	ctx := WithLivePlaybackIdentity(req.Context(), LivePlaybackIdentity{UserID: 7, ProfileID: "profile-a", SessionID: "session-b"})
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, req.WithContext(ctx))

	// Then the proxy refuses access before contacting the provider.
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestLivePlayback_ServeHLS_rewritesManifestAndRejectsUnauthorizedBeforeUpstream(t *testing.T) {
	// Given a deterministic upstream HLS manifest and a valid Live TV grant.
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/live.m3u8" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:2,News\nsegment-1.ts\n")
	}))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{IP: net.ParseIP("198.51.100.2")}, Dialer: liveDialer(upstream.Listener.Addr().String())}),
		ProxyOrigin: "https://silo.example",
		Authority:   liveTestAuthority{},
	})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4, SessionID: "session-a", Mode: LivePlaybackModeHLS,
		ChannelID: SourceQualifiedID("fixture|channel-1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When the owner fetches the Silo manifest, then an unrelated profile tries.
	good := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	good.Header.Set("X-Live-User-ID", "7")
	good.Header.Set("X-Live-Profile-ID", "profile-a")
	goodRR := httptest.NewRecorder()
	service.ServeHTTP(goodRR, good)
	if goodRR.Code != http.StatusOK || strings.Contains(goodRR.Body.String(), "upstream.example") {
		t.Fatalf("manifest response = %d %q", goodRR.Code, goodRR.Body.String())
	}
	if strings.Contains(goodRR.Body.String(), "segment-1.ts") || !strings.Contains(goodRR.Body.String(), "/stream/live/") {
		t.Fatalf("manifest did not rewrite the segment to an opaque Silo route: %q", goodRR.Body.String())
	}
	beforeUnauthorized := requests.Load()
	bad := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	bad.Header.Set("X-Live-User-ID", "8")
	bad.Header.Set("X-Live-Profile-ID", "profile-b")
	badRR := httptest.NewRecorder()
	service.ServeHTTP(badRR, bad)
	if badRR.Code != http.StatusForbidden || requests.Load() != beforeUnauthorized {
		t.Fatalf("unauthorized response = %d, upstream requests %d -> %d", badRR.Code, beforeUnauthorized, requests.Load())
	}
}

func TestLivePlayback_HLSRewriter_rewritesURIAttributes(t *testing.T) {
	// Given an HLS manifest with a URI-bearing initialization tag.
	service := NewLivePlaybackService(LivePlaybackConfig{})
	session := &LivePlaybackSession{GrantID: "grant", providerURL: "https://provider.example/live/index.m3u8", resources: make(map[string]string)}

	// When the server rewrites the manifest.
	rewritten := service.rewriteLiveManifest(session, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\nsegment.ts\n", "https://silo.example")

	// Then the initialization resource and segment use opaque Silo routes.
	if strings.Contains(rewritten, "init.mp4") || strings.Contains(rewritten, "segment.ts") || len(session.resources) != 2 {
		t.Fatalf("rewritten = %q, resources = %#v", rewritten, session.resources)
	}
}

func TestLivePlayback_HLSRewriter_doesNotExposeUnsupportedURI(t *testing.T) {
	// Given an HLS manifest containing an unsupported URI scheme.
	service := NewLivePlaybackService(LivePlaybackConfig{})
	session := &LivePlaybackSession{GrantID: "grant", providerURL: "https://provider.example/live/index.m3u8", resources: make(map[string]string)}

	// When the server rewrites the manifest.
	rewritten := service.rewriteLiveManifest(session, "#EXTM3U\n#EXT-X-KEY:URI=\"file:///secret\"\n", "https://silo.example")

	// Then the provider or local URI is not returned to the client.
	if strings.Contains(rewritten, "file:///secret") || strings.Contains(rewritten, "secret") {
		t.Fatalf("unsupported URI leaked: %q", rewritten)
	}
}

func TestLivePlayback_CancelAndExpire_revokeGrantAndCloseUpstream(t *testing.T) {
	// Given a provider that blocks until the request is cancelled.
	closed := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer(upstream.Listener.Addr().String())}),
		ProxyOrigin: "https://silo.example", IdleTimeout: time.Second, MaxLifetime: time.Minute,
		Authority: liveTestAuthority{StreamURL: "http://upstream.example:8080/live.ts"},
	})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4, SessionID: "session-a", Mode: LivePlaybackModeDirect,
		ChannelID: SourceQualifiedID("fixture|channel-1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When the client cancels the live request.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil).WithContext(ctx)
	req.Header.Set("X-Live-User-ID", "7")
	req.Header.Set("X-Live-Profile-ID", "profile-a")
	done := make(chan struct{})
	go func() {
		service.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not cancelled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("live request did not finish")
	}
	if _, ok := service.Lookup(grant.GrantID); ok {
		t.Fatal("cancelled live grant still active")
	}
}

func TestLivePlayback_ProviderFailure_revokesGrant(t *testing.T) {
	// Given an upstream that returns a terminal provider error.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider failed", http.StatusBadGateway)
	}))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{
		Fetch:       NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer(upstream.Listener.Addr().String())}),
		ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{StreamURL: "http://upstream.example:8080/live.ts"},
	})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}

	// When the live proxy requests the provider stream.
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	req.Header.Set("X-Live-User-ID", "7")
	req.Header.Set("X-Live-Profile-ID", "profile-a")
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, req)

	// Then the provider error is bounded and the grant is revoked.
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if _, ok := service.Lookup(grant.GrantID); ok {
		t.Fatal("provider-failed grant remains active")
	}
}

func TestLivePlayback_DirectCopyFailure_revokesGrant(t *testing.T) {
	// Given a provider stream and a downstream writer that fails during copying.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "live-bytes")
	}))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer(upstream.Listener.Addr().String())}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{StreamURL: "http://upstream.example:8080/live.ts"}})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}

	// When the live proxy copies bytes into the failing downstream writer.
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	req.Header.Set("X-Live-User-ID", "7")
	req.Header.Set("X-Live-Profile-ID", "profile-a")
	service.ServeHTTP(&failingLiveWriter{}, req)

	// Then the copy failure revokes the grant and does not leave it idle-active.
	if _, ok := service.Lookup(grant.GrantID); ok {
		t.Fatal("copy-failed grant remains active")
	}
}

func TestLivePlayback_DirectCopySuccess_refreshesActivity(t *testing.T) {
	// Given a direct stream and a clock that can observe activity refresh.
	now := time.Unix(100, 0).UTC()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "live-bytes") }))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer(upstream.Listener.Addr().String())}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{StreamURL: "http://upstream.example:8080/live.ts"}, Now: func() time.Time { return now }})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}

	// When the downstream copy succeeds.
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	req.Header.Set("X-Live-User-ID", "7")
	req.Header.Set("X-Live-Profile-ID", "profile-a")
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, req)

	// Then activity is refreshed and the stream bytes reach the downstream.
	if recorder.Code != http.StatusOK || recorder.Body.String() != "live-bytes" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if session, ok := service.Lookup(grant.GrantID); !ok || !session.LastSeenAt.Equal(now) {
		t.Fatalf("session activity = %#v, want timestamp %v", session, now)
	}
}

func TestLivePlayback_Sweep_keepsGrantWhileDirectRequestIsActive(t *testing.T) {
	// Given a direct upstream request that remains active beyond the idle window.
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "live")
	}))
	t.Cleanup(upstream.Close)
	service := NewLivePlaybackService(LivePlaybackConfig{Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}, Dialer: liveDialer(upstream.Listener.Addr().String())}), ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{StreamURL: "http://upstream.example:8080/live.ts"}, IdleTimeout: time.Second})
	grant, err := service.Start(context.Background(), LivePlaybackRequest{UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, grant.ManifestURL, nil)
	req.Header.Set("X-Live-User-ID", "7")
	req.Header.Set("X-Live-Profile-ID", "profile-a")
	done := make(chan struct{})
	go func() { service.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}

	// When the abandonment sweep runs while the upstream request is active.
	removed := service.Sweep(time.Now().Add(time.Hour))

	// Then the active grant is retained until the downstream request finishes.
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if _, ok := service.Lookup(grant.GrantID); !ok {
		t.Fatal("active grant was swept")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("direct request did not finish")
	}
}

type failingLiveWriter struct{}

func (*failingLiveWriter) Header() http.Header       { return make(http.Header) }
func (*failingLiveWriter) WriteHeader(int)           {}
func (*failingLiveWriter) Write([]byte) (int, error) { return 0, errors.New("downstream copy failed") }

func TestLivePlayback_Start_rejectsSeekAndInvalidNodeReference(t *testing.T) {
	// Given a live request attempting finite/VOD behavior or an unvalidated node.
	service := NewLivePlaybackService(LivePlaybackConfig{ProxyOrigin: "https://silo.example", Authority: liveTestAuthority{}, Fetch: NewFetchService(FetchConfig{Resolver: liveTestResolver{}})})
	_, err := service.Start(context.Background(), LivePlaybackRequest{
		UserID: 7, ProfileID: "profile-a", LibraryID: 4, ChannelID: SourceQualifiedID("fixture|channel-1"), SessionID: "session-a", Mode: LivePlaybackModeDirect,
		SeekSeconds: 1,
	})
	if !errors.Is(err, ErrLivePlaybackSeekUnsupported) {
		t.Fatalf("seek error = %v, want %v", err, ErrLivePlaybackSeekUnsupported)
	}
}
