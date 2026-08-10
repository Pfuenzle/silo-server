//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const ldapImage = "chrroessner/openldap@sha256:f90fd81f8a5f55c39972f9ae6716ef52d573819ca33c624b0bdb4aebe4324a6a"

type ldapFixture struct {
	caPEM     string
	caFile    string
	ldaps     string
	container string
}

func newPackagedLDAPFixture(t *testing.T, ctx context.Context, root string) ldapFixture {
	t.Helper()
	if !regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`).MatchString(ldapImage) {
		t.Fatal("LDAP image must be immutable")
	}
	directory := t.TempDir()
	certs, init := filepath.Join(directory, "certs"), filepath.Join(directory, "init")
	for _, path := range []string{certs, init} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create LDAP fixture path: %v", err)
		}
	}
	runCommand(t, ctx, directory, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", filepath.Join(certs, "ca.key"), "-out", filepath.Join(certs, "ca.pem"), "-subj", "/CN=silo-ldap-test-ca", "-days", "1")
	runCommand(t, ctx, directory, "openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", filepath.Join(certs, "server.key"), "-out", filepath.Join(certs, "server.csr"), "-subj", "/CN=localhost")
	if err := os.WriteFile(filepath.Join(certs, "san.ext"), []byte("subjectAltName=DNS:localhost,IP:127.0.0.1\n"), 0o600); err != nil {
		t.Fatalf("write LDAP SAN: %v", err)
	}
	runCommand(t, ctx, directory, "openssl", "x509", "-req", "-in", filepath.Join(certs, "server.csr"), "-CA", filepath.Join(certs, "ca.pem"), "-CAkey", filepath.Join(certs, "ca.key"), "-CAcreateserial", "-out", filepath.Join(certs, "server.pem"), "-days", "1", "-extfile", filepath.Join(certs, "san.ext"))
	if err := os.Chmod(filepath.Join(certs, "server.key"), 0o644); err != nil {
		t.Fatalf("make LDAP key readable: %v", err)
	}
	ldif := "dn: uid=ldap-user,ou=people,dc=example,dc=org\nobjectClass: inetOrgPerson\ncn: LDAP User\nsn: User\nuid: ldap-user\nuserPassword: test-password\n\ndn: cn=baseline,ou=groups,dc=example,dc=org\nobjectClass: posixGroup\ncn: baseline\ngidNumber: 1001\nmemberUid: ldap-user\n\n"
	if err := os.WriteFile(filepath.Join(init, "ldap.ldif"), []byte(ldif), 0o644); err != nil {
		t.Fatalf("write LDAP fixture entries: %v", err)
	}
	runID := integrationRunID(t)
	container := ldapContainerName(t.Name(), runID)
	runCommand(t, ctx, root, "docker", "run", "-d", "--rm", "--name", container, "--label", "silo.ldap.test="+runID, "-p", "127.0.0.1::389", "-p", "127.0.0.1::636", "-v", certs+":/certs:ro", "-v", init+":/docker-entrypoint-initdb.d:ro", "-e", "LDAP_ADMIN_PASSWORD=service-password", "-e", "LDAP_BASE_DN=dc=example,dc=org", "-e", "LDAP_ENABLE_TLS=true", "-e", "LDAP_ENABLE_LDAPS=true", "-e", "LDAP_TLS_CA_FILE=/certs/ca.pem", "-e", "LDAP_TLS_CERT_FILE=/certs/server.pem", "-e", "LDAP_TLS_KEY_FILE=/certs/server.key", ldapImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	port := strings.TrimSpace(runCommand(t, ctx, root, "docker", "port", container, "636/tcp"))
	_, value, ok := strings.Cut(port, ":")
	if !ok || value == "" {
		t.Fatalf("parse LDAP LDAPS port %q", port)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if exec.Command("openssl", "s_client", "-connect", "127.0.0.1:"+value, "-servername", "localhost", "-CAfile", filepath.Join(certs, "ca.pem"), "-verify_return_error", "-brief").Run() == nil {
			caPEM, err := os.ReadFile(filepath.Join(certs, "ca.pem"))
			if err != nil {
				t.Fatalf("read LDAP CA: %v", err)
			}
			return ldapFixture{caPEM: string(caPEM), caFile: filepath.Join(certs, "ca.pem"), ldaps: fmt.Sprintf("127.0.0.1:%s", value), container: container}
		}
	}
	t.Fatalf("LDAP fixture did not become ready: %s", runCommand(t, context.Background(), root, "docker", "logs", container))
	return ldapFixture{}
}

func (f ldapFixture) setBaselineMembership(t *testing.T, ctx context.Context, member bool) {
	t.Helper()
	operation := "delete"
	if member {
		operation = "add"
	}
	ldif := fmt.Sprintf("dn: cn=baseline,ou=groups,dc=example,dc=org\nchangetype: modify\n%s: memberUid\nmemberUid: ldap-user\n\n", operation)
	path := filepath.Join(t.TempDir(), "baseline-membership.ldif")
	if err := os.WriteFile(path, []byte(ldif), 0o644); err != nil {
		t.Fatalf("write LDAP membership mutation: %v", err)
	}
	runCommand(t, ctx, "", "docker", "cp", path, f.container+":/tmp/baseline-membership.ldif")
	runCommand(t, ctx, "", "docker", "exec", f.container, "ldapmodify", "-x", "-H", "ldap://localhost", "-D", "cn=admin,dc=example,dc=org", "-w", "service-password", "-f", "/tmp/baseline-membership.ldif")
}

var ldapContainerUnsafe = regexp.MustCompile(`[^a-z0-9_.-]+`)

func ldapContainerName(testName, runID string) string {
	name := ldapContainerUnsafe.ReplaceAllString(strings.ToLower(testName), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "test"
	}
	if len(name) > 20 {
		name = name[:20]
	}
	return "silo-ldap-" + name + "-" + strings.TrimPrefix(runID, "silo-")
}

func TestLDAPContainerName_IsolatesSameTestAcrossRuns(t *testing.T) {
	first := ldapContainerName("Test Packaged/LDAP", "silo-11111111111111111111111111111111")
	second := ldapContainerName("Test Packaged/LDAP", "silo-22222222222222222222222222222222")
	if first == second || len(first) > 63 || len(second) > 63 {
		t.Fatalf("LDAP container names are not distinct Docker-safe names: %q %q", first, second)
	}
}
