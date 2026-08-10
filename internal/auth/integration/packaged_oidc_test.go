//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const deterministicSecret = "integration-secret-key-0123456789abcdef"

type packagedOIDCHarness struct {
	root      string
	buildRoot string
	temp      string
	resources integrationResources
	idp       *fakeOIDCServer
	binary    string
	artifact  string
	process   *siloProcess
	admin     *adminAPI
	installID int
}

type packagedOIDCResult struct {
	providerID string
	pluginPID  int
}

func newPackagedOIDCHarness(t *testing.T) *packagedOIDCHarness {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	t.Cleanup(cancel)
	root := repositoryRoot(t)
	temp := t.TempDir()
	harness := &packagedOIDCHarness{root: root, buildRoot: filepath.Join(temp, "silo-server"), temp: temp}
	harness.prepareBuild(t, ctx)
	harness.resources = newIntegrationResources(t, ctx, harness.buildRoot)
	harness.resources.migrate(t, ctx, harness.binary, deterministicSecret)
	harness.resources.configurePostgresUserStore(t, ctx)
	harness.idp = newFakeOIDCServer(t, temp)
	return harness
}

func (h *packagedOIDCHarness) prepareBuild(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := os.MkdirAll(h.buildRoot, 0o700); err != nil {
		t.Fatalf("create isolated Silo build directory: %v", err)
	}
	runCommand(t, ctx, h.root, "cp", "-a", h.root+"/.", h.buildRoot)
	if err := os.RemoveAll(filepath.Join(h.buildRoot, ".git")); err != nil {
		t.Fatalf("remove copied git directory: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(h.buildRoot, "web", "dist")); err != nil {
		t.Fatalf("remove copied frontend dist: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(h.buildRoot, "web", "node_modules")); err != nil {
		t.Fatalf("remove copied frontend node_modules: %v", err)
	}
	if cache := os.Getenv("SILO_OIDC_BUILD_CACHE"); cache != "" {
		h.usePrebuiltBuild(t, cache)
		runCommand(t, ctx, h.buildRoot, "chmod", "-R", "u+w", h.buildRoot)
	} else {
		runCommandEnv(t, ctx, h.buildRoot, frontendBuildEnvironment(t, h.temp), "make", "build")
	}
	h.binary = filepath.Join(h.buildRoot, "silo")
	pluginRoot := filepath.Clean(filepath.Join(h.root, "..", "silo-plugin-auth-oidc"))
	release := filepath.Join(h.temp, "oidc-release")
	runCommand(t, ctx, pluginRoot, "sh", "scripts/release.sh", "1.2.3", release)
	h.artifact = filepath.Join(release, "plugin-linux-amd64")
	verifyArtifactChecksum(t, h.artifact, filepath.Join(release, "checksums.txt"))
}

func (h *packagedOIDCHarness) usePrebuiltBuild(t *testing.T, cache string) {
	t.Helper()
	if got, want := sourceTreeHash(t, h.root), os.Getenv("SILO_OIDC_BUILD_SOURCE_HASH"); want == "" || got != want {
		t.Fatalf("OIDC build cache source hash mismatch: got %q want %q", got, want)
	}
	receipt, err := os.ReadFile(os.Getenv("SILO_OIDC_BUILD_RECEIPT"))
	if err != nil || string(receipt) != "source_hash="+os.Getenv("SILO_OIDC_BUILD_SOURCE_HASH")+" production_builds=1\n" {
		t.Fatalf("OIDC build receipt is invalid: %v", err)
	}
	cacheBinary := filepath.Join(cache, "silo")
	cacheAssets := filepath.Join(cache, "web", "dist")
	if info, err := os.Stat(cacheBinary); err != nil || info.IsDir() {
		t.Fatalf("OIDC build cache binary unavailable at %q: %v", cacheBinary, err)
	}
	if info, err := os.Stat(cacheAssets); err != nil || !info.IsDir() {
		t.Fatalf("OIDC build cache assets unavailable at %q: %v", cacheAssets, err)
	}
	runCommand(t, context.Background(), h.root, "cp", "-a", cacheBinary, filepath.Join(h.buildRoot, "silo"))
	runCommand(t, context.Background(), h.root, "cp", "-a", cacheAssets, filepath.Join(h.buildRoot, "web", "dist"))
}

func sourceTreeHash(t *testing.T, root string) string {
	t.Helper()
	output := runCommand(t, context.Background(), root, "sh", "-c", "tar --sort=name --mtime='UTC 1970-01-01' --exclude=.git --exclude=.omo --exclude=web/node_modules --exclude=web/dist --exclude=test-results --exclude=web/test-results --exclude=playwright-report --exclude=web/playwright-report -cf - . | sha256sum")
	hash, _, ok := strings.Cut(strings.TrimSpace(output), " ")
	if !ok || len(hash) != 64 {
		t.Fatalf("parse OIDC source hash %q", output)
	}
	return hash
}

func frontendBuildEnvironment(t *testing.T, temporaryDirectory string) []string {
	t.Helper()
	if _, err := exec.LookPath("pnpm"); err == nil {
		return nil
	}
	bin := filepath.Join(temporaryDirectory, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatalf("create temporary pnpm bin directory: %v", err)
	}
	wrapper := filepath.Join(bin, "pnpm")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec npx --yes pnpm@9.15.0 \"$@\"\n"), 0o700); err != nil {
		t.Fatalf("write temporary pnpm wrapper: %v", err)
	}
	return []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
}

