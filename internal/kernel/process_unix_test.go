//go:build !windows

package kernel

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

const workerProcessSignalHelperEnv = "SYNON_WORKER_PROCESS_SIGNAL_HELPER"

const workerProcessContextHelperEnv = "SYNON_WORKER_PROCESS_CONTEXT_HELPER"

func TestWorkerProcessCommandContextKillsRootAndDescendants(t *testing.T) {
	if os.Getenv(workerProcessContextHelperEnv) == "1" {
		runWorkerProcessContextHelper(t)
		return
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := newWorkerProcessCommand(ctx, os.Args[0], "-test.run=^TestWorkerProcessCommandContextKillsRootAndDescendants$")
	command.Env = append(os.Environ(), workerProcessContextHelperEnv+"=1", "SYNON_WORKER_PROCESS_CHILD_PID="+pidPath)
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = process.kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	childPID := waitForWorkerProcessChildPID(t, pidPath, 5*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker root remained alive after context cancellation")
	}
	waitForWorkerProcessGone(t, command.Process.Pid, 5*time.Second)
	waitForWorkerProcessGone(t, childPID, 5*time.Second)
}

func TestWorkerWaitPreservesPhysicalSIGKILLReason(t *testing.T) {
	command := exec.Command("sh", "-c", "kill -KILL $$")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{workers: map[string]*Worker{}}
	worker := &Worker{id: "worker-oom", command: command, manager: manager, done: make(chan struct{})}
	manager.workers[worker.id] = worker
	worker.wait()
	if got := worker.restartReason(nil); got != "was killed — possibly out of memory" {
		t.Fatalf("physical stop reason=%q", got)
	}
	if _, exists := manager.workers[worker.id]; exists {
		t.Fatal("terminal worker remained registered")
	}
}

func TestWorkerWaitClassifiesSupervisorExit137AsPossibleOOM(t *testing.T) {
	command := exec.Command("sh", "-c", "exit 137")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{workers: map[string]*Worker{}}
	worker := &Worker{id: "worker-oom-wrapper", command: command, manager: manager, done: make(chan struct{})}
	manager.workers[worker.id] = worker
	worker.wait()
	if got := worker.restartReason(nil); got != "was killed — possibly out of memory" {
		t.Fatalf("wrapped OOM stop reason=%q", got)
	}
}

func runWorkerProcessContextHelper(t *testing.T) {
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	pidPath := strings.TrimSpace(os.Getenv("SYNON_WORKER_PROCESS_CHILD_PID"))
	if pidPath == "" {
		t.Fatal("worker process child PID path is unavailable")
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestWorkerProcessSignalIncludesRootAndDescendants(t *testing.T) {
	if os.Getenv(workerProcessSignalHelperEnv) == "1" {
		runWorkerProcessSignalHelper(t)
		return
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessSignalIncludesRootAndDescendants$")
	command.Env = append(os.Environ(), workerProcessSignalHelperEnv+"=1", "SYNON_WORKER_PROCESS_CHILD_PID="+pidPath)
	process, err := startWorkerProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = process.kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	childPID := waitForWorkerProcessChildPID(t, pidPath, 5*time.Second)
	if err := process.signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker root remained alive after process-tree interrupt")
	}
	waitForWorkerProcessGone(t, command.Process.Pid, 5*time.Second)
	waitForWorkerProcessGone(t, childPID, 5*time.Second)
}

func runWorkerProcessSignalHelper(t *testing.T) {
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	pidPath := strings.TrimSpace(os.Getenv("SYNON_WORKER_PROCESS_CHILD_PID"))
	if pidPath == "" {
		t.Fatal("worker process child PID path is unavailable")
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func waitForWorkerProcessChildPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
	t.Fatalf("worker child PID was not published at %s", path)
	return 0
}

func waitForWorkerProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after cancellation: %s", pid, fmt.Sprint(syscall.Kill(pid, 0)))
}
