package models

import "testing"

func TestAuthSessionProviderProvenance(t *testing.T) {
	// Given
	local := LocalSessionProviderKey()
	plugin, err := NewPluginSessionProviderKey(42, "ldap")
	if err != nil {
		t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
	}

	// When
	parsed, err := ParseSessionProviderKey(plugin.String())

	// Then
	if local.String() != "local" {
		t.Fatalf("local provider key = %q, want local", local.String())
	}
	if err != nil {
		t.Fatalf("ParseSessionProviderKey() error = %v", err)
	}
	if parsed != plugin {
		t.Fatalf("parsed provider key = %q, want %q", parsed.String(), plugin.String())
	}
}

func TestAuthSessionProviderProvenanceRejectsMalformedPluginKeys(t *testing.T) {
	// Given
	inputs := []string{
		"",
		"plugin",
		"plugin:0:ldap",
		"plugin:1:",
		"plugin:one:ldap",
		"local:extra",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			// When
			_, err := ParseSessionProviderKey(input)

			// Then
			if err == nil {
				t.Fatalf("ParseSessionProviderKey(%q) error = nil", input)
			}
		})
	}
}

func TestSessionProviderCapabilityIDGrammar(t *testing.T) {
	// The SDK v0.12.0 manifest validator accepts every non-empty capability
	// ID except for watch_sync_provider.v1. Session provenance is capability
	// type-agnostic, so it must retain that complete generic ID domain.
	valid := []string{"ldap", "LDAP", " oidc ", "ldap:oidc", "ldap--oidc", "ldap_", "öidc", "ldap/oidc", "\x00", "a" + string(make([]byte, 4096))}
	for index, capabilityID := range valid {
		t.Run(capabilityID, func(t *testing.T) {
			key, err := NewPluginSessionProviderKey(index+1, capabilityID)
			if err != nil {
				t.Fatalf("NewPluginSessionProviderKey() error = %v", err)
			}
			parsed, err := ParseSessionProviderKey(key.String())
			if err != nil || parsed != key {
				t.Fatalf("constructor/parser disagreement: key=%q parsed=%q error=%v", key, parsed, err)
			}
		})
	}
}

func TestSessionProviderCapabilityIDRejectsOnlyEmpty(t *testing.T) {
	// Given
	capabilityID := ""

	// When
	_, err := NewPluginSessionProviderKey(1, capabilityID)

	// Then
	if err == nil {
		t.Fatal("NewPluginSessionProviderKey() error = nil")
	}
}

func TestLegacySessionProvenance(t *testing.T) {
	// Given
	session := AuthSession{}

	// When
	provider := session.ProviderKey

	// Then
	if provider != nil {
		t.Fatalf("legacy provider = %v, want nil", provider)
	}
}
