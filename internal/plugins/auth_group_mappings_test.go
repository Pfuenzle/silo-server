package plugins

import (
	"errors"
	"testing"
)

func TestAuthGroupMapping_ValidateBatchRejectsMalformedAndConflictingMappings(t *testing.T) {
	admin := "admin"
	unsupported := "owner"
	cases := []struct {
		name     string
		mappings []AuthGroupMappingInput
		wantErr  error
	}{
		{
			name:     "blank external group id",
			mappings: []AuthGroupMappingInput{{ExternalGroupID: " ", TargetRole: &admin}},
			wantErr:  ErrAuthGroupMappingInvalid,
		},
		{
			name:     "wildcard external group id",
			mappings: []AuthGroupMappingInput{{ExternalGroupID: "team-*", TargetRole: &admin}},
			wantErr:  ErrAuthGroupMappingInvalid,
		},
		{
			name:     "unsupported target role",
			mappings: []AuthGroupMappingInput{{ExternalGroupID: "team-a", TargetRole: &unsupported}},
			wantErr:  ErrAuthGroupMappingInvalid,
		},
		{
			name: "duplicate external group id",
			mappings: []AuthGroupMappingInput{
				{ExternalGroupID: "team-a", TargetRole: &admin},
				{ExternalGroupID: "team-a", TargetRole: &admin},
			},
			wantErr: ErrAuthGroupMappingConflict,
		},
		{
			name: "conflicting external group id",
			mappings: []AuthGroupMappingInput{
				{ExternalGroupID: "team-a", TargetRole: &admin},
				{ExternalGroupID: "team-a"},
			},
			wantErr: ErrAuthGroupMappingConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			mappings := tc.mappings

			// When
			_, err := ValidateAuthGroupMappingBatch(mappings)

			// Then
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateAuthGroupMappingBatch() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestAuthBindingAuthorizationMode_DefaultsToNone(t *testing.T) {
	// Given
	binding := AuthBinding{InstallationID: 7, CapabilityID: "ldap"}

	// When
	mode := binding.EffectiveAuthorizationMode()

	// Then
	if mode != AuthBindingAuthorizationModeNone {
		t.Fatalf("authorization mode = %q, want %q", mode, AuthBindingAuthorizationModeNone)
	}
}
