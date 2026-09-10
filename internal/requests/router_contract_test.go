package requests

import (
	"encoding/json"
	"testing"
)

func TestRouterMetadataRoundTripsOptionalFields(t *testing.T) {
	// Given a router target with all optional fulfillment metadata
	target := RouterTarget{
		Metadata: &RouterMetadata{
			ExternalCorrelationID: "external-download-42",
			StatusText:            "Importing audiobook",
			ExternalURL:           "https://router.example/downloads/42",
			LibraryURL:            "/api/v1/items/book-42",
			ImportedPath:          "/mnt/media/audiobooks/Author/Title",
			ScanLinkState:         ScanLinkStateScanning,
		},
	}

	// When the target is serialized and decoded again
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	var decoded RouterTarget
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal target: %v", err)
	}

	// Then every optional field remains typed and intact
	if decoded.Metadata == nil || decoded.Metadata.ExternalCorrelationID != "external-download-42" ||
		decoded.Metadata.StatusText != "Importing audiobook" || decoded.Metadata.ExternalURL != "https://router.example/downloads/42" ||
		decoded.Metadata.LibraryURL != "/api/v1/items/book-42" || decoded.Metadata.ImportedPath != "/mnt/media/audiobooks/Author/Title" ||
		decoded.Metadata.ScanLinkState != ScanLinkStateScanning {
		t.Fatalf("metadata did not round-trip: %+v", decoded.Metadata)
	}
}

func TestRouterMetadataIsOmittedFromLegacyTarget(t *testing.T) {
	// Given an old router target without metadata
	target := RouterTarget{Quality: Quality1080p, Status: StatusQueued}

	// When it is serialized
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}

	// Then the additive metadata field is absent
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal target: %v", err)
	}
	if _, ok := fields["metadata"]; ok {
		t.Fatalf("metadata was not omitted: %s", data)
	}
}
