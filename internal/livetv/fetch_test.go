package livetv

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetch_configuredPrivateSourceReadsBoundedBody(t *testing.T) {
	// Given a configured xTeVe-style private source and a bounded response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:-1,News\nhttp://stream.invalid/news\n")
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true, MaxBodyBytes: 256},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})

	// When the configured source is fetched.
	result, err := service.Fetch(context.Background(), Source{Location: "http://10.100.0.2:" + port + "/playlist"})

	// Then the bounded provider body is returned without provider metadata.
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Body) != "#EXTM3U\n#EXTINF:-1,News\nhttp://stream.invalid/news\n" {
		t.Fatalf("body = %q", result.Body)
	}
}

func TestFetch_rejectsUnsafeSchemesAndPorts(t *testing.T) {
	// Given source locations that are not permitted HTTP(S) endpoints.
	service := NewFetchService(FetchConfig{Policy: NetworkPolicy{AllowPrivateNetworks: true}})
	for _, location := range []string{"file:///etc/passwd", "ftp://provider.example/list", "http://10.0.0.2:22/list", "http://10.0.0.2:65536/list"} {
		// When the source is fetched.
		_, err := service.Fetch(context.Background(), Source{Location: location})

		// Then it is rejected before any network request.
		if !errors.Is(err, ErrSourcePolicy) {
			t.Errorf("location %q error = %v, want ErrSourcePolicy", location, err)
		}
	}
}

func TestFetch_rejectsURLUserinfo(t *testing.T) {
	// Given configured URLs containing username-only, password, or encoded credentials.
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true},
		Resolver: fixedResolver{"provider.test": net.ParseIP("10.100.0.2")},
	})
	for _, location := range []string{
		"http://user@provider.test/list",
		"http://:secret@provider.test/list",
		"http://user:secret@provider.test/list",
		"http://user%3Asecret@provider.test/list",
	} {
		// When the source is fetched.
		_, err := service.Fetch(context.Background(), Source{Location: location})

		// Then credentials are rejected before DNS or any request.
		if !errors.Is(err, ErrSourcePolicy) {
			t.Errorf("location %q error = %v, want ErrSourcePolicy", location, err)
		}
	}
}

func TestNetworkPolicy_allowedIPClasses(t *testing.T) {
	// Given representative IPv4 and IPv6 address classes.
	policy := NetworkPolicy{AllowPrivateNetworks: true}
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "unspecified ipv4", ip: "0.0.0.0", want: false},
		{name: "unspecified ipv6", ip: "::", want: false},
		{name: "loopback ipv4", ip: "127.0.0.1", want: false},
		{name: "loopback ipv6", ip: "::1", want: false},
		{name: "link local unicast ipv4", ip: "169.254.1.1", want: false},
		{name: "link local unicast ipv6", ip: "fe80::1", want: false},
		{name: "link local multicast ipv4", ip: "224.0.0.1", want: false},
		{name: "multicast ipv4", ip: "239.1.1.1", want: false},
		{name: "multicast ipv6", ip: "ff02::1", want: false},
		{name: "private ipv4 10", ip: "10.100.0.2", want: true},
		{name: "private ipv4 172", ip: "172.16.0.2", want: true},
		{name: "private ipv4 192", ip: "192.168.1.2", want: true},
		{name: "private ipv6", ip: "fd00::2", want: true},
		{name: "ipv4 metadata", ip: "169.254.169.254", want: false},
		{name: "alternate ipv4 metadata", ip: "100.100.100.200", want: false},
		{name: "ipv6 metadata", ip: "fd00:ec2::254", want: false},
		{name: "public ipv4", ip: "198.51.100.2", want: true},
		{name: "public ipv6", ip: "2001:db8::2", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When the resolved address is checked.
			got := policy.allowedIP(net.ParseIP(tc.ip))

			// Then the address class follows the configured-source policy.
			if got != tc.want {
				t.Fatalf("allowedIP(%q) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestFetch_rejectsRedirectExhaustion(t *testing.T) {
	// Given a source that redirects forever and a small redirect budget.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true, MaxRedirects: 2},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})

	// When the redirect budget is exhausted.
	_, err := service.Fetch(context.Background(), Source{Location: "http://10.100.0.2:" + port + "/loop"})

	// Then the request stops with the typed policy error.
	if !errors.Is(err, ErrSourcePolicy) {
		t.Fatalf("error = %v, want ErrSourcePolicy", err)
	}
}

func TestFetch_rejectsLoopbackAndMetadataDestinations(t *testing.T) {
	// Given destinations commonly used for SSRF and cloud metadata access.
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true},
		Resolver: fixedResolver{"loopback.test": net.ParseIP("127.0.0.1"), "metadata.test": net.ParseIP("169.254.169.254")},
	})
	for _, location := range []string{"http://loopback.test/list", "http://metadata.test/latest"} {
		// When the source is fetched.
		_, err := service.Fetch(context.Background(), Source{Location: location})

		// Then the destination is blocked by resolved-IP policy.
		if !errors.Is(err, ErrSourcePolicy) {
			t.Errorf("location %q error = %v, want ErrSourcePolicy", location, err)
		}
	}
}

