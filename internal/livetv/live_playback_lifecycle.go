package livetv

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const livePlaybackSweepInterval = time.Minute
const livePlaybackReconnectLimit = 3

func reconnectKey(request LivePlaybackRequest) string {
	return fmt.Sprintf("%d:%s:%d:%s", request.UserID, request.ProfileID, request.LibraryID, request.ChannelID)
}

func (s *LivePlaybackService) StartSweeper(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(livePlaybackSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.Sweep(now)
			}
		}
	}()
}

func (s *LivePlaybackService) Reconnect(ctx context.Context, request LivePlaybackRequest) (LivePlaybackGrant, error) {
	if request.SessionID == "" || s == nil {
		return LivePlaybackGrant{}, ErrLivePlaybackForbidden
	}
	key := reconnectKey(request)
	s.mu.Lock()
	if s.reconnects[key] >= livePlaybackReconnectLimit {
		s.mu.Unlock()
		return LivePlaybackGrant{}, ErrLivePlaybackReconnectLimit
	}
	s.reconnects[key]++
	count := s.reconnects[key]
	s.mu.Unlock()
	grant, err := s.Start(ctx, request)
	if err != nil {
		s.mu.Lock()
		s.reconnects[key]--
		if s.reconnects[key] == 0 {
			delete(s.reconnects, key)
		}
		s.mu.Unlock()
		return LivePlaybackGrant{}, fmt.Errorf("reconnect Live TV playback: %w", err)
	}
	s.mu.Lock()
	if session := s.sessions[grant.GrantID]; session != nil {
		session.Reconnects = count
	}
	s.mu.Unlock()
	return grant, nil
}

func (s *LivePlaybackService) Lookup(grantID string) (LivePlaybackSession, bool) {
	s.mu.Lock()
	session, ok := s.sessions[grantID]
	if !ok {
		s.mu.Unlock()
		return LivePlaybackSession{}, false
	}
	copy := *session
	s.mu.Unlock()
	copy.providerURL = ""
	return copy, true
}

func (s *LivePlaybackService) authorize(ctx context.Context, grantID, userID, profileID string) (*LivePlaybackSession, error) {
	s.mu.Lock()
	if _, revoked := s.revoked[grantID]; revoked {
		s.mu.Unlock()
		return nil, ErrLivePlaybackForbidden
	}
	session, ok := s.sessions[grantID]
	if !ok {
		s.mu.Unlock()
		if s.store == nil {
			return nil, ErrLivePlaybackNotFound
		}
		stored, found, err := s.store.Get(ctx, grantID)
		if err != nil {
			return nil, fmt.Errorf("load Live TV playback grant: %w", err)
		}
		if !found {
			return nil, ErrLivePlaybackNotFound
		}
		s.mu.Lock()
		s.sessions[grantID] = &stored
		session = &stored
	}
	now := s.now().UTC()
	if now.Sub(session.CreatedAt) >= s.maxLifetime || now.Sub(session.LastSeenAt) >= s.idleTimeout {
		delete(s.sessions, grantID)
		s.mu.Unlock()
		if s.store != nil {
			_ = s.store.Delete(ctx, grantID)
		}
		if s.observer != nil {
			s.observer.Ended(*session, "expired")
		}
		return nil, ErrLivePlaybackExpired
	}
	if identity, ok := ctx.Value(livePlaybackIdentityKey{}).(LivePlaybackIdentity); ok {
		userID, profileID = fmt.Sprintf("%d", identity.UserID), identity.ProfileID
		if identity.SessionID != session.SessionID {
			s.mu.Unlock()
			return nil, ErrLivePlaybackForbidden
		}
	}
	if strings.TrimSpace(userID) != fmt.Sprintf("%d", session.UserID) || strings.TrimSpace(profileID) != session.ProfileID {
		s.mu.Unlock()
		return nil, ErrLivePlaybackForbidden
	}
	if s.authority == nil {
		s.mu.Unlock()
		return nil, ErrLivePlaybackUnavailable
	}
	channel, err := s.authority.ResolveLiveChannel(ctx, session.LibraryID, SourceQualifiedID(session.SourceKey))
	if err != nil || channel.ID != session.ChannelID || channel.LibraryID != session.LibraryID || channel.SourceID != session.SourceID || strings.TrimSpace(channel.StreamURL) == "" {
		s.mu.Unlock()
		return nil, ErrLivePlaybackUnavailable
	}
	validated, err := s.fetch.policy.validateURL(ctx, s.fetch.resolver, channel.StreamURL)
	if err != nil {
		s.mu.Unlock()
		return nil, ErrLivePlaybackUnavailable
	}
	session.providerURL = validated.String()
	session.LastSeenAt = now
	copy := *session
	s.mu.Unlock()
	if s.store != nil {
		s.mu.Lock()
		_, revoked := s.revoked[grantID]
		s.mu.Unlock()
		if revoked {
			return nil, ErrLivePlaybackForbidden
		}
		if err := s.store.Put(ctx, copy); err != nil {
			return nil, fmt.Errorf("refresh Live TV playback grant: %w", err)
		}
		s.mu.Lock()
		_, revoked = s.revoked[grantID]
		s.mu.Unlock()
		if revoked {
			_ = s.store.Delete(ctx, grantID)
			return nil, ErrLivePlaybackForbidden
		}
	}
	if s.observer != nil {
		s.observer.Activity(copy)
	}
	return &copy, nil
}

func (s *LivePlaybackService) Revoke(grantID string) { s.RevokeContext(context.Background(), grantID) }
func (s *LivePlaybackService) RevokeContext(ctx context.Context, grantID string) {
	s.mu.Lock()
	session, ok := s.sessions[grantID]
	s.revoked[grantID] = struct{}{}
	delete(s.sessions, grantID)
	requests := s.active[grantID]
	delete(s.active, grantID)
	s.mu.Unlock()
	for request := range requests {
		request.cancel()
	}
	if s.store != nil {
		_ = s.store.Delete(ctx, grantID)
	}
	if ok && s.observer != nil {
		s.observer.Ended(*session, "revoked")
	}
}

func (s *LivePlaybackService) beginRequest(parent context.Context, grantID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	request := &livePlaybackActiveRequest{cancel: cancel}
	s.mu.Lock()
	if s.active[grantID] == nil {
		s.active[grantID] = make(map[*livePlaybackActiveRequest]struct{})
	}
	s.active[grantID][request] = struct{}{}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active[grantID], request)
		if len(s.active[grantID]) == 0 {
			delete(s.active, grantID)
		}
		s.mu.Unlock()
	}
}
func (s *LivePlaybackService) Sweep(now time.Time) int {
	s.mu.Lock()
	expired := make([]LivePlaybackSession, 0)
	for id, session := range s.sessions {
		if len(s.active[id]) > 0 {
			continue
		}
		if now.Sub(session.CreatedAt) >= s.maxLifetime || now.Sub(session.LastSeenAt) >= s.idleTimeout {
			delete(s.sessions, id)
			expired = append(expired, *session)
		}
	}
	s.mu.Unlock()
	for _, session := range expired {
		if s.store != nil {
			_ = s.store.Delete(context.Background(), session.GrantID)
		}
		if s.observer != nil {
			s.observer.Ended(session, "abandoned")
		}
	}
	return len(expired)
}
