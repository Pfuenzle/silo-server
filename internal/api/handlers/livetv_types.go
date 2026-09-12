package handlers

import (
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/livetv"
)

const (
	liveTVDefaultPageSize = 50
	liveTVMaxPageSize     = 200
)

type liveTVCapabilityResponse struct {
	SchemaVersion     int      `json:"schema_version"`
	Enabled           bool     `json:"enabled"`
	PlaybackAvailable bool     `json:"playback_available"`
	LibraryTypes      []string `json:"library_types"`
	Features          []string `json:"features"`
}

type liveTVSourceRequest struct {
	Kind      livetv.SourceKind `json:"kind"`
	SourceKey string            `json:"source_key"`
	Name      string            `json:"name"`
	Location  string            `json:"location"`
	Config    json.RawMessage   `json:"config,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
}

type liveTVSourceResponse struct {
	ID            int64      `json:"id"`
	LibraryID     int        `json:"library_id"`
	Kind          string     `json:"kind"`
	SourceKey     string     `json:"source_key"`
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	RefreshState  string     `json:"refresh_state"`
	RefreshError  string     `json:"refresh_error,omitempty"`
	LastRefreshAt *time.Time `json:"last_refresh_at,omitempty"`
}

type liveTVLibraryResponse struct {
	ID      int                    `json:"id"`
	Name    string                 `json:"name"`
	Type    string                 `json:"type"`
	Enabled bool                   `json:"enabled"`
	Sources []liveTVSourceResponse `json:"sources"`
}

type liveTVSourcesResponse struct {
	Items []liveTVSourceResponse `json:"items"`
	Total int                    `json:"total"`
}

type liveTVPage[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type liveTVChannelResponse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Number   string          `json:"number,omitempty"`
	Category string          `json:"category,omitempty"`
	Artwork  json.RawMessage `json:"artwork,omitempty"`
	Rating   json.RawMessage `json:"rating,omitempty"`
	Stale    bool            `json:"stale"`
}

type liveTVProgrammeResponse struct {
	ID          string          `json:"id"`
	ChannelID   string          `json:"channel_id"`
	ChannelName string          `json:"channel_name"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	StartsAt    time.Time       `json:"starts_at"`
	EndsAt      time.Time       `json:"ends_at"`
	Artwork     json.RawMessage `json:"artwork,omitempty"`
	Rating      json.RawMessage `json:"rating,omitempty"`
}

type liveTVGuideResponse struct {
	LibraryID    int                       `json:"library_id"`
	From         time.Time                 `json:"from"`
	To           time.Time                 `json:"to"`
	Stale        bool                      `json:"stale"`
	RefreshError string                    `json:"refresh_error,omitempty"`
	Items        []liveTVProgrammeResponse `json:"items"`
}

type liveTVPlaybackResponse struct {
	ChannelID string `json:"channel_id"`
	Live      bool   `json:"live"`
	Playable  bool   `json:"playable"`
	GrantID   string `json:"grant_id,omitempty"`
	URL       string `json:"url,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}
