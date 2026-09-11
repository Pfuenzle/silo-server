package playback

import (
	"errors"
	"testing"
)

func TestValidateLiveSourceSession_requiresInfiniteNonSeekableOwnershipBoundSession(t *testing.T) {
	// Given a v3 live session with explicit ownership and an opaque source grant.
	session := LiveSourceSession{SessionID: "session", UserID: 7, ProfileID: "profile", LibraryID: 4, ChannelID: 9, Transport: LiveTransportHLS, SourceGrantID: "grant", IsLive: true, Seekable: false}

	// When playback validates the session contract.
	err := ValidateLiveSourceSession(session)

	// Then the explicit infinite live contract is accepted.
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateLiveSourceSession_rejectsFiniteOrSeekableSession(t *testing.T) {
	// Given a session that attempts to use finite VOD or seek semantics.
	session := LiveSourceSession{SessionID: "session", UserID: 7, ProfileID: "profile", LibraryID: 4, ChannelID: 9, Transport: LiveTransportDirect, SourceGrantID: "grant", IsLive: false, Seekable: true}

	// When playback validates the session contract.
	err := ValidateLiveSourceSession(session)

	// Then the finite/seekable session is rejected.
	if !errors.Is(err, ErrInvalidLiveSourceSession) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidLiveSourceSession)
	}
}
