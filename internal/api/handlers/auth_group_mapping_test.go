package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestAuthBindingAuthorizationMode_ExistingBindingProjectionPreservesCurrentFields(t *testing.T) {
	// Given
	bindings := []*plugins.AuthBinding{{
		InstallationID: 7,
		CapabilityID:   "ldap",
		Enabled:        true,
		DisplayOrder:   3,
		AutoProvision:  true,
		DefaultLogin:   true,
	}}

	// When
	got := authBindingsForInstallation(7, bindings)

	// Then
	if len(got) != 1 {
		t.Fatalf("binding count = %d, want 1", len(got))
	}
	if got[0].CapabilityID != "ldap" || !got[0].Enabled || got[0].DisplayOrder != 3 || !got[0].AutoProvision || !got[0].DefaultLogin {
		t.Fatalf("binding projection changed: %#v", got[0])
	}
}
