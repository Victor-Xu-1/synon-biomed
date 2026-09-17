//go:build windows

package processsupervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	processSupervisorHelperRole = "SYNON_PROCESS_SUPERVISOR_HELPER_ROLE"
	processSupervisorPIDFile    = "SYNON_PROCESS_SUPERVISOR_PID_FILE"
	processSupervisorSentinel   = "SYNON_PROCESS_SUPERVISOR_SENTINEL_FILE"
)

func TestStartAssignsJobBeforeResumingProcess(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "started")
	command := processSupervisorHelperCommand("sentinel", sentinel)
	api := systemWindowsProcessAPI
	assigned := false
	assign := api.assignProcessToJobObject
	api.assignProcessToJobObject = func(job, process windows.Handle) error {
		if err := assign(job, process); err != nil {
			return err
		}
		assigned = true
		return nil
	}
	resume := api.resumeThread
	api.resumeThread = func(thread windows.Handle) (uint32, error) {
		if !assigned {
			return 0, errors.New("process resumed before job assignment")
		}
		return resume(thread)
	}

	job, err := start(command, api)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if !assigned {
		t.Fatal("process was not assigned to the job")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("supervised command did not run after assignment: %v", err)
	}
}

func TestStartAssignmentFailureDoesNotRunCommand(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "started")
	command := processSupervisorHelperCommand("sentinel", sentinel)
	api := systemWindowsProcessAPI
	wantErr := errors.New("injected assignment failure")
	api.assignProcessToJobObject = func(windows.Handle, windows.Handle) error {
		time.Sleep(100 * time.Millisecond)
		return wantErr
	}

	job, err := start(command, api)
	if job != nil {
		t.Fatal("failed start returned a live job")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("start error = %v, want %v", err, wantErr)
	}
	assertFailedStartReaped(t, command, err)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command executed before failed assignment: %v", err)
	}
}

func TestStartResumeFailureDoesNotLeaveSuspendedProcess(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "started")
	command := processSupervisorHelperCommand("sentinel", sentinel)
	api := systemWindowsProcessAPI
	wantErr := errors.New("injected resume failure")
	api.resumeThread = func(windows.Handle) (uint32, error) {
		return 0, wantErr
	}

	job, err := start(command, api)
	if job != nil {
		t.Fatal("failed start returned a live job")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("start error = %v, want %v", err, wantErr)
	}
	assertFailedStartReaped(t, command, err)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command executed after failed resume: %v", err)
	}
}

func TestJobTerminateKillsDescendantTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command(os.Args[0], "-test.run=^TestProcessSupervisorHelper$")
	command.Env = append(os.Environ(),
		processSupervisorHelperRole+"=parent",
		processSupervisorPIDFile+"="+pidFile,
	)
	job, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	defer job.Terminate()

	childPID := waitForProcessSupervisorChild(t, pidFile)
	if err := job.Terminate(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("supervised parent did not exit after job termination")
	}
	assertWindowsProcessExited(t, childPID)
}

func TestProcessSupervisorHelper(t *testing.T) {
	role := os.Getenv(processSupervisorHelperRole)
	if role == "" {
		return
	}
	if role == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestProcessSupervisorHelper$")
		child.Env = append(os.Environ(), processSupervisorHelperRole+"=child")
		if err := child.Start(); err != nil {
			os.Exit(11)
		}
		if err := os.WriteFile(os.Getenv(processSupervisorPIDFile), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(12)
		}
	}
	if role == "sentinel" {
		if err := os.WriteFile(os.Getenv(processSupervisorSentinel), []byte("started"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(13)
		}
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func processSupervisorHelperCommand(role, sentinel string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestProcessSupervisorHelper$")
	command.Env = append(os.Environ(),
		processSupervisorHelperRole+"="+role,
		processSupervisorSentinel+"="+sentinel,
	)
	return command
}

func assertFailedStartReaped(t *testing.T, command *exec.Cmd, startErr error) {
	t.Helper()
	if command.Process == nil {
		t.Fatal("failed start did not expose the created process")
	}
	if command.ProcessState == nil {
		t.Fatalf("failed start did not record process state for %d: %v", command.Process.Pid, startErr)
	}
	if !command.ProcessState.Exited() {
		t.Fatalf("failed start process %d state is not exited: %s", command.Process.Pid, command.ProcessState)
	}
}

func waitForProcessSupervisorChild(t *testing.T, pidFile string) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			value, parseErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
			if parseErr == nil && value > 0 {
				return uint32(value)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("supervised child did not start")
	return 0
}

func assertWindowsProcessExited(t *testing.T, pid uint32) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant process %d survived job termination", pid)
	}
}
