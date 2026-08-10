package models

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidSessionProviderKey = errors.New("invalid session provider key")

// SessionProviderKey identifies the authentication provider that created a
// session. The zero value is invalid; use a constructor or parser at a boundary.
type SessionProviderKey struct {
	raw string
}

func LocalSessionProviderKey() SessionProviderKey {
	return SessionProviderKey{raw: "local"}
}

func NewPluginSessionProviderKey(installationID int, capabilityID string) (SessionProviderKey, error) {
	if installationID <= 0 || capabilityID == "" {
		return SessionProviderKey{}, fmt.Errorf("plugin session provider: %w", ErrInvalidSessionProviderKey)
	}
	return SessionProviderKey{raw: "plugin:" + strconv.Itoa(installationID) + ":" + capabilityID}, nil
}

func ParseSessionProviderKey(raw string) (SessionProviderKey, error) {
	if raw == "local" {
		return LocalSessionProviderKey(), nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[0] != "plugin" {
		return SessionProviderKey{}, fmt.Errorf("parse session provider %q: %w", raw, ErrInvalidSessionProviderKey)
	}
	installationID, err := strconv.Atoi(parts[1])
	if err != nil {
		return SessionProviderKey{}, fmt.Errorf("parse session provider %q: %w", raw, ErrInvalidSessionProviderKey)
	}
	key, err := NewPluginSessionProviderKey(installationID, parts[2])
	if err != nil || key.raw != raw {
		return SessionProviderKey{}, fmt.Errorf("parse session provider %q: %w", raw, ErrInvalidSessionProviderKey)
	}
	return key, nil
}

func (key SessionProviderKey) String() string {
	return key.raw
}

func (key SessionProviderKey) PluginBinding() (installationID int, capabilityID string, ok bool) {
	if key.raw == "local" {
		return 0, "", false
	}
	parts := strings.SplitN(key.raw, ":", 3)
	if len(parts) != 3 || parts[0] != "plugin" {
		return 0, "", false
	}
	installationID, err := strconv.Atoi(parts[1])
	if err != nil || installationID <= 0 || parts[2] == "" {
		return 0, "", false
	}
	return installationID, parts[2], true
}
