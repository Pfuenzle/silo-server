//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedOIDC_FirstLoginSessionFailureLeavesNoResidue(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)
	harness.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	trigger := "oidc_first_login_session_failure"
	runCommand(t, ctx, harness.buildRoot, "docker", "exec", harness.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", `CREATE FUNCTION `+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'session failure'; END; $$; CREATE TRIGGER `+trigger+` BEFORE INSERT ON auth_sessions FOR EACH ROW EXECUTE FUNCTION `+trigger+`()`)
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", harness.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-c", `DROP TRIGGER IF EXISTS `+trigger+` ON auth_sessions; DROP FUNCTION IF EXISTS `+trigger+`()`).Run()
	})

	// When
	harness.assertOAuthFailure(t, ctx)

	// Then
	if counts := harness.firstLoginCounts(t, ctx); counts != "0|0|0|0" {
		t.Fatalf("OIDC first-login residue = %s, want 0|0|0|0", counts)
	}
}

func TestPackagedOIDC_LifecyclePreservesLocalAdminAndExternalIdentity(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)

	// When
	harness.assertLifecycle(t)

	// Then
}

func TestPackagedOIDC_RejectsInvalidUpstreamResponsesWithoutMutation(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)
	harness.start(t)

	// When
	harness.assertRejectedUpstreamResponses(t)

	// Then
}

func TestPackagedOIDC_PackageReplacementPreservesInstallationState(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)
	harness.start(t)

	// When
	harness.assertPackageReplacement(t)

	// Then
}

func (h *packagedOIDCHarness) assertLifecycle(t *testing.T) {
	t.Helper()
	h.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	pair := h.oauthLogin(t, ctx)
	h.assertAuthenticated(t, ctx, pair.AccessToken)
	h.assertProviderSession(t, ctx)
	identity := h.identitySnapshot(t, ctx)

	h.admin.setInstallationEnabled(t, ctx, h.installID, false)
	h.admin.assertBreakGlassAdmin(t, ctx, "disable")
	h.assertSessionRejected(t, ctx, pair.AccessToken, "disable")
	h.restart(t, ctx, h.idp.caFile)
	h.assertProviderAbsent(t, ctx)
	h.admin.assertBreakGlassAdmin(t, ctx, "restart while disabled")

	h.admin.setInstallationEnabled(t, ctx, h.installID, true)
	h.admin.assertBreakGlassAdmin(t, ctx, "re-enable")
	h.restart(t, ctx, h.idp.caFile)
	h.assertProviderAndRedirect(t, ctx, h.process, h.installID)
	h.admin.assertBreakGlassAdmin(t, ctx, "restart while enabled")
	h.oauthLogin(t, ctx)
	h.assertIdentityUnchanged(t, ctx, identity)
	h.assertOIDCCardinality(t, ctx)
	h.assertNoSecretsInDurableStores(t, ctx)
	h.assertNoSecretsInRuntimeEvidence(t)
}

func (h *packagedOIDCHarness) assertRejectedUpstreamResponses(t *testing.T) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	for _, mode := range []fakeOIDCMode{fakeOIDCWrongNonce, fakeOIDCWrongSignature} {
		t.Run(string(mode), func(t *testing.T) {
			before := h.identityAndSessionCounts(t, ctx)
			h.idp.setMode(mode)
			h.assertOAuthFailure(t, ctx)
			if after := h.identityAndSessionCounts(t, ctx); after != before {
				t.Fatalf("%s mutated accounts or sessions: before=%s after=%s", mode, before, after)
			}
			h.idp.setMode(fakeOIDCHappy)
		})
	}

	before := h.identityAndSessionCounts(t, ctx)
	h.requestRestart(t, ctx, h.admin, h.process)
	wrongCA := newFakeOIDCServer(t, t.TempDir())
	h.process = h.launchWithCA(t, ctx, wrongCA.caFile)
	h.admin.rebind(h.process.baseURL)
	h.assertInitFailure(t, ctx)
	if after := h.identityAndSessionCounts(t, ctx); after != before {
		t.Fatalf("invalid CA mutated accounts or sessions: before=%s after=%s", before, after)
	}
	h.restart(t, ctx, h.idp.caFile)
	h.assertProviderAndRedirect(t, ctx, h.process, h.installID)
}

func (h *packagedOIDCHarness) assertPackageReplacement(t *testing.T) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	h.oauthLogin(t, ctx)
	identity := h.identitySnapshot(t, ctx)
	for _, version := range []string{"1.2.2", "1.2.3"} {
		h.process.assertAlive(t, "before package replacement "+version)
		artifact := h.releaseArtifact(t, ctx, version)
		installation := h.admin.uploadPlugin(t, ctx, artifact)
		if installation.ID != h.installID || installation.Version != version {
			t.Fatalf("replacement=%+v, want installation=%d version=%s", installation, h.installID, version)
		}
		h.assertManifestVersion(t, ctx, version)
		h.restart(t, ctx, h.idp.caFile)
		h.assertProviderAndRedirect(t, ctx, h.process, h.installID)
		h.oauthLogin(t, ctx)
		h.process.assertAlive(t, "package replacement "+version)
		h.assertIdentityUnchanged(t, ctx, identity)
	}
}

func (h *packagedOIDCHarness) restart(t *testing.T, ctx context.Context, caFile string) {
	t.Helper()
	h.requestRestart(t, ctx, h.admin, h.process)
	h.process = h.launchWithCA(t, ctx, caFile)
	h.admin.rebind(h.process.baseURL)
}

