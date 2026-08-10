// Package externalgroups defines canonical external directory group identifiers.
package externalgroups

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxIDBytes = 256

var ErrInvalidID = errors.New("external group ID is invalid")

// NormalizeID returns the one representation used for mapping, authorization,
// and auditing. Callers must reject an invalid value at their input boundary.
func NormalizeID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" || strings.Contains(id, "*") || len(id) > MaxIDBytes || !utf8.ValidString(id) {
		return "", ErrInvalidID
	}
	return id, nil
}
