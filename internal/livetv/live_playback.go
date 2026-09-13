package livetv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrLivePlaybackNotFound        = errors.New("Live TV playback session not found")
	ErrLivePlaybackForbidden       = errors.New("Live TV playback session is not authorized")
	ErrLivePlaybackSeekUnsupported = errors.New("Live TV playback does not support seeking")
	ErrLivePlaybackExpired         = errors.New("Live TV playback session expired")
	ErrLivePlaybackUnavailable     = errors.New("Live TV playback is unavailable")
	ErrLivePlaybackReconnectLimit  = errors.New("Live TV playback reconnect limit reached")
)

type LivePlaybackIdentity struct {
	UserID               int
	ProfileID, SessionID string
}
type LivePlaybackObserver interface {
	Started(LivePlaybackSession)
	Activity(LivePlaybackSession)
	Ended(LivePlaybackSession, string)
}
type LivePlaybackSourceAuthority interface {
	ResolveLiveChannel(context.Context, int, SourceQualifiedID) (Channel, error)
}
type livePlaybackIdentityKey struct{}

func WithLivePlaybackIdentity(ctx context.Context, identity LivePlaybackIdentity) context.Context {
	return context.WithValue(ctx, livePlaybackIdentityKey{}, identity)
}

type LivePlaybackMode string

const (
	LivePlaybackModeDirect LivePlaybackMode = "direct"
	LivePlaybackModeHLS    LivePlaybackMode = "hls"
	LivePlaybackModeAuto   LivePlaybackMode = "auto"
)

type LivePlaybackRequest struct {
	UserID        int
	ProfileID     string
	LibraryID     int
	ChannelID     SourceQualifiedID
	SessionID     string
	Mode          LivePlaybackMode
	SeekSeconds   float64
	NodeReference string
}

type LivePlaybackConfig struct {
	Fetch                                    *FetchService
	ProxyOrigin                              string
	Now                                      func() time.Time
	IdleTimeout                              time.Duration
	MaxLifetime                              time.Duration
	Observer                                 LivePlaybackObserver
	Authority                                LivePlaybackSourceAuthority
	Store                                    LivePlaybackStore
	AllowPrivateNetworksForConfiguredSources bool
}

type LivePlaybackGrant struct {
	GrantID, ManifestURL string
	Mode                 LivePlaybackMode
	IsLive, Seekable     bool
}

type LivePlaybackSession struct {
	GrantID               string
	UserID                int
	ProfileID             string
	LibraryID             int
	ChannelID             int64
	SessionID             string
	Mode                  LivePlaybackMode
	IsLive, Seekable      bool
	CreatedAt, LastSeenAt time.Time
	SourceID              int64
	SourceKey             string
	NodeReference         string
	trustedSource         bool
	Reconnects            int
	mediaToken            string
	mediaTokenExpiresAt   time.Time
	resources             map[string]string
	providerURL           string
}

type livePlaybackActiveRequest struct {
	cancel context.CancelFunc
}

type LivePlaybackService struct {
	fetch                                    *FetchService
	proxyOrigin                              string
	now                                      func() time.Time
	idleTimeout, maxLifetime                 time.Duration
	observer                                 LivePlaybackObserver
	authority                                LivePlaybackSourceAuthority
	store                                    LivePlaybackStore
	allowPrivateNetworksForConfiguredSources bool
	mu                                       sync.Mutex
	sessions                                 map[string]*LivePlaybackSession
	reconnects                               map[string]int
	active                                   map[string]map[*livePlaybackActiveRequest]struct{}
	revoked                                  map[string]struct{}
}

func NewLivePlaybackService(config LivePlaybackConfig) *LivePlaybackService {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	idle := config.IdleTimeout
	if idle <= 0 {
		idle = 2 * time.Minute
	}
	lifetime := config.MaxLifetime
	if lifetime <= 0 {
		lifetime = 12 * time.Hour
	}
	return &LivePlaybackService{fetch: config.Fetch, proxyOrigin: strings.TrimRight(strings.TrimSpace(config.ProxyOrigin), "/"), now: now, idleTimeout: idle, maxLifetime: lifetime, observer: config.Observer, authority: config.Authority, store: config.Store, allowPrivateNetworksForConfiguredSources: config.AllowPrivateNetworksForConfiguredSources, sessions: make(map[string]*LivePlaybackSession), reconnects: make(map[string]int), active: make(map[string]map[*livePlaybackActiveRequest]struct{}), revoked: make(map[string]struct{})}
}

func (s *LivePlaybackService) SetProxyOrigin(origin string) {
	if s != nil {
		s.mu.Lock()
		s.proxyOrigin = strings.TrimRight(strings.TrimSpace(origin), "/")
		s.mu.Unlock()
	}
}

