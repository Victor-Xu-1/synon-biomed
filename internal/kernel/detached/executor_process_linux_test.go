//go:build linux

package detached_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernel/detached"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

const (
	detachedControllerHelperEnv = "SYNON_DETACHED_CONTROLLER_HELPER"
	detachedSystemdLauncherEnv  = "SYNON_DETACHED_SYSTEMD_LAUNCHER"
)

type detachedTestLauncher struct{}

func (detachedTestLauncher) Observe(context.Context, string, int64) (detached.ExecutorLaunchState, error) {
	return detached.ExecutorLaunchUnknown, nil
}

func (detachedTestLauncher) Launch(request detached.ExecutorLaunchRequest) error {
	logFile, err := os.OpenFile(request.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(request.Executable, request.Arguments...)
	command.Dir = request.WorkingDirectory
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return command.Process.Release()
}

func detachedControllerLauncher() (detached.ExecutorLauncher, error) {
	if os.Getenv(detachedSystemdLauncherEnv) == "1" {
		return detached.NewSystemdUserExecutorLauncher()
	}
	return detachedTestLauncher{}, nil
}

func detachedControllerSocketRoot(home string) (string, error) {
	if os.Getenv(detachedSystemdLauncherEnv) == "1" {
		return detached.DefaultSharedSocketRoot(home)
	}
	return detached.DefaultSocketRoot(home)
}

func TestMain(m *testing.M) {
	if os.Getenv(detachedControllerHelperEnv) == "1" {
		if err := runDetachedControllerHelper(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDetachedExecutorSurvivesControllerProcessExitAndSettlesAfterRestart(t *testing.T) {
	realBinary := strings.TrimSpace(os.Getenv("SYNON_REAL_BINARY"))
	repositoryRoot := strings.TrimSpace(os.Getenv("SYNON_REPOSITORY_ROOT"))
	if realBinary == "" || repositoryRoot == "" {
		t.Skip("real Synon binary and repository root are required")
	}
	home := t.TempDir()
	workspaceDir := filepath.Join(home, "project-files")
	for _, directory := range []string{
		filepath.Join(home, "workspace"), workspaceDir,
		filepath.Join(home, "sockets"), filepath.Join(home, "executor-logs"),
		filepath.Join(home, "runtime", "skills"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(home, "workspace", "synonbiomed-v1.1.sqlite")
	store, claim, operation := seedDetachedProcessFixture(t, databasePath)
	socketRoot, err := detachedControllerSocketRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	claimJSON, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(),
		detachedControllerHelperEnv+"=1",
		"SYNON_DETACHED_HOME="+home,
		"SYNON_DETACHED_WORKSPACE="+workspaceDir,
		"SYNON_DETACHED_REAL_BINARY="+realBinary,
		"SYNON_DETACHED_REPOSITORY_ROOT="+repositoryRoot,
		"SYNON_DETACHED_CLAIM="+string(claimJSON),
		"SYNON_DETACHED_OPERATION="+operation.OperationID,
	)
	output, err := helper.CombinedOutput()
	if err != nil {
		t.Fatalf("controller helper failed: %v\n%s\nexecutor diagnostics:\n%s",
			err, output, detachedExecutorDiagnostics(home))
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executionID := strings.TrimSpace(readRequiredTestFile(t, filepath.Join(home, "controller-execution-id")))
	execution, found, err := store.GetDetachedKernelExecution(context.Background(), executionID)
	if err != nil || !found || execution.TerminalReceiptID != "" {
		t.Fatalf("execution after controller exit=%#v found=%t err=%v", execution, found, err)
	}
	backend, found, err := store.GetKernelExecutionBackend(context.Background(), execution.BackendID)
	if err != nil || !found || backend.ExecutorPID <= 0 || backend.WorkerPID <= 0 {
		t.Fatalf("backend after controller exit=%#v found=%t err=%v", backend, found, err)
	}
	if os.Getenv(detachedSystemdLauncherEnv) == "1" && !strings.Contains(backend.CgroupPath, "synon-kernel-executor-") {
		t.Fatalf("executor did not enter an independent systemd service: %#v", backend)
	}
	t.Cleanup(func() {
		if backend.WorkerPGID > 0 {
			_ = syscall.Kill(-int(backend.WorkerPGID), syscall.SIGKILL)
		}
		if backend.ExecutorPID > 0 {
			_ = syscall.Kill(-int(backend.ExecutorPID), syscall.SIGKILL)
		}
	})
	if _, err := os.Stat(filepath.Join("/proc", fmt.Sprint(backend.ExecutorPID))); err != nil {
		t.Fatalf("executor did not survive controller exit: %v", err)
	}

	recoveredBackend := &detached.Backend{
		Store: store, Executable: realBinary, HomeDir: home,
		SocketRoot: socketRoot, LogDir: filepath.Join(home, "executor-logs"),
		Launcher: detachedTestLauncher{}, StartTimeout: 30 * time.Second,
	}
	recoveredTranscript, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(server.Options{
		Workspace: store, Transcript: recoveredTranscript,
		KernelExecutionBackend: recoveredBackend, FileRoot: home,
	})
	recoveryCtx, stopRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- app.RunDetachedKernelExecutionRecovery(recoveryCtx) }()

	deadline := time.Now().Add(45 * time.Second)
	var settled workspace.KernelLocalOperation
	for time.Now().Before(deadline) {
		settled, found, err = store.GetKernelLocalOperation(context.Background(), "owner", operation.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if found && settled.State == workspace.KernelLocalOperationStateCompleted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopRecovery()
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if !found || settled.State != workspace.KernelLocalOperationStateCompleted || settled.ExecutionLogID != executionID {
		latestExecution, executionFound, executionErr := store.GetDetachedKernelExecution(context.Background(), executionID)
		latestBackend, backendFound, backendErr := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
		t.Fatalf("settled operation=%#v found=%t\nexecution=%#v found=%t err=%v\nbackend=%#v found=%t err=%v\nexecutor diagnostics:\n%s",
			settled, found, latestExecution, executionFound, executionErr,
			latestBackend, backendFound, backendErr, detachedExecutorDiagnostics(home))
	}
	execution, found, err = store.GetDetachedKernelExecution(context.Background(), executionID)
	if err != nil || !found || execution.TerminalReceiptID == "" ||
		execution.State != workspace.DetachedKernelExecutionStateTerminal {
		t.Fatalf("terminal execution=%#v found=%t err=%v", execution, found, err)
	}
	receipt, found, err := store.GetKernelExecutionResultReceipt(context.Background(), execution.TerminalReceiptID)
	if err != nil || !found || receipt.Outcome != workspace.KernelExecutionResultCompleted {
		t.Fatalf("terminal receipt=%#v found=%t err=%v", receipt, found, err)
	}
	settledBackend, found, err := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
	if err != nil || !found || settledBackend.ControllerEpoch != 2 {
		t.Fatalf("settled backend controller epoch=%d found=%t err=%v", settledBackend.ControllerEpoch, found, err)
	}
	logRecord, found, err := store.GetExecutionLog(settled.FrameID, executionID)
	if err != nil || !found || !strings.Contains(logRecord.Stdout, "SURVIVED_CONTROLLER_RESTART") {
		t.Fatalf("execution log=%#v found=%t err=%v", logRecord, found, err)
	}

	ref := kernelruntime.BackendSessionRef{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, SocketPath: backend.SocketPath,
	}
	lease, err := recoveredBackend.AcquireSessionControl(context.Background(), ref, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveredBackend.CloseSession(context.Background(), ref, lease); err != nil {
		t.Fatal(err)
	}
	waitForProcessExit(t, int(backend.ExecutorPID), 15*time.Second)
}

func TestDetachedExecutorConsumesPersistedCancellationAndKillsProcessTree(t *testing.T) {
	testDetachedExecutorTerminalTaskCleanup(t, terminalTaskCleanupCase{})
}

func TestDetachedExecutorReapsFailedTaskAndPersistsLateResult(t *testing.T) {
	testDetachedExecutorTerminalTaskCleanup(t, terminalTaskCleanupCase{failed: true})
}

func TestDetachedExecutorCancelsAcceptedWorkWithoutDispatch(t *testing.T) {
	testDetachedExecutorTerminalTaskCleanup(t, terminalTaskCleanupCase{failed: true, queued: true})
}

func TestDetachedExecutorRecoversAcknowledgementWithoutReceipt(t *testing.T) {
	testDetachedExecutorTerminalTaskCleanup(t, terminalTaskCleanupCase{failed: true, queued: true, acknowledged: true})
}

type terminalTaskCleanupCase struct {
	failed, queued, acknowledged bool
}

func testDetachedExecutorTerminalTaskCleanup(t *testing.T, test terminalTaskCleanupCase) {
	t.Helper()
	failed, queued := test.failed, test.queued
	realBinary := strings.TrimSpace(os.Getenv("SYNON_REAL_BINARY"))
	repositoryRoot := strings.TrimSpace(os.Getenv("SYNON_REPOSITORY_ROOT"))
	if realBinary == "" || repositoryRoot == "" {
		t.Skip("real Synon binary and repository root are required")
	}
	home := t.TempDir()
	workspaceDir := filepath.Join(home, "project-files")
	for _, directory := range []string{
		filepath.Join(home, "workspace"), workspaceDir,
		filepath.Join(home, "sockets"), filepath.Join(home, "executor-logs"),
		filepath.Join(home, "runtime", "skills"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(home, "workspace", "synonbiomed-v1.1.sqlite")
	code := `import subprocess, sys, threading, time
child_code = "import subprocess, sys, time; from pathlib import Path; subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(300)'], start_new_session=True); Path('process-tree-ready').write_text('ready'); time.sleep(300)"
def spawn():
    subprocess.Popen([sys.executable, "-c", child_code], start_new_session=True).wait()
threading.Thread(target=spawn, daemon=True).start()
time.sleep(300)`
	if queued {
		code = `from pathlib import Path; Path('must-not-run').write_text('unexpected dispatch')`
	}
	store, claim, operation := seedDetachedProcessFixtureWithCode(
		t, databasePath, "python-cancel-tree", code,
	)
	claimJSON, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(),
		detachedControllerHelperEnv+"=1",
		"SYNON_DETACHED_HOME="+home,
		"SYNON_DETACHED_WORKSPACE="+workspaceDir,
		"SYNON_DETACHED_REAL_BINARY="+realBinary,
		"SYNON_DETACHED_REPOSITORY_ROOT="+repositoryRoot,
		"SYNON_DETACHED_CLAIM="+string(claimJSON),
		"SYNON_DETACHED_OPERATION="+operation.OperationID,
	)
	if queued {
		helper.Env = append(helper.Env, "SYNON_DETACHED_ACCEPT_ONLY=1")
	}
	output, err := helper.CombinedOutput()
	if err != nil {
		t.Fatalf("controller helper failed: %v\n%s\nexecutor diagnostics:\n%s",
			err, output, detachedExecutorDiagnostics(home))
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executionID := strings.TrimSpace(readRequiredTestFile(t, filepath.Join(home, "controller-execution-id")))
	execution, found, err := store.GetDetachedKernelExecution(context.Background(), executionID)
	expectedState := workspace.DetachedKernelExecutionStateStarted
	if queued {
		expectedState = workspace.DetachedKernelExecutionStateAccepted
	}
	if err != nil || !found || execution.State != expectedState {
		t.Fatalf("active execution=%#v found=%t err=%v", execution, found, err)
	}
	backend, found, err := store.GetKernelExecutionBackend(context.Background(), execution.BackendID)
	if err != nil || !found || backend.ExecutorPID <= 0 || backend.WorkerPID <= 0 {
		t.Fatalf("active backend=%#v found=%t err=%v", backend, found, err)
	}
	t.Cleanup(func() {
		if backend.WorkerPGID > 0 {
			_ = syscall.Kill(-int(backend.WorkerPGID), syscall.SIGKILL)
		}
		if backend.ExecutorPID > 0 {
			_ = syscall.Kill(-int(backend.ExecutorPID), syscall.SIGKILL)
		}
	})
	var descendants []int
	if !queued {
		readyDeadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(workspaceDir, "process-tree-ready")); err == nil {
				break
			}
			if time.Now().After(readyDeadline) {
				t.Fatal("nested process tree never reached execution readiness")
			}
			time.Sleep(10 * time.Millisecond)
		}
		descendants = waitForProcessDescendants(t, int(backend.WorkerPID), 4, 15*time.Second)
	}

	if failed {
		if test.acknowledged {
			// Simulate the interruption window after a durable acknowledgement
			// but before any observer or terminal receipt exists.
			_, lease, err := store.AcquireKernelExecutionBackendControl(context.Background(), workspace.AcquireKernelExecutionBackendControlInput{
				BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
				Token: strings.Repeat("test-cancel-control-", 3), LeaseExpiresAt: time.Now().Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			requested, err := store.RequestKernelExecutionCancel(context.Background(), workspace.RequestKernelExecutionCancelInput{
				KernelDetachedExecutionControlInput: workspace.KernelDetachedExecutionControlInput{
					ExecutionID: executionID, BackendGeneration: backend.BackendGeneration,
					ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: execution.StateVersion,
				}, CancelRequestID: "queued-cancel", Reason: "user_stop",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcknowledgeKernelExecutionCancel(context.Background(), workspace.AcknowledgeKernelExecutionCancelInput{
				ExecutionID: executionID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID,
				ExpectedVersion: requested.StateVersion, CancelRequestID: requested.CancelRequestID, AckSequence: 1, Signal: "dequeue",
			}); err != nil {
				t.Fatal(err)
			}
		}
		status := workspace.FrameStatusFailed
		if _, err := store.UpdateFrame(operation.FrameID, workspace.UpdateFrameInput{Status: &status}); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, _, err := store.CancelFrameWithTranscript(context.Background(), operation.FrameID); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		execution, found, err = store.GetDetachedKernelExecution(context.Background(), executionID)
		if err != nil {
			t.Fatal(err)
		}
		if found && execution.State == workspace.DetachedKernelExecutionStateTerminal {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	expectedMethod, expectedSignal := "sigint", "SIGINT"
	if queued {
		expectedMethod, expectedSignal = "dequeue", ""
	}
	if !found || execution.State != workspace.DetachedKernelExecutionStateTerminal ||
		execution.CancelAckAt == nil || execution.CancelSignal != expectedMethod || execution.TerminalReceiptID == "" {
		t.Fatalf("cancelled execution=%#v found=%t err=%v\nexecutor diagnostics:\n%s",
			execution, found, err, detachedExecutorDiagnostics(home))
	}
	receipt, found, err := store.GetKernelExecutionResultReceipt(context.Background(), execution.TerminalReceiptID)
	if err != nil || !found || receipt.Outcome != workspace.KernelExecutionResultCancelled || !receipt.Interrupted ||
		receipt.TerminationSignal != expectedSignal {
		t.Fatalf("cancel receipt=%#v found=%t err=%v", receipt, found, err)
	}
	for _, pid := range descendants {
		waitForProcessExit(t, pid, 10*time.Second)
	}

	recoveredBackend := &detached.Backend{
		Store: store, Executable: realBinary, HomeDir: home,
		SocketRoot: mustDetachedSocketRoot(home), LogDir: filepath.Join(home, "executor-logs"),
		Launcher: detachedTestLauncher{}, StartTimeout: 30 * time.Second,
	}
	recoveredTranscript, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(server.Options{
		Workspace: store, Transcript: recoveredTranscript,
		KernelExecutionBackend: recoveredBackend, FileRoot: home,
	})
	recoveryCtx, stopRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- app.RunDetachedKernelExecutionRecovery(recoveryCtx) }()
	var settled workspace.KernelLocalOperation
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		settled, found, err = store.GetKernelLocalOperation(context.Background(), "owner", operation.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if found && settled.State == workspace.KernelLocalOperationStateCancelled {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopRecovery()
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if !found || settled.State != workspace.KernelLocalOperationStateCancelled || settled.ExecutionLogID != executionID {
		latestExecution, executionFound, executionErr := store.GetDetachedKernelExecution(context.Background(), executionID)
		latestBackend, backendFound, backendErr := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
		latestReceipt, receiptFound, receiptErr := store.GetKernelExecutionResultReceipt(
			context.Background(), latestExecution.TerminalReceiptID,
		)
		t.Fatalf("settled cancelled operation=%#v found=%t err=%v\nexecution=%#v found=%t err=%v\nreceipt=%#v found=%t err=%v\nbackend=%#v found=%t err=%v",
			settled, found, err, latestExecution, executionFound, executionErr,
			latestReceipt, receiptFound, receiptErr, latestBackend, backendFound, backendErr)
	}
	logRecord, found, err := store.GetExecutionLog(settled.FrameID, executionID)
	if err != nil || !found || logRecord.ExitStatus != "cancelled" {
		t.Fatalf("cancelled execution log=%#v found=%t err=%v", logRecord, found, err)
	}

	stoppedBackend, found, err := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
	if err != nil || !found || stoppedBackend.State != workspace.KernelExecutionBackendStateStopped {
		t.Fatalf("cancelled backend=%#v found=%t err=%v", stoppedBackend, found, err)
	}
	waitForProcessExit(t, int(backend.ExecutorPID), 15*time.Second)
	if queued {
		if _, err := os.Stat(filepath.Join(workspaceDir, "must-not-run")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled queued code ran: %v", err)
		}
	}
	if failed {
		frame, found, err := store.GetFrame(operation.FrameID)
		if err != nil || !found || frame.Status != workspace.FrameStatusFailed {
			t.Fatal("late cleanup rewrote original task failure")
		}
	}
}

func TestDefaultSocketRootIsStableDistinctAndBounded(t *testing.T) {
	first, err := detached.DefaultSocketRoot("/home/example/.synon-biomed-v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := detached.DefaultSocketRoot("/home/example/.synon-biomed-v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	other, err := detached.DefaultSocketRoot("/home/example/other-installation")
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated || first == other || !filepath.IsAbs(first) {
		t.Fatalf("socket roots first=%q repeated=%q other=%q", first, repeated, other)
	}
	longest := filepath.Join(first, "kernel-backend-"+strings.Repeat("0", 36)+".sock")
	if len([]byte(longest)) > 107 {
		t.Fatalf("socket path exceeds Linux sockaddr_un budget: %d %q", len([]byte(longest)), longest)
	}
}

func seedDetachedProcessFixture(
	t *testing.T,
	databasePath string,
) (*workspace.Store, transcriptstore.RunnerClaim, workspace.KernelLocalOperation) {
	return seedDetachedProcessFixtureWithCode(
		t, databasePath, "python-restart",
		"import time; time.sleep(5); print('SURVIVED_CONTROLLER_RESTART'); print('X' * 1100000)",
	)
}

func seedDetachedProcessFixtureWithCode(
	t *testing.T,
	databasePath string,
	toolCallID string,
	code string,
) (*workspace.Store, transcriptstore.RunnerClaim, workspace.KernelLocalOperation) {
	t.Helper()
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Run a restart-safe Python cell.",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	payload, err := json.Marshal(map[string]any{
		"status": "running", "modelToolCalls": []any{map[string]any{
			"id": toolCallID, "type": "function", "name": "python",
			"arguments": map[string]any{
				"code":        code,
				"environment": "python", "background": true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operations []workspace.KernelLocalOperation
	_, _, _, err = repository.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "checkpoint-restart", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			operations, err = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			receipt := transcriptstore.RunnerCheckpointCommitReceipt{
				ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{BatchID: batch.BatchID, CallCount: batch.CallCount},
			}
			for _, operation := range operations {
				receipt.KernelOperations = append(receipt.KernelOperations,
					transcriptstore.RunnerCheckpointKernelOperationReceipt{
						OperationID: operation.OperationID, Ordinal: operation.ToolCallOrdinal,
						ToolCallID: operation.ToolCallID, Tool: operation.Tool, InputSHA256: operation.InputSHA256,
						ApprovalRequestID:   operation.ApprovalRequestID,
						RequestedEventID:    "kernel-local-operation-requested:" + operation.OperationID,
						InitialStateVersion: operation.StateVersion,
					},
				)
			}
			return receipt, nil
		},
	})
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true, DecisionID: "approve-restart",
		Scope: "once", Source: "user", ActorID: "owner", CurrentClaim: claimed.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, claimed.Claim, approved
}

func runDetachedControllerHelper() error {
	home := filepath.Clean(os.Getenv("SYNON_DETACHED_HOME"))
	workspaceDir := filepath.Clean(os.Getenv("SYNON_DETACHED_WORKSPACE"))
	realBinary := filepath.Clean(os.Getenv("SYNON_DETACHED_REAL_BINARY"))
	repositoryRoot := filepath.Clean(os.Getenv("SYNON_DETACHED_REPOSITORY_ROOT"))
	operationID := strings.TrimSpace(os.Getenv("SYNON_DETACHED_OPERATION"))
	if !filepath.IsAbs(home) || !filepath.IsAbs(workspaceDir) || !filepath.IsAbs(realBinary) ||
		!filepath.IsAbs(repositoryRoot) || operationID == "" {
		return errors.New("controller helper environment is incomplete")
	}
	if err := os.Chdir(repositoryRoot); err != nil {
		return err
	}
	var claim transcriptstore.RunnerClaim
	if err := json.Unmarshal([]byte(os.Getenv("SYNON_DETACHED_CLAIM")), &claim); err != nil {
		return err
	}
	store, err := workspace.Open(filepath.Join(home, "workspace", "synonbiomed-v1.1.sqlite"))
	if err != nil {
		return err
	}
	defer store.Close()
	operation, found, err := store.GetKernelLocalOperation(context.Background(), "owner", operationID)
	if err != nil || !found {
		return errors.New("approved kernel operation is unavailable")
	}
	var toolInput struct {
		Code        string `json:"code"`
		Environment string `json:"environment"`
		Background  bool   `json:"background"`
	}
	if err := json.Unmarshal(operation.InputJSON, &toolInput); err != nil || strings.TrimSpace(toolInput.Code) == "" ||
		strings.TrimSpace(toolInput.Environment) == "" || !toolInput.Background {
		return errors.New("approved kernel operation input is invalid")
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), operation.FrameID)
	if err != nil || !found {
		return errors.New("kernel frame access is unavailable")
	}
	spec := kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, KernelKind: "operon", Language: "python",
		Environment: "python", WorkspaceDir: workspaceDir,
		Mounts: []kernelruntime.WorkerMount{
			kernelruntime.TrustedReadOnlyDirectoryMount(filepath.Join(home, "runtime", "skills")),
		},
		ProtectedPaths:       []string{filepath.Join(home, "runtime")},
		EgressAllowedDomains: []string{"example.com"},
	}
	launcher, err := detachedControllerLauncher()
	if err != nil {
		return err
	}
	socketRoot, err := detachedControllerSocketRoot(home)
	if err != nil {
		return err
	}
	backend := &detached.Backend{
		Store: store, Executable: realBinary, HomeDir: home,
		SocketRoot: socketRoot, LogDir: filepath.Join(home, "executor-logs"),
		Launcher: launcher, StartTimeout: 30 * time.Second,
	}
	session, err := backend.EnsureSession(context.Background(), spec)
	if err != nil {
		return err
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), workspace.PrepareKernelLocalOperationInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, Claim: claim, BootID: "controller-before-restart",
		KernelID: session.KernelID, KernelGeneration: session.KernelGeneration,
		ConfinementSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		return err
	}
	lease, err := backend.AcquireSessionControl(context.Background(), session, time.Minute)
	if err != nil {
		return err
	}
	executionID := "execution-process-restart"
	started, execution, err := store.StartDetachedKernelLocalOperation(context.Background(), workspace.StartDetachedKernelLocalOperationInput{
		Start: workspace.StartKernelLocalOperationInput{
			OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
			ExpectedStateVersion: prepared.StateVersion, Claim: claim, BootID: prepared.BootID,
			ExecutionID: executionID,
		},
		BackendID: session.BackendID, BackendGeneration: session.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
		Request: workspace.KernelDetachedExecutionRequestV1{
			Version: 1, OperationID: prepared.OperationID, ExecutionID: executionID,
			OwnerUserID: prepared.OwnerUserID, ProjectID: prepared.ProjectID,
			RootFrameID: prepared.RootFrameID, RootFrameIncarnationID: prepared.RootFrameIncarnationID,
			FrameID: prepared.FrameID, FrameIncarnationID: prepared.FrameIncarnationID,
			KernelID: session.KernelID, KernelGeneration: session.KernelGeneration,
			ToolCallID: prepared.ToolCallID, ToolName: prepared.Tool, Language: "python",
			KernelKind: "operon", Environment: toolInput.Environment,
			Code:       toolInput.Code,
			Background: true, OutputLimitBytes: 1 << 20, Origin: "agent",
		},
	})
	if err != nil || started.State != workspace.KernelLocalOperationStateStarted {
		return fmt.Errorf("start detached operation: %w", err)
	}
	ref := kernelruntime.BackendExecutionRef{
		ExecutionID: execution.ExecutionID, OperationID: execution.OperationID,
		BackendID: execution.BackendID, BackendGeneration: execution.BackendGeneration,
		RequestSHA256: execution.RequestSHA256, ConfinementSHA256: execution.ConfinementSHA256,
	}
	if os.Getenv("SYNON_DETACHED_ACCEPT_ONLY") == "1" {
		return os.WriteFile(filepath.Join(home, "controller-execution-id"), []byte(executionID+"\n"), 0o600)
	}
	if _, err := backend.Start(context.Background(), ref, kernelruntime.BackendStartFence{
		ExecutionStateVersion: execution.StateVersion, ControllerEpoch: lease.Epoch,
		ControllerToken: lease.Token, DispatchSequence: 1,
	}); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "controller-execution-id"), []byte(executionID+"\n"), 0o600)
}

func mustDetachedSocketRoot(home string) string {
	root, err := detached.DefaultSocketRoot(home)
	if err != nil {
		panic(err)
	}
	return root
}

func readRequiredTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func detachedExecutorDiagnostics(home string) string {
	paths, err := filepath.Glob(filepath.Join(home, "executor-logs", "*.log"))
	if err != nil {
		return "glob executor logs: " + err.Error()
	}
	if len(paths) == 0 {
		return "no executor logs"
	}
	var diagnostics strings.Builder
	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			_, _ = fmt.Fprintf(&diagnostics, "%s: %v\n", filepath.Base(path), readErr)
			continue
		}
		const maxDiagnosticBytes = 64 << 10
		if len(raw) > maxDiagnosticBytes {
			raw = raw[len(raw)-maxDiagnosticBytes:]
		}
		_, _ = fmt.Fprintf(&diagnostics, "== %s ==\n%s\n", filepath.Base(path), raw)
	}
	return diagnostics.String()
}

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/proc", fmt.Sprint(pid))); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	status, _ := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "status"))
	command, _ := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "cmdline"))
	t.Fatalf("process %d did not exit\nstatus:\n%s\ncmdline: %q", pid, status, command)
}

func waitForProcessDescendants(t *testing.T, root, minimum int, timeout time.Duration) []int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result := processDescendants(root)
		if len(result) >= minimum {
			return result
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d did not create at least %d descendants", root, minimum)
	return nil
}

func processDescendants(root int) []int {
	result := []int{}
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		threads, err := os.ReadDir(filepath.Join("/proc", fmt.Sprint(parent), "task"))
		if err != nil {
			continue
		}
		for _, thread := range threads {
			raw, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(parent), "task", thread.Name(), "children"))
			if err != nil {
				continue
			}
			for _, field := range strings.Fields(string(raw)) {
				var child int
				if _, err := fmt.Sscan(field, &child); err != nil || child <= 0 || seen[child] {
					continue
				}
				seen[child] = true
				result = append(result, child)
				queue = append(queue, child)
			}
		}
	}
	return result
}
