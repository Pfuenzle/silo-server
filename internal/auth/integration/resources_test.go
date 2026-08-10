//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	postgresImageTag = "pgvector/pgvector:pg17"
	redisImageTag    = "redis:7-alpine"
)

type integrationResources struct {
	root        string
	network     string
	postgres    string
	redis       string
	databaseURL string
	redisURL    string
}

func newIntegrationResources(t *testing.T, ctx context.Context, root string) integrationResources {
	t.Helper()
	runID := integrationRunID(t)
	prefix := "silo-oidc-" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-") + "-" + fmt.Sprint(os.Getpid())
	resources := integrationResources{
		root:     root,
		network:  prefix,
		postgres: prefix + "-postgres",
		redis:    prefix + "-redis",
	}
	runCommand(t, ctx, root, "docker", "network", "create", "--label", "silo.oidc.test="+runID, resources.network)
	t.Cleanup(func() { cleanupResources(t, resources) })

	postgresImage := resolvedImageDigest(t, ctx, root, postgresImageTag)
	redisImage := resolvedImageDigest(t, ctx, root, redisImageTag)
	runCommand(t, ctx, root, "docker", "run", "-d", "--rm", "--label", "silo.oidc.test="+runID, "--name", resources.postgres, "--network", resources.network,
		"-e", "POSTGRES_USER=silo", "-e", "POSTGRES_PASSWORD=silo", "-e", "POSTGRES_DB=silo", "-p", "127.0.0.1::5432", postgresImage)
	runCommand(t, ctx, root, "docker", "run", "-d", "--rm", "--label", "silo.oidc.test="+runID, "--name", resources.redis, "--network", resources.network, "-p", "127.0.0.1::6379", redisImage)

	resources.databaseURL = "postgres://silo:silo@127.0.0.1:" + dockerPort(t, ctx, root, resources.postgres, "5432/tcp") + "/silo?sslmode=disable"
	resources.redisURL = "redis://127.0.0.1:" + dockerPort(t, ctx, root, resources.redis, "6379/tcp")
	waitForCommand(t, ctx, root, []string{"docker", "exec", resources.postgres, "pg_isready", "-U", "silo", "-d", "silo"})
	return resources
}

func (r integrationResources) migrate(t *testing.T, ctx context.Context, binary, secret string) {
	t.Helper()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		command := exec.CommandContext(ctx, binary, "--migrate-only")
		command.Dir = r.root
		command.Env = append(os.Environ(), "DATABASE_URL="+r.databaseURL, "REDIS_URL="+r.redisURL, "SECRET_KEY="+secret)
		output, err := command.CombinedOutput()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("migrate Silo database: %v\n%s", ctx.Err(), output)
		case <-deadline.C:
			t.Fatalf("migrate Silo database: %v\n%s", err, output)
		case <-ticker.C:
		}
	}
}

func (r integrationResources) configurePostgresUserStore(t *testing.T, ctx context.Context) {
	t.Helper()
	runCommand(t, ctx, r.root, "docker", "exec", r.postgres, "psql", "-v", "ON_ERROR_STOP=1", "-U", "silo", "-d", "silo", "-c", `INSERT INTO server_settings (key, value) VALUES ('userdb.backend', 'postgres') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	backend := strings.TrimSpace(runCommand(t, ctx, r.root, "docker", "exec", r.postgres, "psql", "-U", "silo", "-d", "silo", "-At", "-c", `SELECT value FROM server_settings WHERE key = 'userdb.backend'`))
	if backend != "postgres" {
		t.Fatalf("configured user store backend = %q, want postgres", backend)
	}
}

func resolvedImageDigest(t *testing.T, ctx context.Context, directory, image string) string {
	t.Helper()
	runCommand(t, ctx, directory, "docker", "pull", image)
	output := runCommand(t, ctx, directory, "docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", image)
	digest := strings.TrimSpace(output)
	if !strings.Contains(digest, "@sha256:") {
		t.Fatalf("docker image %q did not resolve to a digest: %q", image, digest)
	}
	return digest
}

func dockerPort(t *testing.T, ctx context.Context, directory, container, port string) string {
	t.Helper()
	output := runCommand(t, ctx, directory, "docker", "port", container, port)
	_, value, ok := strings.Cut(strings.TrimSpace(output), ":")
	if !ok || value == "" {
		t.Fatalf("parse docker port %q: %q", port, output)
	}
	return value
}

func waitForCommand(t *testing.T, ctx context.Context, directory string, command []string) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if exec.CommandContext(ctx, command[0], command[1:]...).Run() == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %q: %v", command, ctx.Err())
		case <-deadline.C:
			t.Fatalf("timed out waiting for %q", command)
		case <-ticker.C:
		}
	}
}

func cleanupResources(t *testing.T, resources integrationResources) {
	t.Helper()
	started := time.Now()
	t.Logf("teardown start docker resources=%s", resources.network)
	defer func() { t.Logf("teardown end docker resources=%s duration=%s", resources.network, time.Since(started)) }()
	for _, args := range [][]string{{"docker", "rm", "-f", resources.postgres}, {"docker", "rm", "-f", resources.redis}, {"docker", "network", "rm", resources.network}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Logf("teardown start %s", strings.Join(args, " "))
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		err := command.Run()
		if err != nil && ctx.Err() == nil {
			t.Errorf("cleanup %q: %v", args, err)
		}
		t.Logf("teardown end %s err=%v context=%v", strings.Join(args, " "), err, ctx.Err())
		cancel()
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("current directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository root")
		}
		directory = parent
	}
}