func TestFetch_rechecksResolvedIPAtConnectionTime(t *testing.T) {
	// Given a resolver that changes the configured host to loopback after URL validation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "must not reach provider")
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	resolver := &rotatingResolver{addresses: [][]net.IPAddr{{{IP: net.ParseIP("10.100.0.2")}}, {{IP: net.ParseIP("127.0.0.1")}}}}
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true},
		Resolver: resolver,
		Dialer:   remappedDialer(server.URL),
	})

	// When the source is fetched and the connection-time lookup sees the new address.
	_, err := service.Fetch(context.Background(), Source{Location: "http://xteve.test:" + port + "/playlist"})

	// Then the rebinding is rejected before the dialer reaches the provider.
	if !errors.Is(err, ErrSourcePolicy) {
		t.Fatalf("error = %v, want ErrSourcePolicy", err)
	}
}

func TestFetch_revalidatesRedirect(t *testing.T) {
	// Given a permitted configured source that redirects to loopback.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://loopback.test/private", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy: NetworkPolicy{AllowPrivateNetworks: true},
		Resolver: fixedResolver{
			"10.100.0.2":    net.ParseIP("10.100.0.2"),
			"loopback.test": net.ParseIP("127.0.0.1"),
		},
		Dialer: remappedDialer(server.URL),
	})

	// When the redirect is followed.
	_, err := service.Fetch(context.Background(), Source{Location: "http://10.100.0.2:" + port + "/playlist"})

	// Then the redirected destination is independently rejected.
	if !errors.Is(err, ErrSourcePolicy) {
		t.Fatalf("error = %v, want ErrSourcePolicy", err)
	}
}

func TestFetch_redactsCredentialsAndURLFromErrors(t *testing.T) {
	// Given a source URL containing credentials and a failing endpoint.
	service := NewFetchService(FetchConfig{Policy: NetworkPolicy{AllowPrivateNetworks: true}})
	location := "http://user:secret@example.invalid:81/private?token=hidden"

	// When the source fetch fails.
	_, err := service.Fetch(context.Background(), Source{Location: location})

	// Then diagnostics contain neither secrets nor the raw provider URL.
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), location) || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("unredacted error = %v", err)
	}
}

func TestFetch_cancelsStalledResponse(t *testing.T) {
	// Given a provider that never completes its response body.
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true, ResponseTimeout: time.Second},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})
	ctx, cancel := context.WithCancel(context.Background())

	// When the caller cancels after the provider accepts the request.
	result := make(chan error, 1)
	go func() {
		_, err := service.Fetch(ctx, Source{Location: "http://10.100.0.2:" + port + "/stream"})
		result <- err
	}()
	<-started
	cancel()

	// Then the fetch terminates with the caller's cancellation.
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFetch_rejectsOversizedBody(t *testing.T) {
	// Given a configured source whose body exceeds the service limit.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "0123456789")
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true, MaxBodyBytes: 4},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})

	// When the oversized source is fetched.
	_, err := service.Fetch(context.Background(), Source{Location: "http://10.100.0.2:" + port + "/playlist"})

	// Then the body is rejected without returning partial content.
	if !errors.Is(err, ErrSourcePolicy) {
		t.Fatalf("error = %v, want ErrSourcePolicy", err)
	}
}

func TestFetch_rejectsOversizedResponseHeaders(t *testing.T) {
	// Given a configured source with response headers over the policy limit.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Provider-Metadata", strings.Repeat("x", 128))
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	port := serverPort(t, server.URL)
	service := NewFetchService(FetchConfig{
		Policy:   NetworkPolicy{AllowPrivateNetworks: true, MaxHeaderBytes: 64},
		Resolver: fixedResolver{"10.100.0.2": net.ParseIP("10.100.0.2")},
		Dialer:   remappedDialer(server.URL),
	})

	// When the response is fetched.
	_, err := service.Fetch(context.Background(), Source{Location: "http://10.100.0.2:" + port + "/playlist"})

	// Then the header-bounded transport fails without provider details.
	if err == nil || strings.Contains(err.Error(), "Provider") {
		t.Fatalf("error = %v, want redacted header-bound failure", err)
	}
}

func serverPort(t *testing.T, rawURL string) string {
	t.Helper()
	address := strings.TrimPrefix(rawURL, "http://")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

type fixedResolver map[string]net.IP

func (r fixedResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := r[host]
	if !ok {
		return nil, &net.DNSError{Name: host, Err: "not found"}
	}
	return []net.IPAddr{{IP: ip}}, nil
}

type rotatingResolver struct {
	addresses [][]net.IPAddr
	index     int
}

func (r *rotatingResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	addresses := r.addresses[r.index]
	if r.index < len(r.addresses)-1 {
		r.index++
	}
	return addresses, nil
}

func remappedDialer(serverURL string) DialContextFunc {
	address := strings.TrimPrefix(serverURL, "http://")
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
}
