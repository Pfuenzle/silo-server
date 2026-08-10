//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type packagedLDAPHarness struct {
	root, buildRoot, temp, binary, artifact string
	resources                               integrationResources
	directory                               ldapFixture
	process                                 *siloProcess
	admin                                   *adminAPI
	installID                               int
}

type ldapLoginPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func newPackagedLDAPHarness(t *testing.T) *packagedLDAPHarness {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	t.Cleanup(cancel)
	root, temp := repositoryRoot(t), t.TempDir()
	harness := &packagedLDAPHarness{root: root, temp: temp, buildRoot: filepath.Join(temp, "silo-server")}
	if err := os.MkdirAll(harness.buildRoot, 0o700); err != nil {
		t.Fatalf("create isolated Silo build directory: %v", err)
	}
	runCommand(t, ctx, root, "cp", "-a", root+"/.", harness.buildRoot)
	for _, path := range []string{".git", "web/dist", "web/node_modules"} {
		if err := os.RemoveAll(filepath.Join(harness.buildRoot, path)); err != nil {
			t.Fatalf("remove copied build artifact: %v", err)
		}
	}
	if cache := os.Getenv("SILO_LDAP_BUILD_CACHE"); cache != "" {
		harness.usePrebuiltBuild(t, cache)
		runCommand(t, ctx, harness.buildRoot, "chmod", "-R", "u+w", harness.buildRoot)
	} else {
		runCommandEnv(t, ctx, harness.buildRoot, frontendBuildEnvironment(t, temp), "make", "build")
	}
	harness.binary = filepath.Join(harness.buildRoot, "silo")
	release := filepath.Join(temp, "ldap-release")
	runCommand(t, ctx, filepath.Join(root, "..", "silo-plugin-auth-ldap"), "sh", "scripts/release.sh", "1.2.3", release)
	harness.artifact = filepath.Join(release, "plugin-linux-amd64")
	verifyArtifactChecksum(t, harness.artifact, filepath.Join(release, "checksums.txt"))
	harness.resources = newIntegrationResources(t, ctx, harness.buildRoot)
	harness.resources.migrate(t, ctx, harness.binary, deterministicSecret)
	harness.resources.configurePostgresUserStore(t, ctx)
	harness.seedDefaultPluginGuards(t, ctx)
	harness.directory = newPackagedLDAPFixture(t, ctx, root)
	return harness
}

func (h *packagedLDAPHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	initial := h.launch(t, ctx)
	h.assertPostgresUserStore(t, initial)
	h.admin = newAdminAPI(initial.baseURL)
	h.admin.createBreakGlassAdmin(t, ctx)
	h.admin.assertBreakGlassAdmin(t, ctx, "before LDAP upload")
	installation := h.admin.uploadPlugin(t, ctx, h.artifact)
	h.admin.configureLDAP(t, ctx, installation.ID, h.ldapConfig())
	h.admin.enableLDAP(t, ctx, installation.ID)
	h.admin.replaceLDAPMappings(t, ctx, installation.ID, []map[string]any{{"external_group_id": "baseline", "target_role": "user"}})
	h.requestRestart(t, ctx, initial)
	h.process = h.launch(t, ctx)
	h.admin.rebind(h.process.baseURL)
	h.installID = installation.ID
	h.admin.assertBreakGlassAdmin(t, ctx, "LDAP startup")
	h.assertProvider(t, ctx)
	h.assertNoDefaultPluginBootstrap(t)
}

func (h *packagedLDAPHarness) assertPostgresUserStore(t *testing.T, process *siloProcess) {
	t.Helper()
	logs := process.logs(t)
	if !strings.Contains(logs, `msg="user store initialized"`) || !strings.Contains(logs, "backend=postgres") {
		t.Fatalf("LDAP fixture did not initialize PostgreSQL user store: %s", logs)
	}
}

