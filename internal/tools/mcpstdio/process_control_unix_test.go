//go:build unix

package mcpstdio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	processTreeParentFixture = "GO_WANT_MCP_PROCESS_TREE_PARENT"
	processTreeChildFixture  = "GO_WANT_MCP_PROCESS_TREE_CHILD"
	processTreePIDFile       = "SYNON_MCP_PROCESS_TREE_PID_FILE"
)

func TestMCPContextCancellationKillsProcessTree(t *testing.T) {
	if os.Getenv(processTreeChildFixture) == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv(processTreeParentFixture) == "1" {
		runMCPProcessTreeParentFixture()
		return
	}

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	session, err := startSession(ctx, t.TempDir(), ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPContextCancellationKillsProcessTree", "--"},
		Env: map[string]string{
			processTreeParentFixture: "1",
			processTreePIDFile:       pidFile,
		},
	})
	if err != nil {
		t.Fatalf("start MCP process tree fixture: %v", err)
	}
	defer session.close()
	childPID := waitForMCPChildPID(t, pidFile)
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("MCP child process %d survived context cancellation", childPID)
	}
}

func runMCPProcessTreeParentFixture() {
	command := exec.Command(os.Args[0], "-test.run=TestMCPContextCancellationKillsProcessTree", "--")
	command.Env = append(os.Environ(), processTreeChildFixture+"=1")
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv(processTreePIDFile), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		_ = command.Process.Kill()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForMCPChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("MCP child PID file %s was not created", path)
	return 0
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
