package livetv

import (
	"errors"
	"testing"
)

func TestNewSourceQualifiedID_isStableAndSourceQualified(t *testing.T) {
	// Given the same external identifier from two source namespaces.
	left, err := NewSourceQualifiedID("playlist-a", "news-1")
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewSourceQualifiedID("playlist-b", "news-1")
	if err != nil {
		t.Fatal(err)
	}

	// When their canonical identities are compared.
	// Then source namespaces remain distinct.
	if left == right || left.String() != "playlist-a|news-1" {
		t.Fatalf("identities = %q and %q", left, right)
	}
}

func TestNewSourceQualifiedID_rejectsMalformedInput(t *testing.T) {
	// Given an empty or control-character source-qualified identity.
	for _, tc := range []struct {
		name       string
		sourceKey  string
		externalID string
	}{
		{name: "empty source", externalID: "channel"},
		{name: "empty external id", sourceKey: "source"},
		{name: "newline", sourceKey: "source\n", externalID: "channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When the identity is parsed.
			_, err := NewSourceQualifiedID(tc.sourceKey, tc.externalID)

			// Then malformed input is rejected by the typed sentinel.
			if !errors.Is(err, ErrInvalidSourceID) {
				t.Fatalf("error = %v, want ErrInvalidSourceID", err)
			}
		})
	}
}
