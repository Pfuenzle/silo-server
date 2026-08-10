//go:build integration

package integration

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"testing"
)

const siloTestRunIDEnv = "SILO_TEST_RUN_ID"

var siloTestRunIDPattern = regexp.MustCompile(`^silo-[a-f0-9]{32}$`)

func integrationRunID(t *testing.T) string {
	t.Helper()
	if runID := os.Getenv(siloTestRunIDEnv); siloTestRunIDPattern.MatchString(runID) {
		return runID
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("create integration run ID: %v", err)
	}
	return "silo-" + hex.EncodeToString(bytes)
}
