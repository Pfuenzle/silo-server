package auth

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestExternalGroups_Parse_accepts_exactLDAPEnvelope(t *testing.T) {
	// Given
	claims := mustExternalGroupsClaims(t, map[string]any{
		"provider_kind": "ldap",
		"groups": []any{
			map[string]any{"id": "admins", "display_name": "Administrators"},
			map[string]any{"id": "readers"},
		},
	})

	// When
	groups, err := ParseExternalGroupsV1(claims)

	// Then
	if err != nil {
		t.Fatalf("ParseExternalGroupsV1() error = %v", err)
	}
	want := []ExternalGroup{{ID: "admins", DisplayName: "Administrators"}, {ID: "readers"}}
	if len(groups) != len(want) || groups[0] != want[0] || groups[1] != want[1] {
		t.Fatalf("ParseExternalGroupsV1() = %#v, want %#v", groups, want)
	}
}

func TestExternalGroups_Parse_normalizesIDsAndRejectsBounds(t *testing.T) {
	tests := []struct {
		name    string
		groups  []any
		wantErr bool
		wantID  string
	}{
		{name: "trims ID", groups: []any{map[string]any{"id": "  admins\t", "display_name": " Admins "}}, wantID: "admins"},
		{name: "101 groups", groups: make([]any, 101), wantErr: true},
		{name: "257 byte ID", groups: []any{map[string]any{"id": strings.Repeat("a", 257)}}, wantErr: true},
		{name: "257 byte display", groups: []any{map[string]any{"id": "admins", "display_name": strings.Repeat("a", 257)}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": tt.groups})
			groups, err := ParseExternalGroupsV1(claims)
			if tt.wantErr {
				if !errors.Is(err, ErrExternalGroupsInvalid) {
					t.Fatalf("ParseExternalGroupsV1() error = %v, want invalid", err)
				}
				return
			}
			if err != nil || len(groups) != 1 || groups[0].ID != tt.wantID {
				t.Fatalf("ParseExternalGroupsV1() = %#v, %v; want canonical %q", groups, err, tt.wantID)
			}
		})
	}
}

func TestExternalGroups_Parse_rejectsOversizedEnvelope(t *testing.T) {
	claims := mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins", "display_name": strings.Repeat("a", 32<<10)}}})
	if _, err := ParseExternalGroupsV1(claims); !errors.Is(err, ErrExternalGroupsInvalid) {
		t.Fatalf("ParseExternalGroupsV1() error = %v, want invalid", err)
	}
}

func FuzzExternalGroups_Parse(f *testing.F) {
	f.Add("admins")
	f.Add("  admins  ")
	f.Add("")
	f.Fuzz(func(t *testing.T, id string) {
		claims := mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": id}}})
		_, _ = ParseExternalGroupsV1(claims)
	})
}

func TestExternalGroups_Parse_rejects_untrusted_shapes(t *testing.T) {
	tests := []struct {
		name    string
		claims  *structpb.Struct
		wantErr error
	}{
		{"missing version", mustClaims(t, map[string]any{}), ErrExternalGroupsInvalid},
		{"wrong version alongside expected", mustClaims(t, map[string]any{"silo.external_groups.v2": map[string]any{}}), ErrExternalGroupsInvalid},
		{"unknown top level", mustClaims(t, map[string]any{"silo.external_groups.v1": map[string]any{"provider_kind": "ldap", "groups": []any{}}, "role": "admin"}), ErrExternalGroupsInvalid},
		{"wrong provider", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "oidc", "groups": []any{}}), ErrExternalGroupsInvalid},
		{"numeric id", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": 42}}}), ErrExternalGroupsInvalid},
		{"whitespace id", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": " \t "}}}), ErrExternalGroupsInvalid},
		{"wildcard id", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "team-*"}}}), ErrExternalGroupsInvalid},
		{"unknown group field", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins", "role": "admin"}}}), ErrExternalGroupsInvalid},
		{"duplicate id", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "groups": []any{map[string]any{"id": "admins"}, map[string]any{"id": "admins"}}}), ErrExternalGroupsInvalid},
		{"local role field", mustExternalGroupsClaims(t, map[string]any{"provider_kind": "ldap", "role": "admin", "groups": []any{}}), ErrExternalGroupsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := ParseExternalGroupsV1(tt.claims)

			// Then
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseExternalGroupsV1() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func mustExternalGroupsClaims(t *testing.T, envelope map[string]any) *structpb.Struct {
	t.Helper()
	return mustClaims(t, map[string]any{"silo.external_groups.v1": envelope})
}

func mustClaims(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	claims, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	return claims
}