func (h *packagedOIDCHarness) launchWithCA(t *testing.T, ctx context.Context, caFile string) *siloProcess {
	t.Helper()
	return startSilo(t, ctx, siloStartConfig{root: h.buildRoot, binary: h.binary, databaseURL: h.resources.databaseURL, redisURL: h.resources.redisURL, secret: deterministicSecret, publicURL: "{origin}", pluginCache: filepath.Join(h.temp, "plugins"), caFile: caFile})
}

func (h *packagedOIDCHarness) assertSessionRejected(t *testing.T, ctx context.Context, token, stage string) {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/me", token, nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("provider session at %s=%d, want 401", stage, response.StatusCode)
	}
}

func (h *packagedOIDCHarness) assertProviderAbsent(t *testing.T, ctx context.Context) {
	t.Helper()
	providers := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/providers", "", nil, "")
	defer providers.Body.Close()
	if providers.StatusCode != http.StatusOK || strings.Contains(responseBody(t, providers), fmt.Sprintf("plugin:%d:oidc", h.installID)) {
		t.Fatal("disabled OIDC provider remained visible")
	}
	init := h.admin.request(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/auth/oauth/%d/init", h.installID), "", nil, "")
	defer init.Body.Close()
	if init.StatusCode != http.StatusBadGateway {
		t.Fatalf("disabled OAuth init=%d, want 502", init.StatusCode)
	}
}

func (h *packagedOIDCHarness) assertOAuthFailure(t *testing.T, ctx context.Context) {
	t.Helper()
	client := newOAuthTestClient(t)
	init, err := client.Post(h.process.baseURL+fmt.Sprintf("/api/v1/auth/oauth/%d/init", h.installID), "application/json", nil)
	if err != nil || init.StatusCode != http.StatusFound {
		t.Fatalf("failed OAuth init: %v status=%v", err, init)
	}
	authorizeURL := init.Header.Get("Location")
	_ = init.Body.Close()
	authorize, err := h.oidcHTTPClient(t).Get(authorizeURL)
	if err != nil || authorize.StatusCode != http.StatusFound {
		t.Fatalf("failed fake authorization: %v status=%v", err, authorize)
	}
	callbackURL := authorize.Header.Get("Location")
	_ = authorize.Body.Close()
	callback, err := client.Get(callbackURL)
	if err != nil || callback.StatusCode != http.StatusFound {
		t.Fatalf("failed OAuth callback: %v status=%v", err, callback)
	}
	location := callback.Header.Get("Location")
	_ = callback.Body.Close()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse OAuth failure redirect: %v", err)
	}
	query := parsed.Query()
	if parsed.Path != "/login" || query.Get("error") != "oauth_failed" || query.Get("redirect") != "/" {
		t.Fatalf("OAuth failure was not sanitized: %q", location)
	}
	if query.Has("reason") || query.Has("code") || query.Has("state") || len(query) != 2 {
		t.Fatalf("OAuth failure leaked callback detail: %q", location)
	}
	h.assertSecretAbsent(t, "OAuth failure URL", location)
	h.process.assertAlive(t, "rejected upstream response")
}

func (h *packagedOIDCHarness) assertInitFailure(t *testing.T, ctx context.Context) {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/auth/oauth/%d/init", h.installID), "", nil, "")
	defer response.Body.Close()
	body := responseBody(t, response)
	if response.StatusCode != http.StatusBadGateway || strings.Contains(body, "x509") || strings.Contains(body, h.idp.url) {
		t.Fatalf("invalid CA response was not generic: status=%d body=%q", response.StatusCode, body)
	}
	h.assertSecretAbsent(t, "invalid CA response", body)
	h.process.assertAlive(t, "invalid CA rejection")
}

func (h *packagedOIDCHarness) releaseArtifact(t *testing.T, ctx context.Context, version string) string {
	t.Helper()
	release := filepath.Join(h.temp, "oidc-release-"+version)
	pluginRoot := filepath.Clean(filepath.Join(h.root, "..", "silo-plugin-auth-oidc"))
	runCommand(t, ctx, pluginRoot, "sh", "scripts/release.sh", version, release)
	artifact := filepath.Join(release, "plugin-linux-amd64")
	verifyArtifactChecksum(t, artifact, filepath.Join(release, "checksums.txt"))
	return artifact
}

func (h *packagedOIDCHarness) assertManifestVersion(t *testing.T, ctx context.Context, version string) {
	t.Helper()
	manifest := h.databaseQuery(t, ctx, `SELECT convert_from(manifest_json, 'UTF8') FROM plugin_archives WHERE plugin_installation_id = %d ORDER BY created_at DESC LIMIT 1`, h.installID)
	if !strings.Contains(manifest, `"version":"`+version+`"`) {
		t.Fatalf("stored manifest does not record version %s: %s", version, manifest)
	}
}

func (h *packagedOIDCHarness) assertNoSecretsInRuntimeEvidence(t *testing.T) {
	t.Helper()
	requests, err := os.ReadFile(h.idp.requestLog)
	if err != nil {
		t.Fatalf("read IdP request log: %v", err)
	}
	h.assertSecretAbsent(t, "IdP request log", string(requests))
	h.assertSecretAbsent(t, "Silo log", h.process.logs(t))
}