func (h *packagedOIDCHarness) start(t *testing.T) packagedOIDCResult {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	initial := h.launch(t, ctx, "{origin}")
	h.assertPostgresUserStore(t, initial)
	admin := newAdminAPI(initial.baseURL)
	admin.createBreakGlassAdmin(t, ctx)
	admin.assertBreakGlassAdmin(t, ctx, "before enable")
	installation := admin.uploadPlugin(t, ctx, h.artifact)
	if _, err := os.Stat(installation.InstallPath); err != nil {
		t.Fatalf("installed packaged plugin is unavailable at %q: %v", installation.InstallPath, err)
	}
	admin.configureOIDC(t, ctx, installation.ID, h.idp.url)
	h.assertRuntimeConfigEncryptedEnvelope(t, ctx, installation.ID)
	admin.enableOIDC(t, ctx, installation.ID)
	admin.assertBreakGlassAdmin(t, ctx, "enable")
	h.requestRestart(t, ctx, admin, initial)

	withoutPublicURL := h.launch(t, ctx, "")
	h.assertURLMutation(t, ctx, withoutPublicURL, installation.ID)
	admin.rebind(withoutPublicURL.baseURL)
	admin.assertBreakGlassAdmin(t, ctx, "down")
	withoutPublicURL.stop(t)

	withPublicURL := h.launch(t, ctx, "{origin}")
	providerID := h.assertProviderAndRedirect(t, ctx, withPublicURL, installation.ID)
	if requests, err := os.ReadFile(h.idp.requestLog); err != nil || !strings.Contains(string(requests), "GET /.well-known/openid-configuration") {
		t.Fatalf("packaged OIDC plugin did not query fake issuer discovery: %v\n%s", err, requests)
	}
	pluginPID := withPublicURL.pluginPID(t)
	t.Logf("packaged OIDC proof: install=%s plugin_pid=%d provider=%s", installation.InstallPath, pluginPID, providerID)
	admin.rebind(withPublicURL.baseURL)
	admin.assertBreakGlassAdmin(t, ctx, "up")
	h.process, h.admin, h.installID = withPublicURL, admin, installation.ID
	return packagedOIDCResult{providerID: providerID, pluginPID: pluginPID}
}

func (h *packagedOIDCHarness) assertPostgresUserStore(t *testing.T, process *siloProcess) {
	t.Helper()
	logs := process.logs(t)
	if !strings.Contains(logs, `msg="user store initialized"`) || !strings.Contains(logs, "backend=postgres") {
		t.Fatalf("OIDC fixture did not initialize PostgreSQL user store: %s", logs)
	}
}

func (h *packagedOIDCHarness) endToEnd(t *testing.T) {
	t.Helper()
	h.start(t)
	h.assertAdminOrigin(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	pair := h.oauthLogin(t, ctx)
	h.assertAuthenticated(t, ctx, pair.AccessToken)
	h.assertRefreshLogoutRelogin(t, ctx, pair)
}

func (h *packagedOIDCHarness) assertAdminOrigin(t *testing.T) {
	t.Helper()
	if h.admin.baseURL != h.process.baseURL {
		t.Fatalf("stale admin API origin: got %q want %q", h.admin.baseURL, h.process.baseURL)
	}
}

type oauthPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Next         string `json:"next"`
}

