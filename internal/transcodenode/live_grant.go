package transcodenode

import (
	"errors"
	"strings"
)

var ErrInvalidLiveSourceGrant = errors.New("invalid Live TV source grant")

type LiveSourceGrant struct {
	GrantID         string `json:"grant_id"`
	SessionID       string `json:"session_id"`
	UserID          int    `json:"user_id"`
	ProfileID       string `json:"profile_id"`
	LibraryID       int    `json:"library_id"`
	ChannelID       int64  `json:"channel_id"`
	SourceReference string `json:"source_reference"`
}

func ValidateLiveSourceGrant(grant LiveSourceGrant) error {
	if strings.TrimSpace(grant.GrantID) == "" || strings.TrimSpace(grant.SessionID) == "" || grant.UserID <= 0 || strings.TrimSpace(grant.ProfileID) == "" || grant.LibraryID <= 0 || grant.ChannelID <= 0 {
		return ErrInvalidLiveSourceGrant
	}
	reference := strings.TrimSpace(grant.SourceReference)
	if reference == "" || len(reference) > 128 || strings.ContainsAny(reference, ":/?#\\\x00\r\n") {
		return ErrInvalidLiveSourceGrant
	}
	return nil
}
