package transcodenode

import (
	"errors"
	"testing"
)

func TestValidateLiveSourceGrant_rejectsProviderAndFilesystemReferences(t *testing.T) {
	// Given a grant with a source reference that could be interpreted as a URL or path.
	grant := LiveSourceGrant{GrantID: "grant", SessionID: "session", UserID: 7, ProfileID: "profile", LibraryID: 4, ChannelID: 9, SourceReference: "https://provider.example/live.m3u8"}

	// When the transcode node validates the grant.
	err := ValidateLiveSourceGrant(grant)

	// Then the node rejects it before any executor can receive provider data.
	if !errors.Is(err, ErrInvalidLiveSourceGrant) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidLiveSourceGrant)
	}
}

func TestValidateLiveSourceGrant_acceptsOpaqueBoundReference(t *testing.T) {
	// Given a grant whose source reference is opaque and fully ownership-bound.
	grant := LiveSourceGrant{GrantID: "grant", SessionID: "session", UserID: 7, ProfileID: "profile", LibraryID: 4, ChannelID: 9, SourceReference: "node-source-1"}

	// When the transcode node validates the grant.
	err := ValidateLiveSourceGrant(grant)

	// Then the grant is accepted for node-local source resolution.
	if err != nil {
		t.Fatal(err)
	}
}