func (h *packagedOIDCHarness) oauthLogin(t *testing.T, ctx context.Context) oauthPair {
	t.Helper()
	client := h.oidcHTTPClient(t)
	init, err := client.Post(h.process.baseURL+fmt.Sprintf("/api/v1/auth/oauth/%d/init?next=//evil.example", h.installID), "application/json", nil)
	if err != nil || init.StatusCode != http.StatusFound {
		t.Fatalf("OAuth init: %v status=%v\n%s", err, init, h.process.logs(t))
	}
	authorizeURL := init.Header.Get("Location")
	_ = init.Body.Close()
	if strings.Contains(authorizeURL, "access_token") {
		t.Fatal("OAuth init redirect leaked token")
	}
	authorize, err := client.Get(authorizeURL)
	if err != nil || authorize.StatusCode != http.StatusFound {
		t.Fatalf("fake authorize: %v", err)
	}
	callbackURL := authorize.Header.Get("Location")
	_ = authorize.Body.Close()
	callback, err := client.Get(callbackURL)
	if err != nil || callback.StatusCode != http.StatusFound {
		t.Fatalf("OAuth callback: %v", err)
	}
	completeURL := callback.Header.Get("Location")
	_ = callback.Body.Close()
	h.process.assertAlive(t, "OAuth completion exchange")
	if strings.Contains(completeURL, "access_token") || !strings.HasPrefix(completeURL, h.process.baseURL+"/login/oauth-complete?") {
		t.Fatalf("unsafe completion redirect %q", completeURL)
	}
	code := mustURLCode(t, completeURL)
	h.assertOAuthStateEncrypted(t, ctx)
	h.assertCompletionEncrypted(t, ctx)
	payload, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("marshal OAuth completion: %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.process.baseURL+"/api/v1/auth/oauth/complete", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build OAuth completion: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := h.admin.client.Do(request)
	if err != nil {
		t.Fatalf("OAuth completion transport: %v\n%s", err, h.process.logs(t))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("complete OAuth: %d %s", response.StatusCode, responseBody(t, response))
	}
	var pair oauthPair
	decodeJSON(t, response.Body, &pair)
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.Next != "/" {
		t.Fatalf("invalid OAuth completion %+v", pair)
	}
	h.assertCompletionConsumed(t, ctx)
	h.assertDefaultAccessGroup(t, ctx)
	h.process.assertAlive(t, "completion replay")
	replayPayload, _ := json.Marshal(map[string]string{"code": code})
	replayRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.process.baseURL+"/api/v1/auth/oauth/complete", bytes.NewReader(replayPayload))
	replayRequest.Header.Set("Content-Type", "application/json")
	replay, err := h.admin.client.Do(replayRequest)
	if err != nil {
		t.Fatalf("completion replay transport: %v\n%s", err, h.process.logs(t))
	}
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("completion replay=%d, want 401", replay.StatusCode)
	}
	return pair
}

func (h *packagedOIDCHarness) oidcHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	caPEM, err := os.ReadFile(h.idp.caFile)
	if err != nil {
		t.Fatalf("read fake OIDC CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("parse fake OIDC CA")
	}
	client := newOAuthTestClient(t)
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return client
}

func newOAuthTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create OAuth test cookie jar: %v", err)
	}
	return &http.Client{
		Timeout: integrationHTTPTimeout,
		Jar:     jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (h *packagedOIDCHarness) assertAuthenticated(t *testing.T, ctx context.Context, token string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.process.baseURL+"/api/v1/auth/me", nil)
	if err != nil {
		t.Fatalf("build OAuth /me: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := h.admin.client.Do(request)
	if err != nil {
		t.Fatalf("OAuth /me transport: %v\n%s", err, h.process.logs(t))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("OAuth /me=%d", response.StatusCode)
	}
}

func (h *packagedOIDCHarness) assertRefreshLogoutRelogin(t *testing.T, ctx context.Context, pair oauthPair) {
	t.Helper()
	h.process.assertAlive(t, "refresh")
	payload, _ := json.Marshal(map[string]string{"refresh_token": pair.RefreshToken})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.process.baseURL+"/api/v1/auth/refresh", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	refresh, err := h.admin.client.Do(request)
	if err != nil {
		t.Fatalf("refresh transport while Silo PID %d reports alive: %v\n%s", h.process.command.Process.Pid, err, h.process.logs(t))
	}
	defer refresh.Body.Close()
	if refresh.StatusCode != http.StatusOK {
		t.Fatalf("refresh=%d", refresh.StatusCode)
	}
	var renewed oauthPair
	decodeJSON(t, refresh.Body, &renewed)
	logout := h.admin.request(t, ctx, http.MethodPost, "/api/v1/auth/logout", renewed.AccessToken, nil, "")
	defer logout.Body.Close()
	if logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout=%d", logout.StatusCode)
	}
	revoked := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"refresh_token": renewed.RefreshToken})
	defer revoked.Body.Close()
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked refresh=%d", revoked.StatusCode)
	}
	h.oauthLogin(t, ctx)
}

func mustURLCode(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	code := parsed.Query().Get("code")
	if code == "" {
		t.Fatal("completion URL lacks code")
	}
	return code
}

