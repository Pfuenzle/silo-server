package auth

import (
	"testing"
)

func TestParseCredentialProviderPolicy_EmptyString_ReturnsEmptyPolicy(t *testing.T) {
	policy, err := ParseCredentialProviderPolicy("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !policy.IsEmpty() {
		t.Fatal("expected empty policy for empty string")
	}
}

func TestParseCredentialProviderPolicy_ValidOrder_ReturnsOrderedIDs(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"ldap":  {ID: "ldap", Mode: "credentials"},
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `["ldap","local"]`
	policy, err := ParseCredentialProviderPolicy(raw, registered)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids := policy.ProviderIDs()
	if len(ids) != 2 || ids[0] != "ldap" || ids[1] != "local" {
		t.Fatalf("expected [ldap, local], got %v", ids)
	}
}

func TestParseCredentialProviderPolicy_DuplicateProvider_ReturnsError(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"ldap":  {ID: "ldap", Mode: "credentials"},
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `["ldap","ldap"]`
	_, err := ParseCredentialProviderPolicy(raw, registered)
	if err == nil {
		t.Fatal("expected error for duplicate provider")
	}
}

func TestParseCredentialProviderPolicy_UnknownProvider_ReturnsError(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `["nonexistent"]`
	_, err := ParseCredentialProviderPolicy(raw, registered)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestParseCredentialProviderPolicy_OAuthProvider_ReturnsError(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"oidc":  {ID: "oidc", Mode: "oauth"},
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `["oidc"]`
	_, err := ParseCredentialProviderPolicy(raw, registered)
	if err == nil {
		t.Fatal("expected error for OAuth provider in fallback order")
	}
}

func TestParseCredentialProviderPolicy_EmptyArray_ReturnsEmptyPolicy(t *testing.T) {
	policy, err := ParseCredentialProviderPolicy("[]", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !policy.IsEmpty() {
		t.Fatal("expected empty policy for empty array")
	}
}

func TestParseCredentialProviderPolicy_EmptyProviderID_ReturnsError(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `[""]`
	_, err := ParseCredentialProviderPolicy(raw, registered)
	if err == nil {
		t.Fatal("expected error for empty provider ID")
	}
}

func TestParseCredentialProviderPolicy_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := ParseCredentialProviderPolicy("not-json", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseCredentialProviderPolicy_MixedCredentialAndOAuth_RejectsOAuth(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"ldap":  {ID: "ldap", Mode: "credentials"},
		"oidc":  {ID: "oidc", Mode: "oauth"},
		"local": {ID: "local", Mode: "credentials"},
	}
	raw := `["ldap","oidc","local"]`
	_, err := ParseCredentialProviderPolicy(raw, registered)
	if err == nil {
		t.Fatal("expected error for OAuth provider mixed into fallback order")
	}
}

func TestCredentialProviderPolicyJSON_EmptySlice_ReturnsEmptyString(t *testing.T) {
	raw, err := CredentialProviderPolicyJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != "" {
		t.Fatalf("expected empty string, got %q", raw)
	}
}

func TestCredentialProviderPolicyJSON_ValidSlice_ReturnsJSON(t *testing.T) {
	raw, err := CredentialProviderPolicyJSON([]string{"ldap", "local"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != `["ldap","local"]` {
		t.Fatalf("expected JSON array, got %q", raw)
	}
}

func TestCredentialProviderPolicy_ProviderIDs_ReturnsClone(t *testing.T) {
	registered := map[string]LoginProviderInfo{
		"ldap":  {ID: "ldap", Mode: "credentials"},
		"local": {ID: "local", Mode: "credentials"},
	}
	policy, _ := ParseCredentialProviderPolicy(`["ldap","local"]`, registered)
	ids1 := policy.ProviderIDs()
	ids2 := policy.ProviderIDs()
	ids1[0] = "mutated"
	if ids2[0] != "ldap" {
		t.Fatal("ProviderIDs should return a clone, not a shared slice")
	}
}
