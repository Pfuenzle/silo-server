package livetv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

var ErrLivePlaybackQualityUnavailable = errors.New("Live TV quality is unavailable")

type LiveQualityOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Height      int    `json:"height,omitempty"`
	BitrateKbps int    `json:"bitrate_kbps,omitempty"`
}

type LiveQualityState struct {
	Options              []LiveQualityOption `json:"options"`
	ActiveID             string              `json:"active_id,omitempty"`
	TranscodingSupported bool                `json:"transcoding_supported"`
	UnsupportedReason    string              `json:"unsupported_reason,omitempty"`
	AudioTracks          []LiveAudioTrack    `json:"audio_tracks,omitempty"`
}

type LiveAudioTrack struct {
	ID       string `json:"id"`
	Language string `json:"language,omitempty"`
	Name     string `json:"name,omitempty"`
	Default  bool   `json:"default"`
}

type liveQualityVariant struct {
	option LiveQualityOption
	url    string
}

func (s *LivePlaybackService) QualityOptions(ctx context.Context, grantID string) ([]LiveQualityOption, error) {
	session, err := s.authorize(ctx, grantID, "", "")
	if err != nil {
		return nil, err
	}
	if session.Mode != LivePlaybackModeHLS {
		return []LiveQualityOption{}, nil
	}
	body, err := s.fetchLiveMaster(ctx, session.providerURL, session.trustedSource)
	if err != nil {
		return nil, fmt.Errorf("load Live TV quality ladder: %w", err)
	}
	variants := parseLiveQualityVariants(body, session.providerURL)
	options := make([]LiveQualityOption, 0, len(variants))
	for _, variant := range variants {
		options = append(options, variant.option)
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Height < options[j].Height })
	return options, nil
}

func (s *LivePlaybackService) SelectQuality(ctx context.Context, grantID, qualityID string) (LiveQualityOption, error) {
	session, err := s.authorize(ctx, grantID, "", "")
	if err != nil {
		return LiveQualityOption{}, err
	}
	if session.Mode != LivePlaybackModeHLS {
		return LiveQualityOption{}, ErrLivePlaybackQualityUnavailable
	}
	body, err := s.fetchLiveMaster(ctx, session.providerURL, session.trustedSource)
	if err != nil {
		return LiveQualityOption{}, fmt.Errorf("load Live TV quality ladder: %w", err)
	}
	variant, ok := parseLiveQualityVariants(body, session.providerURL)[qualityID]
	if !ok {
		return LiveQualityOption{}, ErrLivePlaybackQualityUnavailable
	}
	s.mu.Lock()
	var stored LivePlaybackSession
	if current := s.sessions[grantID]; current != nil {
		current.qualitySourceURL = variant.url
		current.qualityID = variant.option.ID
		stored = cloneLivePlaybackSession(*current)
	}
	s.mu.Unlock()
	if stored.GrantID == "" {
		return LiveQualityOption{}, ErrLivePlaybackNotFound
	}
	if s.store != nil {
		if err := s.store.Put(ctx, stored); err != nil {
			return LiveQualityOption{}, fmt.Errorf("store Live TV quality selection: %w", err)
		}
	}
	return variant.option, nil
}

func (s *LivePlaybackService) QualityState(ctx context.Context, grantID string) (LiveQualityState, error) {
	session, err := s.authorize(ctx, grantID, "", "")
	if err != nil {
		return LiveQualityState{}, err
	}
	if session.Mode != LivePlaybackModeHLS {
		return LiveQualityState{Options: []LiveQualityOption{}, TranscodingSupported: false, UnsupportedReason: "server_transcoding_unavailable"}, nil
	}
	options, err := s.QualityOptions(ctx, grantID)
	if err != nil {
		return LiveQualityState{}, err
	}
	body, err := s.fetchLiveMaster(ctx, session.providerURL, session.trustedSource)
	if err != nil {
		return LiveQualityState{}, fmt.Errorf("load Live TV audio tracks: %w", err)
	}
	state := LiveQualityState{Options: options, ActiveID: session.qualityID, TranscodingSupported: false, AudioTracks: parseLiveAudioTracks(body)}
	if len(options) == 0 {
		state.UnsupportedReason = "provider_did_not_publish_quality_variants"
	}
	return state, nil
}

func parseLiveAudioTracks(body string) []LiveAudioTrack {
	tracks := make([]LiveAudioTrack, 0)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") || !strings.Contains(line, "TYPE=AUDIO") {
			continue
		}
		attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		id := attrs["GROUP-ID"]
		if id == "" {
			continue
		}
		tracks = append(tracks, LiveAudioTrack{ID: id, Language: attrs["LANGUAGE"], Name: attrs["NAME"], Default: attrs["DEFAULT"] == "YES"})
	}
	return tracks
}

func parseHLSAttributes(raw string) map[string]string {
	attrs := make(map[string]string)
	for _, field := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		attrs[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	return attrs
}

func (s *LivePlaybackService) fetchLiveMaster(ctx context.Context, sourceURL string, private bool) (string, error) {
	validated, err := s.fetch.policy.validateURLWithPrivateNetworks(ctx, s.fetch.resolver, sourceURL, private)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, validated.String(), nil)
	if err != nil {
		return "", ErrSourcePolicy
	}
	response, err := s.fetch.clientFor(private).Do(request)
	if err != nil {
		return "", sourcePolicyError("quality ladder", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("source returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, s.fetch.policy.MaxBodyBytes+1))
	if err != nil || int64(len(data)) > s.fetch.policy.MaxBodyBytes {
		return "", ErrSourcePolicy
	}
	return string(data), nil
}

func parseLiveQualityVariants(body, baseURL string) map[string]liveQualityVariant {
	variants := make(map[string]liveQualityVariant)
	scanner := bufio.NewScanner(strings.NewReader(body))
	var attrs string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attrs = strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")
			continue
		}
		if attrs == "" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		width, height, bitrate := 0, 0, 0
		for _, part := range strings.Split(attrs, ",") {
			key, value, found := strings.Cut(part, "=")
			if !found {
				continue
			}
			switch key {
			case "BANDWIDTH":
				bitrate, _ = strconv.Atoi(value)
			case "RESOLUTION":
				_, _ = fmt.Sscanf(value, "%dx%d", &width, &height)
			}
		}
		resolved, err := resolveLiveResource(baseURL, line)
		if err == nil {
			id := fmt.Sprintf("q-%d-%d", height, bitrate)
			label := "Auto"
			if height > 0 {
				label = fmt.Sprintf("%dp", height)
			}
			variants[id] = liveQualityVariant{option: LiveQualityOption{ID: id, Label: label, Height: height, BitrateKbps: bitrate / 1000}, url: resolved}
		}
		attrs = ""
	}
	return variants
}
