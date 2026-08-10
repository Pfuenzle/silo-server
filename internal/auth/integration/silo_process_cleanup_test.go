//go:build integration

package integration

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSiloProcessStop_CleansReadyProcessGroups(t *testing.T) {
	tests := []struct {
		name         string
		ignoreSIGINT bool
	}{
		{name: "normal"},
		{name: "forced", ignoreSIGINT: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			process := startReadyProcessGroup(t, test.ignoreSIGINT)
			previousGrace := siloShutdownGrace
			if test.ignoreSIGINT {
				siloShutdownGrace = 10 * time.Millisecond
			} else {
				siloShutdownGrace = time.Second
			}
			t.Cleanup(func() { siloShutdownGrace = previousGrace })

			// When
			started := time.Now()
			process.stop(t)

			// Then
			if err := syscall.Kill(-process.groupID, syscall.Signal(0)); err != syscall.ESRCH {
				t.Fatalf("process group survived cleanup: %v", err)
			}
			if process.forced != test.ignoreSIGINT {
				t.Fatalf("forced cleanup = %t, want %t", process.forced, test.ignoreSIGINT)
			}
			if test.ignoreSIGINT && time.Since(started) < siloShutdownGrace {
				t.Fatalf("stubborn process exited before forced-cleanup grace elapsed")
			}
		})
	}
}

func TestSiloProcessRegistry_ConcurrentEntriesUnregisterIndependently(t *testing.T) {
	registryDir := filepath.Join(t.TempDir(), "process-groups")
	if err := os.Mkdir(registryDir, 0o700); err != nil {
		t.Fatalf("create process registry: %v", err)
	}
	t.Setenv(siloTestProcessRegistryEnv, registryDir)

	processes := make([]*exec.Cmd, 0, 4)
	for range 4 {
		command := exec.Command("sleep", "60")
		command.SysProcAttr = siloProcessSysProcAttr()
		if err := command.Start(); err != nil {
			t.Fatalf("start registry fixture: %v", err)
		}
		processes = append(processes, command)
	}
	t.Cleanup(func() {
		for _, command := range processes {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Wait()
		}
	})

	var registrations sync.WaitGroup
	for _, command := range processes {
		registrations.Go(func() {
			if err := registerSiloTestProcessGroup(command.Process.Pid); err != nil {
				t.Errorf("register process group: %v", err)
			}
		})
	}
	registrations.Wait()

	for _, command := range processes {
		entry, err := os.ReadFile(filepath.Join(registryDir, strconv.Itoa(command.Process.Pid)))
		if err != nil {
			t.Fatalf("read registry entry: %v", err)
		}
		startTime, err := siloProcessStartTime(command.Process.Pid)
		if err != nil {
			t.Fatalf("read process start time: %v", err)
		}
		if got := strings.TrimSpace(string(entry)); got != startTime {
			t.Fatalf("registry start time = %q, want %q", got, startTime)
		}
	}

	unregisterSiloTestProcessGroup(processes[0].Process.Pid)
	if _, err := os.Stat(filepath.Join(registryDir, strconv.Itoa(processes[0].Process.Pid))); !os.IsNotExist(err) {
		t.Fatalf("unregistered entry still exists: %v", err)
	}
	for _, command := range processes[1:] {
		if _, err := os.Stat(filepath.Join(registryDir, strconv.Itoa(command.Process.Pid))); err != nil {
			t.Fatalf("independent entry missing: %v", err)
		}
	}
}

func TestSiloProcessLaunch_RegistersDedicatedParentBoundGroup(t *testing.T) {
	// Given
	registryPath := filepath.Join(t.TempDir(), "process-groups")
	if err := os.Mkdir(registryPath, 0o700); err != nil {
		t.Fatalf("create process registry: %v", err)
	}
	t.Setenv("SILO_TEST_PROCESS_REGISTRY", registryPath)

	// When
	if err := registerSiloTestProcessGroup(4242); err != nil {
		t.Fatalf("register Silo process group: %v", err)
	}
	expectedStartTime, err := siloProcessStartTime(4242)
	if err != nil {
		t.Fatalf("read registered process start time: %v", err)
	}
	attributes := siloProcessSysProcAttr()

	// Then
	registry, err := os.ReadFile(filepath.Join(registryPath, "4242"))
	if err != nil {
		t.Fatalf("read process registry: %v", err)
	}
	if got := strings.TrimSpace(string(registry)); got != expectedStartTime {
		t.Fatalf("registered process start time = %q, want %q", got, expectedStartTime)
	}
	if !attributes.Setpgid {
		t.Fatal("Silo launch does not create a dedicated process group")
	}
	if attributes.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Silo parent-death signal = %v, want SIGKILL", attributes.Pdeathsig)
	}
}

func TestSiloProcessLaunch_KillsChildWhenParentExits(t *testing.T) {
	if os.Getenv("SILO_TEST_PARENT_HELPER") == "1" {
		startParentDeathChild(t)
		return
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	parent := exec.Command(os.Args[0], "-test.run=^TestSiloProcessLaunch_KillsChildWhenParentExits$")
	parent.Env = append(os.Environ(), "SILO_TEST_PARENT_HELPER=1", "SILO_TEST_PARENT_READY="+readyPath)
	if output, err := parent.CombinedOutput(); err != nil {
		t.Fatalf("start parent-death helper: %v\n%s", err, output)
	}

	portBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatalf("read child listener port: %v", err)
	}
	port, err := strconv.Atoi(string(portBytes))
	if err != nil {
		t.Fatalf("parse child listener port %q: %v", portBytes, err)
	}
	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(time.Second)
	for {
		listener, listenErr := net.Listen("tcp", address)
		if listenErr == nil {
			if closeErr := listener.Close(); closeErr != nil {
				t.Fatalf("close replacement listener: %v", closeErr)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child listener %s survived parent exit: %v", address, listenErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func startParentDeathChild(t *testing.T) {
	t.Helper()
	readyPath := os.Getenv("SILO_TEST_PARENT_READY")
	child := exec.Command("python3", "-c", "import socket,sys,time; listener=socket.socket(); listener.bind(('127.0.0.1', 0)); listener.listen(); open(sys.argv[1], 'w').write(str(listener.getsockname()[1])); time.sleep(60)", readyPath)
	child.SysProcAttr = siloProcessSysProcAttr()
	if err := child.Start(); err != nil {
		t.Fatalf("start parent-death child: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("parent-death child did not report listener readiness")
		}
		time.Sleep(time.Millisecond)
	}
}

func startReadyProcessGroup(t *testing.T, ignoreSIGINT bool) *siloProcess {
	t.Helper()
	readyPath := filepath.Join(t.TempDir(), "ready")
	signalSetup := ""
	if ignoreSIGINT {
		signalSetup = "signal.signal(signal.SIGINT, signal.SIG_IGN); "
	}
	command := exec.Command("python3", "-c", "import os,signal,time; "+signalSetup+"open(os.environ['SILO_TEST_READY'], 'w').close(); time.sleep(60)")
	command.Env = append(os.Environ(), "SILO_TEST_READY="+readyPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start ready process group: %v", err)
	}
	logFile, err := os.Open(os.DevNull)
	if err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		t.Fatalf("open null log file: %v", err)
	}
	process := &siloProcess{command: command, groupID: command.Process.Pid, done: make(chan error, 1), logFile: logFile}
	go func() { process.done <- command.Wait() }()
	t.Cleanup(func() { process.stop(t) })
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return process
		}
		select {
		case <-deadline.C:
			t.Fatal("process group did not report readiness")
		case <-ticker.C:
		}
	}
}
