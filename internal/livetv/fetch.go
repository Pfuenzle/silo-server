package livetv

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *FetchService) detectLivePlaybackMode(ctx context.Context, rawURL string, allowPrivateNetworks bool) (LivePlaybackMode, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u, err := s.policy.validateURLWithPrivateNetworks(probeCtx, s.resolver, rawURL, allowPrivateNetworks)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", ErrSourcePolicy
	}
	response, err := s.clientFor(allowPrivateNetworks).Do(request)
	if err != nil {
		return "", sourcePolicyError("playback probe", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Live TV source returned status %d", response.StatusCode)
	}
	prefix, err := bufio.NewReader(io.LimitReader(response.Body, 512)).Peek(8)
	if err != nil && len(prefix) == 0 {
		return "", sourcePolicyError("playback probe", err)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if contentType == "application/vnd.apple.mpegurl" || contentType == "application/x-mpegurl" || strings.HasPrefix(string(prefix), "#EXTM3U") {
		return LivePlaybackModeHLS, nil
	}
	if contentType == "video/mp2t" || contentType == "application/octet-stream" || (len(prefix) > 0 && prefix[0] == 0x47) {
		return LivePlaybackModeDirect, nil
	}
	return "", fmt.Errorf("unsupported Live TV stream content type %q", contentType)
}

type FetchConfig struct {
	Policy   NetworkPolicy
	Resolver IPResolver
	Dialer   DialContextFunc
}

type FetchService struct {
	policy   NetworkPolicy
	resolver IPResolver
	client   *http.Client
	dialer   DialContextFunc
}

type FetchResult struct {
	Body        []byte
	StatusCode  int
	ContentType string
}

func NewFetchService(config FetchConfig) *FetchService {
	policy := config.Policy.withDefaults()
	resolver := config.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := config.Dialer
	if dialer == nil {
		dialer = (&net.Dialer{Timeout: policy.ResponseTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	service := &FetchService{policy: policy, resolver: resolver, dialer: dialer}
	service.client = service.clientFor(policy.AllowPrivateNetworks)
	return service
}

func (s *FetchService) clientFor(allowPrivateNetworks bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = s.policy.MaxHeaderBytes
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrSourcePolicy
		}
		ip, err := resolveAllowedIP(ctx, s.policy, s.resolver, host, port, allowPrivateNetworks)
		if err != nil {
			return nil, err
		}
		return s.dialer(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return &http.Client{
		Transport: transport,
		Timeout:   s.policy.ResponseTimeout,
		CheckRedirect: func(req *http.Request, history []*http.Request) error {
			if len(history) >= s.policy.MaxRedirects {
				return ErrSourcePolicy
			}
			_, err := s.policy.validateURLWithPrivateNetworks(req.Context(), s.resolver, req.URL.String(), allowPrivateNetworks)
			return err
		},
	}
}

func (s *FetchService) Fetch(ctx context.Context, source Source) (FetchResult, error) {
	return s.fetch(ctx, source.Location, s.policy.AllowPrivateNetworks)
}

func (s *FetchService) FetchConfiguredSource(ctx context.Context, source Source) (FetchResult, error) {
	return s.fetch(ctx, source.Location, true)
}

func (s *FetchService) fetch(ctx context.Context, rawURL string, allowPrivateNetworks bool) (FetchResult, error) {
	if s == nil || s.client == nil {
		return FetchResult{}, ErrSourcePolicy
	}
	u, err := s.policy.validateURLWithPrivateNetworks(ctx, s.resolver, rawURL, allowPrivateNetworks)
	if err != nil {
		return FetchResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return FetchResult{}, ErrSourcePolicy
	}
	request.Header.Set("Accept", "application/x-mpegurl, application/xml, text/xml, text/plain")
	response, err := s.clientFor(allowPrivateNetworks).Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return FetchResult{}, ctx.Err()
		}
		return FetchResult{}, sourcePolicyError("request", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return FetchResult{}, fmt.Errorf("Live TV source returned status %d", response.StatusCode)
	}
	if response.ContentLength > s.policy.MaxBodyBytes {
		return FetchResult{}, ErrSourcePolicy
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, s.policy.MaxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return FetchResult{}, ctx.Err()
		}
		return FetchResult{}, sourcePolicyError("body read", err)
	}
	if int64(len(body)) > s.policy.MaxBodyBytes {
		return FetchResult{}, ErrSourcePolicy
	}
	return FetchResult{Body: body, StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type")}, nil
}

func resolveAllowedIP(ctx context.Context, policy NetworkPolicy, resolver IPResolver, host, port string, allowPrivateNetworks bool) (net.IP, error) {
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil || !allowedPort(portNumber) {
		return nil, ErrSourcePolicy
	}
	ipAddrs, err := resolver.LookupIPAddr(ctx, strings.Trim(host, "[]"))
	if err != nil || len(ipAddrs) == 0 {
		return nil, ErrSourcePolicy
	}
	for _, ipAddr := range ipAddrs {
		if !policy.allowedIPWithPrivateNetworks(ipAddr.IP, allowPrivateNetworks) {
			continue
		}
		return ipAddr.IP, nil
	}
	return nil, ErrSourcePolicy
}
