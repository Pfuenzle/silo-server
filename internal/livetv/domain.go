package livetv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidSourceKind    = errors.New("invalid Live TV source kind")
	ErrInvalidSourceID      = errors.New("invalid Live TV source-qualified ID")
	ErrRuntimeNotConfigured = errors.New("Live TV runtime is not configured")
	ErrInvalidFavoriteKind  = errors.New("invalid Live TV favorite kind")
)

type SourceKind string

const (
	SourceKindPlaylist SourceKind = "playlist"
	SourceKindEPG      SourceKind = "epg"
)

func (k SourceKind) Valid() bool { return k == SourceKindPlaylist || k == SourceKindEPG }

type SourceQualifiedID string

type FavoriteKind string

const (
	FavoriteKindChannel   FavoriteKind = "channel"
	FavoriteKindProgramme FavoriteKind = "programme"
)

func (k FavoriteKind) Valid() bool {
	return k == FavoriteKindChannel || k == FavoriteKindProgramme
}

type Favorite struct {
	UserID    int
	ProfileID string
	LibraryID int
	Kind      FavoriteKind
	StableID  SourceQualifiedID
	AddedAt   time.Time
}

type ChannelCategory string

func NewSourceQualifiedID(sourceKey, externalID string) (SourceQualifiedID, error) {
	if strings.ContainsAny(sourceKey+externalID, "\x00\r\n|") {
		return "", ErrInvalidSourceID
	}
	sourceKey = strings.TrimSpace(sourceKey)
	externalID = strings.TrimSpace(externalID)
	if sourceKey == "" || externalID == "" {
		return "", ErrInvalidSourceID
	}
	return SourceQualifiedID(sourceKey + "|" + externalID), nil
}

func (id SourceQualifiedID) String() string { return string(id) }

type Source struct {
	ID            int64
	LibraryID     int
	Kind          SourceKind
	SourceKey     string
	Name          string
	Location      string
	Config        []byte
	Enabled       bool
	LastRefreshAt *time.Time
	RefreshState  string
	RefreshError  string
}

// ArtworkCacher stores provider artwork and returns an internal object path.
// Implementations must reject unsafe provider URLs and never return them.
type ArtworkCacher interface {
	CacheLiveTVArtwork(context.Context, string, string, string) (string, error)
}

type Channel struct {
	ID         int64
	LibraryID  int
	SourceID   int64
	ExternalID string
	StableID   SourceQualifiedID
	Name       string
	Number     string
	Category   ChannelCategory
	StreamURL  string
	Artwork    []byte
	Rating     []byte
}

type Programme struct {
	ID          int64
	LibraryID   int
	SourceID    int64
	ChannelID   int64
	ExternalID  string
	StableID    SourceQualifiedID
	Title       string
	Description string
	StartsAt    time.Time
	EndsAt      time.Time
	Artwork     []byte
	Rating      []byte
}

type ChannelEPGMapping struct {
	LibraryID    int
	ChannelID    int64
	EPGSourceID  int64
	EPGChannelID string
}

func ValidateSource(kind SourceKind, sourceKey, name, location string) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidSourceKind, kind)
	}
	if _, err := NewSourceQualifiedID(sourceKey, name); err != nil {
		return err
	}
	if strings.TrimSpace(location) == "" {
		return ErrInvalidSourceID
	}
	return nil
}
