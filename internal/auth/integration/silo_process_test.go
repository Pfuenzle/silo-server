//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const integrationHTTPTimeout = 10 * time.Second

var siloShutdownGrace = 10 * time.Second

type siloProcess struct {
	baseURL   string
	port      int
	jellyPort int
	absPort   int
	groupID   int
	command   *exec.Cmd
	logPath   string
	logFile   *os.File
	stopped   bool
	forced    bool
	startedAt time.Time
	done      chan error
}

type siloStartConfig struct {
	root, binary, databaseURL, redisURL, secret, publicURL, pluginCache, caFile string
}

func startSilo(t *testing.T, ctx context.Context, config siloStartConfig) *siloProcess {
	t.Helper()
	port, jellyPort, absPort := availablePorts(t)
	logPath := filepath.Join(filepath.Dir(config.pluginCache), fmt.Sprintf("silo-%d.log", port))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open Silo log: %v", err)
	}
	command := exec.CommandContext(context.Background(), config.binary)
	command.Dir = config.root
	command.SysProcAttr = siloProcessSysProcAttr()
	publicURL := config.publicURL
	if publicURL == "{origin}" {
		publicURL = "http://127.0.0.1:" + strconv.Itoa(port)
	}
	command.Env = append(os.Environ(), "DATABASE_URL="+config.databaseURL, "REDIS_URL="+config.redisURL, "SECRET_KEY="+config.secret, "PORT="+strconv.Itoa(port), "JF_PORT="+strconv.Itoa(jellyPort), "ABS_PORT="+strconv.Itoa(absPort), "SILO_PUBLIC_URL="+publicURL, "SILO_PLUGIN_CACHE_DIR="+config.pluginCache)
	if config.caFile != "" {
		command.Env = append(command.Env, "SSL_CERT_FILE="+config.caFile)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start production Silo: %v", err)
	}
	if err := registerSiloTestProcessGroup(command.Process.Pid); err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		_ = logFile.Close()
		t.Fatalf("register Silo process group: %v", err)
	}
	process := &siloProcess{baseURL: "http://127.0.0.1:" + strconv.Itoa(port), port: port, jellyPort: jellyPort, absPort: absPort, groupID: command.Process.Pid, command: command, logPath: logPath, logFile: logFile, startedAt: time.Now().UTC(), done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	t.Cleanup(func() { process.stop(t) })
	process.waitReady(t, ctx)
	process.assertListenerOwnership(t)
	return process
}

func (p *siloProcess) stop(t *testing.T) {
	t.Helper()
	started := time.Now()
	pid := 0
	if p.command.Process != nil {
		pid = p.command.Process.Pid
	}
	t.Logf("teardown start silo group=%d pid=%d", p.groupID, pid)
	defer func() { t.Logf("teardown end silo group=%d duration=%s", p.groupID, time.Since(started)) }()
	if p.stopped {
		return
	}
	p.stopped = true
	defer unregisterSiloTestProcessGroup(p.groupID)
	if p.groupID > 0 {
		t.Logf("teardown signal silo group=%d SIGINT", p.groupID)
		_ = syscall.Kill(-p.groupID, syscall.SIGINT)
		if p.command.Process != nil {
			select {
			case <-p.done:
				t.Logf("teardown wait silo group=%d exited after SIGINT", p.groupID)
			case <-time.After(siloShutdownGrace):
				t.Logf("teardown signal silo group=%d SIGKILL", p.groupID)
				p.forced = true
				_ = syscall.Kill(-p.groupID, syscall.SIGKILL)
				<-p.done
				t.Logf("teardown wait silo group=%d exited after SIGKILL", p.groupID)
			}
		} else {
			time.Sleep(100 * time.Millisecond)
			t.Logf("teardown signal exited silo group=%d SIGKILL", p.groupID)
			_ = syscall.Kill(-p.groupID, syscall.SIGKILL)
		}
	}
	if err := p.logFile.Close(); err != nil {
		t.Errorf("close Silo log: %v", err)
	}
}

func (p *siloProcess) waitExit(t *testing.T) {
	t.Helper()
	if p.command.Process == nil {
		return
	}
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("Silo restart exit: %v\n%s", err, p.logs(t))
		}
		p.command.Process = nil
		p.terminateGroup(t)
		unregisterSiloTestProcessGroup(p.groupID)
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for Silo restart\n%s", p.logs(t))
	}
}

func (p *siloProcess) terminateGroup(t *testing.T) {
	t.Helper()
	if p.groupID <= 0 {
		return
	}
	_ = syscall.Kill(-p.groupID, syscall.SIGINT)
	t.Logf("teardown signal exited silo group=%d SIGINT", p.groupID)
	time.Sleep(100 * time.Millisecond)
	_ = syscall.Kill(-p.groupID, syscall.SIGKILL)
	t.Logf("teardown signal exited silo group=%d SIGKILL", p.groupID)
}

func (p *siloProcess) assertListenerOwnership(t *testing.T) {
	t.Helper()
	for _, port := range []int{p.port, p.jellyPort, p.absPort} {
		if !processOwnsListener(p.command.Process.Pid, port) {
			t.Fatalf("Silo PID %d does not own listener %d", p.command.Process.Pid, port)
		}
	}
}

func processOwnsListener(pid, port int) bool {
	inodes := make(map[string]bool)
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 11 || fields[3] != "0A" {
				continue
			}
			address := strings.Split(fields[1], ":")
			if len(address) != 2 {
				continue
			}
			var listenerPort int
			if _, err := fmt.Sscanf(address[1], "%X", &listenerPort); err == nil && listenerPort == port {
				inodes[fields[9]] = true
			}
		}
	}
	entries, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/fd/" + entry.Name())
		if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") && inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			return true
		}
	}
	return false
}

func (p *siloProcess) waitReady(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		response, err := client.Get(p.baseURL + "/api/v1/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Silo readiness: %v\n%s", ctx.Err(), p.logs(t))
		case <-deadline.C:
			t.Fatalf("timed out waiting for Silo readiness\n%s", p.logs(t))
		case <-ticker.C:
		}
	}
}

func (p *siloProcess) logs(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return "<log unavailable>"
	}
	return string(data)
}

func (p *siloProcess) assertAlive(t *testing.T, stage string) {
	t.Helper()
	if p.command.Process == nil {
		t.Fatalf("Silo exited before %s\n%s", stage, p.logs(t))
	}
	select {
	case err := <-p.done:
		p.command.Process = nil
		t.Fatalf("Silo exited before %s after %s: %v\n%s", stage, time.Since(p.startedAt), err, p.logs(t))
	default:
	}
	if err := p.command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("Silo PID %d is not alive before %s after %s: %v\n%s", p.command.Process.Pid, stage, time.Since(p.startedAt), err, p.logs(t))
	}
}

func (p *siloProcess) pluginPID(t *testing.T) int {
	t.Helper()
	output, err := exec.Command("pgrep", "-P", strconv.Itoa(p.command.Process.Pid)).Output()
	if err != nil {
		t.Fatalf("find packaged plugin child PID: %v\n%s", err, p.logs(t))
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	pid, err := strconv.Atoi(first)
	if err != nil || pid <= 0 {
		t.Fatalf("parse packaged plugin PID %q: %v", first, err)
	}
	return pid
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func availablePorts(t *testing.T) (int, int, int) {
	t.Helper()
	ports := make([]int, 0, 3)
	seen := make(map[int]bool)
	for len(ports) < 3 {
		port := availablePort(t)
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports[0], ports[1], ports[2]
}

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 40*time.Minute)
}