func (h *packagedLDAPHarness) ldapConfig() map[string]any {
	return map[string]any{"transport": "ldaps", "server_address": h.directory.ldaps, "server_name": "localhost", "ca_pem": h.directory.caPEM, "bind_dn": "cn=admin,dc=example,dc=org", "bind_password": "service-password", "user_base_dn": "ou=people,dc=example,dc=org", "group_base_dn": "ou=groups,dc=example,dc=org", "user_filter": "(&(objectClass=inetOrgPerson)(uid={username}))", "group_filter": "(&(objectClass=posixGroup)(memberUid={username}))", "username_attribute": "uid", "subject_attribute": "uid", "subject_encoding": "utf8", "group_id_attribute": "cn", "group_id_encoding": "utf8", "connect_timeout_seconds": 5, "tls_timeout_seconds": 5, "bind_timeout_seconds": 5, "search_timeout_seconds": 5, "total_timeout_seconds": 10, "user_result_limit": 1, "group_result_limit": 10}
}

func (h *packagedLDAPHarness) launch(t *testing.T, ctx context.Context) *siloProcess {
	t.Helper()
	return startSilo(t, ctx, siloStartConfig{root: h.buildRoot, binary: h.binary, databaseURL: h.resources.databaseURL, redisURL: h.resources.redisURL, secret: deterministicSecret, publicURL: "{origin}", pluginCache: filepath.Join(h.temp, "plugins")})
}

