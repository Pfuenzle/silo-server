//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const siloTestProcessRegistryEnv = "SILO_TEST_PROCESS_REGISTRY"

func siloProcessSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func registerSiloTestProcessGroup(groupID int) error {
	registryDir := os.Getenv(siloTestProcessRegistryEnv)
	if registryDir == "" {
		return nil
	}
	startTime, err := siloProcessStartTime(groupID)
	if err != nil {
		return err
	}
	entryPath := filepath.Join(registryDir, strconv.Itoa(groupID))
	entry, err := os.OpenFile(entryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create Silo process registry entry: %w", err)
	}
	if _, err := entry.WriteString(startTime + "\n"); err != nil {
		_ = entry.Close()
		_ = os.Remove(entryPath)
		return fmt.Errorf("write Silo process registry entry: %w", err)
	}
	if err := entry.Close(); err != nil {
		_ = os.Remove(entryPath)
		return fmt.Errorf("close Silo process registry entry: %w", err)
	}
	return nil
}

func unregisterSiloTestProcessGroup(groupID int) {
	registryDir := os.Getenv(siloTestProcessRegistryEnv)
	if registryDir == "" || groupID <= 0 {
		return
	}
	_ = os.Remove(filepath.Join(registryDir, strconv.Itoa(groupID)))
}

func siloProcessStartTime(pid int) (string, error) {
	if pid <= 1 {
		return "", fmt.Errorf("invalid Silo process ID %d", pid)
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", fmt.Errorf("read Silo process stat: %w", err)
	}
	_, fieldsText, found := strings.Cut(string(stat), ") ")
	if !found {
		return "", fmt.Errorf("parse Silo process stat")
	}
	fields := strings.Fields(fieldsText)
	if len(fields) <= 19 || fields[19] == "0" {
		return "", fmt.Errorf("parse Silo process start time")
	}
	return fields[19], nil
}
