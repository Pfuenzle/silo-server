//go:build integration

package integration

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const genericCredentialFailure = "{\"error\":\"invalid_credentials\",\"message\":\"Invalid username or password\"}\n"
const genericInfrastructureFailure = "{\"error\":\"internal_error\",\"message\":\"An unexpected error occurred\"}\n"

func TestPackagedLDAP_IndependentFailures(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
	}{
		{name: "wrong password generic 401", username: "ldap-user", password: "wrong-password"},
		{name: "injection shaped username generic 401 without mutation", username: "ldap-user*)(uid=*)", password: "test-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPackagedLDAPHarness(t)
			h.start(t)
			ctx, cancel := contextWithTimeout(t)
			defer cancel()
			before := h.ldapSnapshot(t, ctx)
			response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", h.installID), "username": tc.username, "password": tc.password})
			body := responseBody(t, response)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized || body != genericCredentialFailure || h.ldapSnapshot(t, ctx) != before {
				t.Fatalf("sanitized LDAP credential failure status=%d body_fingerprint=%x state_before=%s state_after=%s", response.StatusCode, sha256.Sum256([]byte(body)), before, h.ldapSnapshot(t, ctx))
			}
		})
	}
}

func TestPackagedLDAP_EmptyDirectoryGroupsLeastPrivilege(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	h.admin.replaceLDAPMappings(t, ctx, h.installID, []map[string]any{{"external_group_id": "baseline", "target_role": "admin"}})
	h.login(t, ctx, "test-password")
	h.directory.setBaselineMembership(t, ctx, false)

	// When
	pair := h.login(t, ctx, "test-password")

	// Then
	h.assertLDAPIdentity(t, ctx, "user", "false")
	h.assertDefaultLDAPAccessGroup(t, ctx)
	h.assertMe(t, ctx, pair.AccessToken, http.StatusOK)
	h.admin.assertBreakGlassAdmin(t, ctx, "LDAP empty directory groups")
}

func TestPackagedLDAP_LDAPSOutageIsSanitizedAndAtomic(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	before := h.ldapSnapshot(t, ctx)
	runCommand(t, ctx, h.buildRoot, "docker", "pause", h.directory.container)
	t.Cleanup(func() { _ = exec.Command("docker", "unpause", h.directory.container).Run() })

	// When
	started := time.Now()
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", h.installID), "username": "ldap-user", "password": "test-password"})
	body := responseBody(t, response)
	_ = response.Body.Close()

	// Then
	if response.StatusCode != http.StatusInternalServerError || body != genericInfrastructureFailure || h.ldapSnapshot(t, ctx) != before || time.Since(started) > 12*time.Second {
		t.Fatalf("LDAPS outage contract status=%d body_fingerprint=%x state_before=%s state_after=%s duration=%s", response.StatusCode, sha256.Sum256([]byte(body)), before, h.ldapSnapshot(t, ctx), time.Since(started))
	}
}

func TestPackagedLDAP_DirectoryTimeoutIsBoundedAndSanitized(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	before := h.ldapSnapshot(t, ctx)
	config := h.ldapConfig()
	config["server_address"] = "192.0.2.1:636"
	config["connect_timeout_seconds"] = 1
	config["tls_timeout_seconds"] = 1
	config["bind_timeout_seconds"] = 1
	config["search_timeout_seconds"] = 1
	config["total_timeout_seconds"] = 2
	h.admin.configureLDAP(t, ctx, h.installID, config)
	h.restart(t, ctx)

	// When
	started := time.Now()
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", h.installID), "username": "ldap-user", "password": "test-password"})
	body := responseBody(t, response)
	_ = response.Body.Close()

	// Then
	if response.StatusCode != http.StatusInternalServerError || body != genericInfrastructureFailure || h.ldapSnapshot(t, ctx) != before || time.Since(started) > 5*time.Second {
		t.Fatalf("LDAP timeout contract status=%d body_fingerprint=%x state_before=%s state_after=%s duration=%s", response.StatusCode, sha256.Sum256([]byte(body)), before, h.ldapSnapshot(t, ctx), time.Since(started))
	}
}

func TestPackagedLDAP_RuntimeEvidenceExcludesCredentialsAndDirectoryMaterial(t *testing.T) {
	// Given
	h := newPackagedLDAPHarness(t)
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	pair := h.login(t, ctx, "test-password")

	// When
	processLogs := h.process.logs(t)
	ldapLogs := runCommand(t, ctx, h.buildRoot, "docker", "logs", h.directory.container)

	// Then
	for _, channel := range []struct {
		name               string
		content            string
		fixtureLDAPLogOnly bool
	}{
		{name: "Silo log", content: processLogs},
		{name: "LDAP fixture log", content: redactLDAPFixtureBindDN(ldapLogs), fixtureLDAPLogOnly: true},
	} {
		for label, secret := range map[string]string{
			"bind password":        "service-password",
			"LDAP user password":   "test-password",
			"access token":         pair.AccessToken,
			"refresh token":        pair.RefreshToken,
			"LDAP user filter":     "(&(objectClass=inetOrgPerson)(uid={username}))",
			"LDAP group filter":    "(&(objectClass=posixGroup)(memberUid={username}))",
			"private key material": "-----BEGIN PRIVATE KEY-----",
		} {
			if strings.Contains(channel.content, secret) {
				t.Fatalf("%s leaked %s fingerprint=%x", channel.name, label, sha256.Sum256([]byte(secret)))
			}
		}
		if !channel.fixtureLDAPLogOnly && strings.Contains(channel.content, ldapFixtureBindDN) {
			t.Fatalf("%s leaked fixture-only bind DN fingerprint=%x", channel.name, sha256.Sum256([]byte(ldapFixtureBindDN)))
		}
	}
}

const ldapFixtureBindDN = "cn=admin,dc=example,dc=org"

func redactLDAPFixtureBindDN(content string) string {
	return strings.ReplaceAll(content, ldapFixtureBindDN, "[fixture-ldap-bind-dn]")
}
