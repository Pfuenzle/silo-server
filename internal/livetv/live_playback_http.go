package livetv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func (s *LivePlaybackService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	grantID, endpoint, resource, ok := parseLiveRoute(r.URL.Path)
	if !ok {
		http.Error(w, ErrLivePlaybackNotFound.Error(), http.StatusNotFound)
		return
	}
	session, err := s.authorize(r.Context(), grantID, r.Header.Get("X-Live-User-ID"), r.Header.Get("X-Live-Profile-ID"))
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, ErrLivePlaybackForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	if endpoint == "segment" && session.Mode != LivePlaybackModeHLS {
		http.Error(w, "segment is not available for direct playback", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	providerURL, err := s.resourceURL(session, resource)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, ErrSourcePolicy.Error(), http.StatusBadGateway)
		return
	}
	validated, err := s.fetch.policy.validateURL(r.Context(), s.fetch.resolver, providerURL)
	if err != nil {
		s.RevokeContext(r.Context(), grantID)
		http.Error(w, ErrSourcePolicy.Error(), http.StatusBadGateway)
		return
	}
	requestContext, releaseRequest := s.beginRequest(r.Context(), grantID)
	defer releaseRequest()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, validated.String(), nil)
	if err != nil {
		http.Error(w, ErrSourcePolicy.Error(), http.StatusBadGateway)
		return
	}
	client := *s.fetch.client
	client.Timeout = 0
	response, err := client.Do(request)
	if err != nil {
		if r.Context().Err() != nil {
			s.RevokeContext(r.Context(), grantID)
			return
		}
		http.Error(w, "Live TV provider unavailable", http.StatusBadGateway)
		s.RevokeContext(r.Context(), grantID)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		s.RevokeContext(r.Context(), grantID)
		http.Error(w, "Live TV provider unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	if endpoint == "manifest" && session.Mode == LivePlaybackModeHLS {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, s.fetch.policy.MaxBodyBytes+1))
		if readErr != nil {
			s.RevokeContext(r.Context(), grantID)
			http.Error(w, "Live TV manifest unavailable", http.StatusBadGateway)
			return
		}
		if int64(len(body)) > s.fetch.policy.MaxBodyBytes {
			s.RevokeContext(r.Context(), grantID)
			http.Error(w, ErrSourcePolicy.Error(), http.StatusBadGateway)
			return
		}
		s.mu.Lock()
		rewritten := s.rewriteLiveManifest(session, string(body), s.proxyOrigin)
		stored := *session
		s.mu.Unlock()
		if s.store != nil {
			if err := s.store.Put(r.Context(), stored); err != nil {
				s.RevokeContext(r.Context(), grantID)
				http.Error(w, "Live TV manifest unavailable", http.StatusBadGateway)
				return
			}
		}
		w.WriteHeader(response.StatusCode)
		if _, writeErr := io.WriteString(w, rewritten); writeErr != nil {
			s.RevokeContext(r.Context(), grantID)
			return
		}
		s.refreshActivity(r.Context(), grantID)
		return
	}
	w.WriteHeader(response.StatusCode)
	if _, copyErr := io.Copy(w, response.Body); copyErr != nil {
		s.RevokeContext(r.Context(), grantID)
		if r.Context().Err() == nil {
			http.Error(w, fmt.Sprintf("Live TV stream interrupted: %v", copyErr), http.StatusBadGateway)
		}
		return
	}
	s.refreshActivity(r.Context(), grantID)
}

func (s *LivePlaybackService) refreshActivity(ctx context.Context, grantID string) {
	s.mu.Lock()
	current := s.sessions[grantID]
	if current == nil {
		s.mu.Unlock()
		return
	}
	current.LastSeenAt = s.now().UTC()
	updated := *current
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.Put(ctx, updated); err != nil {
			s.RevokeContext(ctx, grantID)
			return
		}
	}
	if s.observer != nil {
		s.observer.Activity(updated)
	}
}