func (h *packagedOIDCHarness) launch(t *testing.T, ctx context.Context, publicURL string) *siloProcess {
	t.Helper()
	return startSilo(t, ctx, siloStartConfig{root: h.buildRoot, binary: h.binary, databaseURL: h.resources.databaseURL, redisURL: h.resources.redisURL, secret: deterministicSecret, publicURL: publicURL, pluginCache: filepath.Join(h.temp, "plugins"), caFile: h.idp.caFile})
}

func (h *packagedOIDCHarness) requestRestart(t *testing.T, ctx context.Context, admin *adminAPI, process *siloProcess) {
	t.Helper()
	response := admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/admin/server/restart", admin.token, map[string]any{"reason": "OIDC auth binding changed"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("request Silo restart: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	process.waitExit(t)
}

func (h *packagedOIDCHarness) assertURLMutation(t *testing.T, ctx context.Context, process *siloProcess, installationID int) {
	t.Helper()
	api := newAdminAPI(process.baseURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, process.baseURL+"/api/v1/auth/providers", nil)
	if err != nil {
		t.Fatalf("build providers request: %v", err)
	}
	providers, err := api.client.Do(request)
	if err != nil {
		t.Fatalf("providers without public URL: %v\n%s", err, process.logs(t))
	}
	defer providers.Body.Close()
	if providers.StatusCode != http.StatusOK {
		t.Fatalf("providers without public URL: status %d", providers.StatusCode)
	}
	if strings.Contains(responseBody(t, providers), "plugin:") {
		t.Fatal("provider remained visible after removing SILO_PUBLIC_URL")
	}
	init := api.request(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/auth/oauth/%d/init", installationID), "", nil, "")
	defer init.Body.Close()
	if init.StatusCode != http.StatusNotFound {
		t.Fatalf("OAuth init without public URL = %d, want 404", init.StatusCode)
	}
}

func (h *packagedOIDCHarness) assertProviderAndRedirect(t *testing.T, ctx context.Context, process *siloProcess, installationID int) string {
	t.Helper()
	api := newAdminAPI(process.baseURL)
	providers := api.request(t, ctx, http.MethodGet, "/api/v1/auth/providers", "", nil, "")
	defer providers.Body.Close()
	providerID := fmt.Sprintf("plugin:%d:oidc", installationID)
	if providers.StatusCode != http.StatusOK || !strings.Contains(responseBody(t, providers), providerID) {
		t.Fatalf("providers did not include %q", providerID)
	}
	client := newOAuthTestClient(t)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/auth/oauth/%d/init", process.baseURL, installationID), nil)
	if err != nil {
		t.Fatalf("build OAuth init request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("OAuth init request: %v\n%s", err, process.logs(t))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || !strings.HasPrefix(response.Header.Get("Location"), h.idp.url+"/authorize") {
		t.Fatalf("OAuth init = %d location=%q\n%s", response.StatusCode, response.Header.Get("Location"), process.logs(t))
	}
	authorizeURL, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse OAuth authorize URL: %v", err)
	}
	wantCallback := process.baseURL + fmt.Sprintf("/api/v1/auth/oauth/%d/callback", installationID)
	if got := authorizeURL.Query().Get("redirect_uri"); got != wantCallback {
		t.Fatalf("OAuth callback origin/path = %q, want %q", got, wantCallback)
	}
	idpResponse, err := h.oidcHTTPClient(t).Get(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("IdP authorize request: %v", err)
	}
	_ = idpResponse.Body.Close()
	if got := h.idp.lastRedirectURI(); got != wantCallback {
		t.Fatalf("IdP received callback origin/path = %q, want %q", got, wantCallback)
	}
	return providerID
}

func verifyArtifactChecksum(t *testing.T, artifact, checksums string) {
	t.Helper()
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("read release artifact: %v", err)
	}
	checksumData, err := os.ReadFile(checksums)
	if err != nil {
		t.Fatalf("read release checksums: %v", err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(data)) + "  " + filepath.Base(artifact)
	if !strings.Contains(string(checksumData), want) {
		t.Fatalf("release checksum missing %q", want)
	}
}

func TestPackagedOIDC_HostStartsProvider(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)

	// When
	result := harness.start(t)

	// Then
	if result.providerID == "" || result.pluginPID <= 0 {
		t.Fatalf("missing packaged provider proof: %+v", result)
	}
}

func TestPackagedOIDC_EndToEnd(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)

	// When
	harness.endToEnd(t)
}

func TestPackagedOIDC_HostRemainsAliveAfterStartup(t *testing.T) {
	// Given
	harness := newPackagedOIDCHarness(t)
	harness.start(t)

	// When
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	<-timer.C

	// Then
	harness.process.assertAlive(t, "30-second idle lifetime")
}
