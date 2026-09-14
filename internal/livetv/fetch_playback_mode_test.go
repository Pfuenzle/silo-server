package livetv

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch_detectLivePlaybackMode_classifiesMPEGTSFromContentTypeBeforeBody(t *testing.T) {
	// Given a provider that declares MPEG-TS but delays its first packet.
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	service := NewFetchService(FetchConfig{
		Resolver: fixedResolver{"provider.test": net.ParseIP("198.51.100.2")},
		Dialer:   remappedDialer(server.URL),
	})
	port := serverPort(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan struct {
		mode LivePlaybackMode
		err  error
	}, 1)
	go func() {
		mode, err := service.detectLivePlaybackMode(ctx, "http://provider.test:"+port+"/live.ts", false)
		result <- struct {
			mode LivePlaybackMode
			err  error
		}{mode: mode, err: err}
	}()
	<-started

	// Then a declared MPEG-TS response is classified without waiting for bytes.
	got := <-result
	if got.err != nil || got.mode != LivePlaybackModeDirect {
		t.Fatalf("mode = %q, error = %v, want direct without body wait", got.mode, got.err)
	}
	cancel()
}
