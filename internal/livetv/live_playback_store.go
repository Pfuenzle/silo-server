package livetv

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrLivePlaybackGrantIntegrity = errors.New("invalid Live TV playback grant integrity")

type LivePlaybackStore interface {
	Put(context.Context, LivePlaybackSession) error
	Get(context.Context, string) (LivePlaybackSession, bool, error)
	Delete(context.Context, string) error
}

type MemoryLivePlaybackStore struct {
	mu       sync.RWMutex
	sessions map[string]LivePlaybackSession
}

func NewMemoryLivePlaybackStore() *MemoryLivePlaybackStore {
	return &MemoryLivePlaybackStore{sessions: make(map[string]LivePlaybackSession)}
}

func (s *MemoryLivePlaybackStore) Put(_ context.Context, session LivePlaybackSession) error {
	s.mu.Lock()
	s.sessions[session.GrantID] = session
	s.mu.Unlock()
	return nil
}

func (s *MemoryLivePlaybackStore) Get(_ context.Context, grantID string) (LivePlaybackSession, bool, error) {
	s.mu.RLock()
	session, ok := s.sessions[grantID]
	s.mu.RUnlock()
	return session, ok, nil
}

func (s *MemoryLivePlaybackStore) Delete(_ context.Context, grantID string) error {
	s.mu.Lock()
	delete(s.sessions, grantID)
	s.mu.Unlock()
	return nil
}

type RedisLivePlaybackStore struct {
	client      *redis.Client
	ttl         time.Duration
	keyMaterial []byte
}

func NewRedisLivePlaybackStore(client *redis.Client, ttl time.Duration) *RedisLivePlaybackStore {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &RedisLivePlaybackStore{client: client, ttl: ttl}
}

func (s *RedisLivePlaybackStore) SetIntegrityKey(key string) {
	s.keyMaterial = []byte(key)
}

func (s *RedisLivePlaybackStore) IntegrityConfigured() bool {
	return s != nil && len(s.keyMaterial) > 0
}

func (s *RedisLivePlaybackStore) key(grantID string) string { return "silo:livetv:playback:" + grantID }

func (s *RedisLivePlaybackStore) Put(ctx context.Context, session LivePlaybackSession) error {
	if len(s.keyMaterial) == 0 {
		return ErrLivePlaybackGrantIntegrity
	}
	stored := livePlaybackStoredSession{Session: session, ProviderURL: session.providerURL, Resources: session.resources}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal Live TV playback grant: %w", err)
	}
	value := append(data, '\n')
	value = append(value, s.signature(data)...)
	if err := s.client.Set(ctx, s.key(session.GrantID), value, s.ttl).Err(); err != nil {
		return fmt.Errorf("store Live TV playback grant: %w", err)
	}
	return nil
}

func (s *RedisLivePlaybackStore) Get(ctx context.Context, grantID string) (LivePlaybackSession, bool, error) {
	data, err := s.client.Get(ctx, s.key(grantID)).Bytes()
	if err == redis.Nil {
		return LivePlaybackSession{}, false, nil
	}
	if err != nil {
		return LivePlaybackSession{}, false, fmt.Errorf("load Live TV playback grant: %w", err)
	}
	stored, err := s.decode(data)
	if err != nil {
		return LivePlaybackSession{}, false, err
	}
	return stored, true, nil
}

func (s *RedisLivePlaybackStore) decode(data []byte) (LivePlaybackSession, error) {
	parts := bytes.SplitN(data, []byte{'\n'}, 2)
	if len(parts) != 2 || len(s.keyMaterial) == 0 || !hmac.Equal(parts[1], s.signature(parts[0])) {
		return LivePlaybackSession{}, ErrLivePlaybackGrantIntegrity
	}
	var stored livePlaybackStoredSession
	if err := json.Unmarshal(parts[0], &stored); err != nil {
		return LivePlaybackSession{}, fmt.Errorf("parse Live TV playback grant: %w", err)
	}
	stored.Session.providerURL = stored.ProviderURL
	stored.Session.resources = stored.Resources
	return stored.Session, nil
}

func (s *RedisLivePlaybackStore) signature(data []byte) []byte {
	mac := hmac.New(sha256.New, s.keyMaterial)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

type livePlaybackStoredSession struct {
	Session     LivePlaybackSession `json:"session"`
	ProviderURL string              `json:"provider_url"`
	Resources   map[string]string   `json:"resources,omitempty"`
}

func (s *RedisLivePlaybackStore) Delete(ctx context.Context, grantID string) error {
	if err := s.client.Del(ctx, s.key(grantID)).Err(); err != nil {
		return fmt.Errorf("delete Live TV playback grant: %w", err)
	}
	return nil
}
