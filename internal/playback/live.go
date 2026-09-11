package playback

import (
	"errors"
	"strings"
)

var ErrInvalidLiveSourceSession = errors.New("invalid Live TV source session")

type LiveTransport string

const (
	LiveTransportDirect LiveTransport = "direct"
	LiveTransportProxy  LiveTransport = "proxy"
	LiveTransportHLS    LiveTransport = "hls"
)

type LiveSourceSession struct {
	SessionID     string
	UserID        int
	ProfileID     string
	LibraryID     int
	ChannelID     int64
	Transport     LiveTransport
	SourceGrantID string
	IsLive        bool
	Seekable      bool
}

func ValidateLiveSourceSession(session LiveSourceSession) error {
	if strings.TrimSpace(session.SessionID) == "" || session.UserID <= 0 || strings.TrimSpace(session.ProfileID) == "" || session.LibraryID <= 0 || session.ChannelID <= 0 || strings.TrimSpace(session.SourceGrantID) == "" {
		return ErrInvalidLiveSourceSession
	}
	if session.Transport != LiveTransportDirect && session.Transport != LiveTransportProxy && session.Transport != LiveTransportHLS {
		return ErrInvalidLiveSourceSession
	}
	if !session.IsLive || session.Seekable {
		return ErrInvalidLiveSourceSession
	}
	return nil
}
