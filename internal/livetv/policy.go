package livetv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrSourcePolicy = errors.New("Live TV source violates network policy")

type NetworkPolicy struct {
	AllowPrivateNetworks bool
	MaxBodyBytes         int64
	MaxHeaderBytes       int64
	ResponseTimeout      time.Duration
	MaxRedirects         int
}

type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

func (p NetworkPolicy) withDefaults() NetworkPolicy {
	if p.MaxBodyBytes <= 0 {
		p.MaxBodyBytes = 8 << 20
	}
	if p.MaxHeaderBytes <= 0 {
		p.MaxHeaderBytes = 64 << 10
	}
	if p.ResponseTimeout <= 0 {
		p.ResponseTimeout = 30 * time.Second
	}
	if p.MaxRedirects <= 0 {
		p.MaxRedirects = 5
	}
	return p
}

func (p NetworkPolicy) validateURL(ctx context.Context, resolver IPResolver, raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, ErrSourcePolicy
	}
	if u.User != nil {
		return nil, ErrSourcePolicy
	}
	port, err := endpointPort(u)
	if err != nil || !allowedPort(port) {
		return nil, ErrSourcePolicy
	}
	ipAddrs, err := resolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ipAddrs) == 0 {
		return nil, ErrSourcePolicy
	}
	for _, ipAddr := range ipAddrs {
		if !p.allowedIP(ipAddr.IP) {
			return nil, ErrSourcePolicy
		}
	}
	return u, nil
}

func endpointPort(u *url.URL) (int, error) {
	if u.Port() != "" {
		return strconv.Atoi(u.Port())
	}
	if u.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

func allowedPort(port int) bool { return port == 80 || port == 443 || port >= 1024 && port <= 65535 }

func (p NetworkPolicy) allowedIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || isMetadataIP(ip) {
		return false
	}
	if ip.IsPrivate() && !p.AllowPrivateNetworks {
		return false
	}
	return true
}

func isMetadataIP(ip net.IP) bool {
	return ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

func sourcePolicyError(op string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrSourcePolicy) {
		return err
	}
	return fmt.Errorf("Live TV source %s failed: %w", op, ErrSourcePolicy)
}
