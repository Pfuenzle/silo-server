package livetv

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type FetchConfig struct {
	Policy   NetworkPolicy
	Resolver IPResolver
	Dialer   DialContextFunc
}

type FetchService struct {
	policy   NetworkPolicy
	resolver IPResolver
	client   *http.Client
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = policy.MaxHeaderBytes
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrSourcePolicy
		}
		ip, err := resolveAllowedIP(ctx, policy, resolver, host, port)
		if err != nil {
			return nil, err
		}
		return dialer(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	service := &FetchService{policy: policy, resolver: resolver}
	service.client = &http.Client{
		Transport: transport,
		Timeout:   policy.ResponseTimeout,
		CheckRedirect: func(req *http.Request, history []*http.Request) error {
			if len(history) >= policy.MaxRedirects {
				return ErrSourcePolicy
			}
			_, err := policy.validateURL(req.Context(), resolver, req.URL.String())
			return err
		},
	}
	return service
}

func (s *FetchService) Fetch(ctx context.Context, source Source) (FetchResult, error) {
	if s == nil || s.client == nil {
		return FetchResult{}, ErrSourcePolicy
	}
	u, err := s.policy.validateURL(ctx, s.resolver, source.Location)
	if err != nil {
		return FetchResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return FetchResult{}, ErrSourcePolicy
	}
	request.Header.Set("Accept", "application/x-mpegurl, application/xml, text/xml, text/plain")
	response, err := s.client.Do(request)
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

func resolveAllowedIP(ctx context.Context, policy NetworkPolicy, resolver IPResolver, host, port string) (net.IP, error) {
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil || !allowedPort(portNumber) {
		return nil, ErrSourcePolicy
	}
	ipAddrs, err := resolver.LookupIPAddr(ctx, strings.Trim(host, "[]"))
	if err != nil || len(ipAddrs) == 0 {
		return nil, ErrSourcePolicy
	}
	for _, ipAddr := range ipAddrs {
		if !policy.allowedIP(ipAddr.IP) {
			continue
		}
		return ipAddr.IP, nil
	}
	return nil, ErrSourcePolicy
}