func (s *LivePlaybackService) ProxyOrigin() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxyOrigin
}

func (s *LivePlaybackService) Start(ctx context.Context, request LivePlaybackRequest) (LivePlaybackGrant, error) {
	if request.SeekSeconds != 0 {
		return LivePlaybackGrant{}, ErrLivePlaybackSeekUnsupported
	}
	if s == nil || s.fetch == nil || s.ProxyOrigin() == "" || s.authority == nil {
		return LivePlaybackGrant{}, ErrLivePlaybackUnavailable
	}
	if request.UserID <= 0 || strings.TrimSpace(request.ProfileID) == "" || request.LibraryID <= 0 || request.ChannelID == "" || strings.TrimSpace(request.SessionID) == "" {
		return LivePlaybackGrant{}, ErrLivePlaybackForbidden
	}
	if identity, ok := ctx.Value(livePlaybackIdentityKey{}).(LivePlaybackIdentity); ok && (identity.UserID != request.UserID || identity.ProfileID != request.ProfileID || identity.SessionID != request.SessionID) {
		return LivePlaybackGrant{}, ErrLivePlaybackForbidden
	}
	if request.Mode != LivePlaybackModeDirect && request.Mode != LivePlaybackModeHLS && request.Mode != LivePlaybackModeAuto {
		return LivePlaybackGrant{}, fmt.Errorf("unsupported Live TV playback mode %q", request.Mode)
	}
	if request.NodeReference != "" {
		return LivePlaybackGrant{}, ErrLivePlaybackUnavailable
	}
	channel, err := s.authority.ResolveLiveChannel(ctx, request.LibraryID, request.ChannelID)
	if err != nil {
		return LivePlaybackGrant{}, fmt.Errorf("resolve Live TV channel: %w", err)
	}
	if channel.LibraryID != request.LibraryID || channel.ID <= 0 || strings.TrimSpace(channel.StreamURL) == "" {
		return LivePlaybackGrant{}, ErrLivePlaybackUnavailable
	}
	validated, err := s.fetch.policy.validateURLWithPrivateNetworks(ctx, s.fetch.resolver, channel.StreamURL, s.allowPrivateNetworksForConfiguredSources)
	if err != nil {
		return LivePlaybackGrant{}, err
	}
	if request.Mode == LivePlaybackModeAuto {
		request.Mode, err = s.fetch.detectLivePlaybackMode(ctx, validated.String(), s.allowPrivateNetworksForConfiguredSources)
		if err != nil {
			return LivePlaybackGrant{}, err
		}
	}
	now := s.now().UTC()
	mediaToken, err := newLiveMediaToken()
	if err != nil {
		return LivePlaybackGrant{}, fmt.Errorf("create Live TV media binding: %w", err)
	}
	session := &LivePlaybackSession{GrantID: uuid.NewString(), UserID: request.UserID, ProfileID: request.ProfileID, LibraryID: request.LibraryID, ChannelID: channel.ID, SessionID: request.SessionID, Mode: request.Mode, IsLive: true, CreatedAt: now, LastSeenAt: now, SourceID: channel.SourceID, SourceKey: string(channel.StableID), trustedSource: s.allowPrivateNetworksForConfiguredSources, mediaToken: mediaToken, mediaTokenExpiresAt: now.Add(10 * time.Minute), providerURL: validated.String(), resources: make(map[string]string)}
	s.mu.Lock()
	s.sessions[session.GrantID] = session
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.Put(ctx, *session); err != nil {
			s.RevokeContext(ctx, session.GrantID)
			return LivePlaybackGrant{}, fmt.Errorf("store Live TV playback grant: %w", err)
		}
		s.mu.Lock()
		wasRevoked := false
		if _, ok := s.revoked[session.GrantID]; ok {
			wasRevoked = true
			delete(s.revoked, session.GrantID)
		}
		s.mu.Unlock()
		if wasRevoked {
			_ = s.store.Delete(ctx, session.GrantID)
			return LivePlaybackGrant{}, ErrLivePlaybackForbidden
		}
	}
	if s.observer != nil {
		s.observer.Started(*session)
	}
	return LivePlaybackGrant{GrantID: session.GrantID, ManifestURL: mediaURL(s.ProxyOrigin(), session.GrantID, "manifest", "", mediaToken), Mode: session.Mode, IsLive: true}, nil
}

func (s *LivePlaybackService) MediaTokenIdentity(ctx context.Context, grantID, token string) (LivePlaybackIdentity, error) {
	session, err := s.authorize(ctx, grantID, "", "", token)
	if err != nil {
		return LivePlaybackIdentity{}, err
	}
	return LivePlaybackIdentity{UserID: session.UserID, ProfileID: session.ProfileID, SessionID: session.SessionID}, nil
}

func newLiveMediaToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