func (h *packagedLDAPHarness) seedDefaultPluginGuards(t *testing.T, ctx context.Context) {
	t.Helper()
	runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind) VALUES ('silo.tmdb', '0', '/nonexistent/ldap-fixture-tmdb', false, 'manual', 'plugin'), ('silo.tvdb', '0', '/nonexistent/ldap-fixture-tvdb', false, 'manual', 'plugin')`)
	if got := strings.TrimSpace(runCommand(t, ctx, h.buildRoot, "docker", "exec", h.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-c", `SELECT string_agg(plugin_id || '|' || enabled::text || '|' || update_policy, ',' ORDER BY plugin_id) FROM plugin_installations WHERE plugin_id IN ('silo.tmdb', 'silo.tvdb')`)); got != "silo.tmdb|false|manual,silo.tvdb|false|manual" {
		t.Fatalf("default plugin guards = %q", got)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(cleanupCtx, "docker", "exec", h.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", "DELETE FROM plugin_installations WHERE install_path IN ('/nonexistent/ldap-fixture-tmdb', '/nonexistent/ldap-fixture-tvdb')").Run(); err != nil && cleanupCtx.Err() == nil {
			t.Errorf("remove default plugin guards: %v", err)
		}
	})
}

func (h *packagedLDAPHarness) assertNoDefaultPluginBootstrap(t *testing.T) {
	t.Helper()
	logs := h.process.logs(t)
	if strings.Contains(logs, "auto-installing default plugin") || strings.Contains(logs, "silo.tmdb") || strings.Contains(logs, "silo.tvdb") {
		t.Fatal("default plugin bootstrap appeared in LDAP fixture logs")
	}
}

func (h *packagedLDAPHarness) requestRestart(t *testing.T, ctx context.Context, process *siloProcess) {
	t.Helper()
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/admin/server/restart", h.admin.token, map[string]any{"reason": "LDAP auth binding changed"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("request LDAP restart: %d", response.StatusCode)
	}
	process.waitExit(t)
}

func (h *packagedLDAPHarness) assertProvider(t *testing.T, ctx context.Context) {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/providers", "", nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(responseBody(t, response), fmt.Sprintf("plugin:%d:ldap", h.installID)) {
		t.Fatal("packaged LDAP credentials provider was not registered")
	}
}

func (h *packagedLDAPHarness) login(t *testing.T, ctx context.Context, password string) ldapLoginPair {
	t.Helper()
	response := h.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", h.installID), "username": "ldap-user", "password": password})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("LDAP login status %d: %s", response.StatusCode, responseBody(t, response))
	}
	var pair ldapLoginPair
	decodeJSON(t, response.Body, &pair)
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("LDAP login omitted token pair")
	}
	if h.process.pluginPID(t) <= 0 {
		t.Fatal("packaged LDAP plugin is not a child process after authentication")
	}
	return pair
}

func TestPackagedLDAP_LoginAndAdminRevocation(t *testing.T) {
	// Given
	harness := newPackagedLDAPHarness(t)
	harness.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	baseline := harness.login(t, ctx, "test-password")
	harness.assertLDAPIdentity(t, ctx, "user", "false")
	accessGroupID := harness.admin.createLDAPAccessGroup(t, ctx)
	harness.admin.replaceLDAPMappings(t, ctx, harness.installID, []map[string]any{{"external_group_id": "baseline", "access_group_id": accessGroupID}})
	harness.assertRejected(t, ctx, baseline)
	access := harness.login(t, ctx, "test-password")
	harness.assertLDAPAccessGroup(t, ctx, accessGroupID)

	// When
	harness.admin.replaceLDAPMappings(t, ctx, harness.installID, []map[string]any{{"external_group_id": "baseline", "target_role": "admin"}})
	harness.assertRejected(t, ctx, access)
	admin := harness.login(t, ctx, "test-password")

	// Then
	harness.assertLDAPIdentity(t, ctx, "admin", "false")
	harness.assertMe(t, ctx, admin.AccessToken, http.StatusOK)
	harness.admin.replaceLDAPMappings(t, ctx, harness.installID, []map[string]any{{"external_group_id": "baseline", "target_role": "user"}})
	harness.assertRejected(t, ctx, admin)
	least := harness.login(t, ctx, "test-password")
	harness.assertLDAPIdentity(t, ctx, "user", "false")
	harness.assertMe(t, ctx, least.AccessToken, http.StatusOK)
	harness.admin.assertBreakGlassAdmin(t, ctx, "LDAP authoritative demotion")
}

func TestPackagedLDAP_FailureAtomicity(t *testing.T) {
	// Given
	harness := newPackagedLDAPHarness(t)
	harness.start(t)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	before := harness.ldapCounts(t, ctx)

	// When
	response := harness.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", harness.installID), "username": "ldap-user*)(uid=*)", "password": "wrong-password"})
	body := responseBody(t, response)
	_ = response.Body.Close()

	// Then
	if (response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusInternalServerError) || strings.Contains(strings.ToLower(body), "ldap") || harness.ldapCounts(t, ctx) != before {
		t.Fatalf("LDAP failure was not generic and atomic: status=%d body=%q before=%s after=%s", response.StatusCode, body, before, harness.ldapCounts(t, ctx))
	}
	t.Run("audit insert rollback", func(t *testing.T) {
		trigger := "ldap_fixture_audit_failure"
		runCommand(t, ctx, harness.buildRoot, "docker", "exec", harness.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", `CREATE FUNCTION `+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit failure'; END; $$; CREATE TRIGGER `+trigger+` BEFORE INSERT ON external_authorization_audit FOR EACH ROW EXECUTE FUNCTION `+trigger+`()`)
		t.Cleanup(func() {
			_ = exec.Command("docker", "exec", harness.resources.postgres, "psql", "-U", "silo", "-d", "silo", "-c", `DROP TRIGGER IF EXISTS `+trigger+` ON external_authorization_audit; DROP FUNCTION IF EXISTS `+trigger+`()`).Run()
		})
		before := harness.ldapSnapshot(t, ctx)
		response := harness.admin.requestJSON(t, ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"provider": fmt.Sprintf("plugin:%d:ldap", harness.installID), "username": "ldap-user", "password": "test-password"})
		body := responseBody(t, response)
		_ = response.Body.Close()
		after := harness.ldapSnapshot(t, ctx)
		if response.StatusCode != http.StatusInternalServerError || body != "{\"error\":\"internal_error\",\"message\":\"An unexpected error occurred\"}\n" || after != before {
			t.Fatalf("audit failure was not generic and atomic: status=%d body=%q before=%s after=%s", response.StatusCode, body, before, after)
		}
		if err := exec.Command("docker", "exec", harness.resources.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", `DROP TRIGGER IF EXISTS `+trigger+` ON external_authorization_audit; DROP FUNCTION IF EXISTS `+trigger+`()`).Run(); err != nil {
			t.Fatalf("remove audit failure trigger: %v", err)
		}
		harness.login(t, ctx, "test-password")
		if recovered := harness.ldapSnapshot(t, ctx); recovered == before {
			t.Fatalf("LDAP login did not persist after removing audit trigger: snapshot=%s", recovered)
		}
	})
}
