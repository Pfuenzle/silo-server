//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func runCommand(t *testing.T, ctx context.Context, directory, name string, args ...string) string {
	t.Helper()
	return runCommandEnv(t, ctx, directory, nil, name, args...)
}

func runCommandEnv(t *testing.T, ctx context.Context, directory string, environment []string, name string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", formatCommand(name, args), err, output)
	}
	return string(output)
}

func formatCommand(name string, args []string) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
}
