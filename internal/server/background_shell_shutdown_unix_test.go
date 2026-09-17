//go:build unix

package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServerCloseStopsBackgroundShellProcessTreeAndTask(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	result, err := srv.executeShellTool(context.Background(), "Bash", map[string]any{
		"command":           "sleep 60 & echo $! > shutdown-child.pid; wait",
		"workdir":           ".",
		"description":       "shutdown process tree fixture",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := mapValue(result)
	taskID := stringValue(value["task_id"])
	if taskID == "" {
		t.Fatalf("background shell result = %#v", value)
	}
	pid := waitForServerShellChildPID(t, filepath.Join(root, "shutdown-child.pid"))
	if !serverTestProcessExists(pid) {
		t.Fatalf("background child process %d was not running", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for serverTestProcessExists(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if serverTestProcessExists(pid) {
		t.Fatalf("background child process %d survived Server.Close", pid)
	}
	task, found, err := srv.taskStore.Get(taskID)
	if err != nil || !found || task.Status != "stopped" || task.Metadata["stoppedBy"] != "server_shutdown" {
		t.Fatalf("shutdown task = %#v found=%v error=%v", task, found, err)
	}
	srv.backgroundShellMu.Lock()
	remaining := len(srv.backgroundShells)
	srv.backgroundShellMu.Unlock()
	if remaining != 0 {
		t.Fatalf("background shell registry retained %d entries", remaining)
	}
}

func waitForServerShellChildPID(t *testing.T, path string) int {
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
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("background child PID file %s was not created", path)
	return 0
}

func serverTestProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
