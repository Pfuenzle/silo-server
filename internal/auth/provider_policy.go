package auth

import (
	"encoding/json"
	"fmt"
	"slices"
)

const SettingKeyCredentialProviderFallback = "auth.credential_provider_fallback"

type CredentialProviderPolicy struct {
	providerIDs []string
	knownIDs    map[string]bool
	oauthIDs    map[string]bool
}

func ParseCredentialProviderPolicy(raw string, registered map[string]LoginProviderInfo) (CredentialProviderPolicy, error) {
	if raw == "" {
		return CredentialProviderPolicy{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return CredentialProviderPolicy{}, fmt.Errorf("credential provider policy: invalid JSON array: %w", err)
	}
	if len(ids) == 0 {
		return CredentialProviderPolicy{}, nil
	}

	knownIDs := make(map[string]bool, len(registered))
	oauthIDs := make(map[string]bool)
	for id, info := range registered {
		knownIDs[id] = true
		if info.Mode == "oauth" {
			oauthIDs[id] = true
		}
	}

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return CredentialProviderPolicy{}, fmt.Errorf("credential provider policy: empty provider ID")
		}
		if seen[id] {
			return CredentialProviderPolicy{}, fmt.Errorf("credential provider policy: duplicate provider %q", id)
		}
		seen[id] = true
		if !knownIDs[id] {
			return CredentialProviderPolicy{}, fmt.Errorf("credential provider policy: unknown provider %q", id)
		}
		if oauthIDs[id] {
			return CredentialProviderPolicy{}, fmt.Errorf("credential provider policy: OAuth provider %q cannot be in credential fallback order", id)
		}
	}

	return CredentialProviderPolicy{
		providerIDs: slices.Clone(ids),
		knownIDs:    knownIDs,
		oauthIDs:    oauthIDs,
	}, nil
}

func (p CredentialProviderPolicy) ProviderIDs() []string {
	if len(p.providerIDs) == 0 {
		return nil
	}
	return slices.Clone(p.providerIDs)
}

func (p CredentialProviderPolicy) IsEmpty() bool {
	return len(p.providerIDs) == 0
}

func (p CredentialProviderPolicy) Contains(id string) bool {
	return p.knownIDs[id]
}

func (p CredentialProviderPolicy) IsOAuth(id string) bool {
	return p.oauthIDs[id]
}

func CredentialProviderPolicyJSON(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal credential provider policy: %w", err)
	}
	return string(data), nil
}

func MustParseCredentialProviderPolicy(raw string, registered map[string]LoginProviderInfo) CredentialProviderPolicy {
	p, err := ParseCredentialProviderPolicy(raw, registered)
	if err != nil {
		panic(err)
	}
	return p
}
