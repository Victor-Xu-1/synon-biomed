package kernel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagerInterruptsRealCellWithoutLosingKernelState(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-interrupt", FrameID: "frame-interrupt", RootFrameID: "root-interrupt",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(workspaceDir, "interrupt-started")
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-interrupt", Language: "python", Environment: "python",
		ExecID: "exec-interrupt", ToolUseID: "user-exec-interrupt", Origin: "user",
		Code: "from pathlib import Path\nstate_before_interrupt = 41\nPath('interrupt-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, marker)

	interrupted := manager.Interrupt("frame-interrupt", "exec-interrupt")
	if !interrupted.Interrupted || interrupted.Via != "sigint" || interrupted.Dequeued || interrupted.Reason != "" {
		t.Fatalf("interrupt result = %#v", interrupted)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if !outcome.Response.Interrupted || outcome.TimedOut || outcome.Err != nil {
		t.Fatalf("interrupt outcome = %#v", outcome)
	}
	if manager.ActiveCount() != 1 || manager.ActiveExecutionCount() != 0 {
		t.Fatalf("active kernels=%d executions=%d", manager.ActiveCount(), manager.ActiveExecutionCount())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, "print(state_before_interrupt + 1)", "user")
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || strings.TrimSpace(response.Stdout) != "42" {
		t.Fatalf("post-interrupt response = %#v", response)
	}
}

func TestManagerSubmitStartAuthorizationPreventsExecutionAndCleansReservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kernel process confinement is unavailable on Windows")
	}
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "background"}[background], func(t *testing.T) {
			manager := newLifecycleTestManager(t, Config{})
			workspaceDir := t.TempDir()
			if _, err := manager.StartSession(SessionSpec{
				KernelID: "kernel-authorize", FrameID: "frame-authorize", RootFrameID: "root-authorize",
				AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
			}); err != nil {
				t.Fatal(err)
			}
			gate := make(chan error, 1)
			marker := filepath.Join(workspaceDir, "must-not-run")
			handle, err := manager.Submit(SubmitRequest{
				KernelID: "kernel-authorize", FrameID: "frame-authorize", Language: "python", Environment: "python",
				ExecID: "exec-authorize", ToolUseID: "tool-authorize", Origin: "agent", Background: background,
				Code: "from pathlib import Path\nPath('must-not-run').write_text('bad')", StartAuthorization: gate,
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-handle.Started():
				t.Fatal("execution started before authorization")
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker before denial err=%v", err)
			}
			gate <- errors.New("authorization denied")
			close(gate)
			outcome := waitForLifecycleOutcome(t, handle)
			if !outcome.Dequeued || outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "authorization denied") {
				t.Fatalf("authorization outcome=%#v", outcome)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied execution wrote marker err=%v", err)
			}
			if manager.ActiveExecutionCount() != 0 || len(manager.ListExecStreams("frame-authorize")) != 0 {
				t.Fatalf("denied execution leaked registry state")
			}
		})
	}
	t.Run("approved", func(t *testing.T) {
		manager := newLifecycleTestManager(t, Config{})
		workspaceDir := t.TempDir()
		if _, err := manager.StartSession(SessionSpec{
			KernelID: "kernel-approved", FrameID: "frame-approved", RootFrameID: "root-approved",
			AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
		}); err != nil {
			t.Fatal(err)
		}
		gate := make(chan error, 1)
		handle, err := manager.Submit(SubmitRequest{
			KernelID: "kernel-approved", FrameID: "frame-approved", Language: "python", Environment: "python",
			ExecID: "exec-approved", ToolUseID: "tool-approved", Origin: "agent",
			Code: "print('approved')", StartAuthorization: gate,
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-handle.Started():
			t.Fatal("execution started before approval")
		case <-time.After(50 * time.Millisecond):
		}
		gate <- nil
		close(gate)
		outcome := waitForLifecycleOutcome(t, handle)
		if outcome.Err != nil || outcome.Dequeued || strings.TrimSpace(outcome.Response.Stdout) != "approved" {
			t.Fatalf("approved outcome=%#v", outcome)
		}
		if manager.ActiveExecutionCount() != 0 {
			t.Fatalf("approved execution leaked registry state")
		}
	})
	t.Run("interrupted before open", func(t *testing.T) {
		manager := newLifecycleTestManager(t, Config{})
		workspaceDir := t.TempDir()
		if _, err := manager.StartSession(SessionSpec{
			KernelID: "kernel-gated-interrupt", FrameID: "frame-gated-interrupt", RootFrameID: "root-gated-interrupt",
			AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
		}); err != nil {
			t.Fatal(err)
		}
		gate := make(chan error, 1)
		marker := filepath.Join(workspaceDir, "interrupted-must-not-run")
		handle, err := manager.Submit(SubmitRequest{
			KernelID: "kernel-gated-interrupt", FrameID: "frame-gated-interrupt", Language: "python", Environment: "python",
			ExecID: "exec-gated-interrupt", ToolUseID: "tool-gated-interrupt", Origin: "agent", Background: true,
			Code: "from pathlib import Path\nPath('interrupted-must-not-run').write_text('bad')", StartAuthorization: gate,
		})
		if err != nil {
			t.Fatal(err)
		}
		interrupted := manager.Interrupt("frame-gated-interrupt", "exec-gated-interrupt")
		if !interrupted.Dequeued || interrupted.Interrupted {
			t.Fatalf("interrupt result=%#v", interrupted)
		}
		outcome := waitForLifecycleOutcome(t, handle)
		if !outcome.Dequeued || outcome.Err != nil {
			t.Fatalf("interrupt outcome=%#v", outcome)
		}
		gate <- nil
		close(gate)
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted execution wrote marker err=%v", err)
		}
		if manager.ActiveExecutionCount() != 0 || len(manager.ListExecStreams("frame-gated-interrupt")) != 0 {
			t.Fatalf("interrupted execution leaked registry state")
		}
	})
}

func TestManagedExecutionStartAuthorizationRejectsBeforeWorkerIO(t *testing.T) {
	denied := errors.New("authorization denied")
	gate := make(chan error, 1)
	state := &workerLifecycle{backgroundQueued: 1, generation: 1}
	execution := &managedExecution{
		worker: &Worker{workspaceDir: t.TempDir()}, state: state, generation: 1,
		request: SubmitRequest{
			ExecID: "exec-gated-unit", Background: true, StartAuthorization: gate,
		},
		status: "queued", interrupt: make(chan struct{}, 1),
		started: make(chan ExecutionStarted, 1), done: make(chan ExecutionOutcome, 1), finished: make(chan struct{}),
	}
	go execution.run()
	gate <- denied
	close(gate)
	outcome := <-execution.done
	if !outcome.Dequeued || !errors.Is(outcome.Err, denied) || !outcome.StartedAt.IsZero() {
		t.Fatalf("gated outcome=%#v", outcome)
	}
	state.mu.Lock()
	backgroundQueued, current := state.backgroundQueued, state.current
	state.mu.Unlock()
	if backgroundQueued != 0 || current != nil {
		t.Fatalf("gated execution leaked state background=%d current=%#v", backgroundQueued, current)
	}
}

func TestManagerExecutionTimeoutInterruptsCellAndPreservesWorker(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{
		ExecutionTimeout: 150 * time.Millisecond,
		InterruptGrace:   2 * time.Second,
	})
	workspaceDir := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-timeout", FrameID: "frame-timeout", RootFrameID: "root-timeout",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, cancelSetup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSetup()
	setup, err := worker.Execute(setupContext, `
import importlib.abc
import importlib.util
import sys
import time

timeout_state = "retained"

class _DelayedLoader(importlib.abc.Loader):
    def create_module(self, spec):
        return None

    def exec_module(self, module):
        return None

class _DelayedFinder(importlib.abc.MetaPathFinder):
    def find_spec(self, fullname, path=None, target=None):
        if fullname == "delayed_runtime_module":
            time.sleep(0.4)
            return importlib.util.spec_from_loader(fullname, _DelayedLoader())
        return None

sys.meta_path.insert(0, _DelayedFinder())
`, "user")
	if err != nil || setup.Error != "" {
		t.Fatalf("prepare delayed import fixture: response=%#v err=%v", setup, err)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-timeout", Language: "python", Environment: "python",
		ExecID: "exec-timeout", ToolUseID: "user-exec-timeout", Origin: "user",
		// Source preflight can outlive the active execution budget under load.
		// The budget must begin only after the worker confirms that the cell
		// reached its interruptible execution boundary.
		Code: "import delayed_runtime_module\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if !outcome.TimedOut || !outcome.Response.Interrupted || outcome.Err != nil {
		t.Fatalf("timeout outcome = %#v", outcome)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, "print(timeout_state)", "user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Stdout) != "retained" || response.Error != "" {
		t.Fatalf("post-timeout response = %#v", response)
	}
}

func TestManagerInterruptDuringPythonPreflightSettlesWithinGraceWithoutRunningUserCode(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{
		ExecutionTimeout: 5 * time.Second,
		InterruptGrace:   150 * time.Millisecond,
	})
	workspaceDir := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-preflight-interrupt", FrameID: "frame-preflight-interrupt", RootFrameID: "root-preflight-interrupt",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, cancelSetup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSetup()
	setup, err := worker.Execute(setupContext, `
import importlib.abc
import sys
import time
from pathlib import Path

class _SlowCancellationFinder(importlib.abc.MetaPathFinder):
    def find_spec(self, fullname, path=None, target=None):
        if fullname == "slow_preflight_cancel_module":
            Path("slow-preflight-entered").write_text("entered")
            time.sleep(2)
        return None

sys.meta_path.insert(0, _SlowCancellationFinder())
`, "user")
	if err != nil || setup.Error != "" {
		t.Fatalf("prepare slow preflight fixture: response=%#v err=%v", setup, err)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-preflight-interrupt", Language: "python", Environment: "python",
		ExecID: "exec-preflight-interrupt", ToolUseID: "user-exec-preflight-interrupt", Origin: "user",
		Code: "import slow_preflight_cancel_module\nfrom pathlib import Path\nPath('preflight-user-code-ran').write_text('must not execute')",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "slow-preflight-entered"))
	interrupted := manager.Interrupt("frame-preflight-interrupt", "exec-preflight-interrupt")
	if !interrupted.Interrupted || interrupted.Via != "sigint" {
		t.Fatalf("interrupt result=%#v", interrupted)
	}
	select {
	case outcome := <-handle.Done():
		if outcome.Err == nil || outcome.TimedOut || outcome.Dequeued || outcome.Response.ID != "" {
			t.Fatalf("preflight interrupt outcome=%#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel during Python preflight did not settle within the configured interrupt boundary")
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "preflight-user-code-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled preflight executed user code: %v", err)
	}
	if _, ok := <-handle.Started(); ok {
		t.Fatal("cancelled preflight published an execution-start acknowledgement")
	}
	if manager.ActiveExecutionCount() != 0 {
		t.Fatalf("cancelled preflight leaked %d active executions", manager.ActiveExecutionCount())
	}
}

func TestManagerCarriesPendingInterruptAcrossPythonExecutionAcknowledgement(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{
		ExecutionTimeout: 5 * time.Second,
		InterruptGrace:   2 * time.Second,
	})
	workspaceDir := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-preflight-ack", FrameID: "frame-preflight-ack", RootFrameID: "root-preflight-ack",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	setupContext, cancelSetup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSetup()
	setup, err := worker.Execute(setupContext, `
import importlib.abc
import importlib.util
import sys
import time
from pathlib import Path

class _AcknowledgementRaceLoader(importlib.abc.Loader):
    def create_module(self, spec):
        return None

    def exec_module(self, module):
        return None

class _AcknowledgementRaceFinder(importlib.abc.MetaPathFinder):
    def find_spec(self, fullname, path=None, target=None):
        if fullname != "preflight_acknowledgement_module":
            return None
        Path("ack-preflight-entered").write_text("entered")
        while not Path("release-ack-preflight").exists():
            time.sleep(0.01)
        return importlib.util.spec_from_loader(fullname, _AcknowledgementRaceLoader())

sys.meta_path.insert(0, _AcknowledgementRaceFinder())
`, "user")
	if err != nil || setup.Error != "" {
		t.Fatalf("prepare acknowledgement race fixture: response=%#v err=%v", setup, err)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-preflight-ack", Language: "python", Environment: "python",
		ExecID: "exec-preflight-ack", ToolUseID: "user-exec-preflight-ack", Origin: "user",
		Code: "import preflight_acknowledgement_module\nimport time\nfrom pathlib import Path\ntime.sleep(2)\nPath('ack-race-user-code-ran').write_text('must not execute')",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "ack-preflight-entered"))
	interrupted := manager.Interrupt("frame-preflight-ack", "exec-preflight-ack")
	if !interrupted.Interrupted || interrupted.Via != "sigint" {
		t.Fatalf("interrupt result=%#v", interrupted)
	}
	consumeDeadline := time.Now().Add(time.Second)
	for len(handle.execution.interrupt) != 0 && time.Now().Before(consumeDeadline) {
		runtime.Gosched()
	}
	if len(handle.execution.interrupt) != 0 {
		t.Fatal("pre-acknowledgement interrupt was not consumed")
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "release-ack-preflight"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case started, ok := <-handle.Started():
		if !ok || started.ExecID != "exec-preflight-ack" {
			t.Fatalf("execution acknowledgement=%#v open=%t", started, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("released preflight did not acknowledge execution")
	}
	select {
	case outcome := <-handle.Done():
		if outcome.Err != nil || outcome.TimedOut || outcome.Dequeued || !outcome.Response.Interrupted {
			t.Fatalf("pending interrupt outcome=%#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("pending interrupt was not delivered after execution acknowledgement")
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "ack-race-user-code-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending interrupt allowed user code side effect: %v", err)
	}
	reuseContext, cancelReuse := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelReuse()
	response, err := worker.Execute(reuseContext, "print(6 * 7)", "user")
	if err != nil || response.Error != "" || strings.TrimSpace(response.Stdout) != "42" {
		t.Fatalf("worker after pending interrupt: response=%#v err=%v", response, err)
	}
}

func TestManagerListsBoundedLiveStdoutFromRealCell(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-stream", FrameID: "frame-stream", RootFrameID: "root-stream",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-stream", Language: "python", Environment: "python",
		ExecID: "exec-stream", ToolUseID: "tool-stream", Origin: "user",
		Code: "from pathlib import Path\nprint('A' * 10486784 + 'TAIL', flush=True)\nPath('stream-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "stream-started"))
	streams := waitForExecStreams(t, manager, "frame-stream")
	if len(streams) != 1 || streams[0].ToolUseID != "tool-stream" {
		t.Fatalf("live streams = %#v", streams)
	}
	const truncationMarker = "\n…(live stream truncated at 10 MB; full output in tool_result)\n"
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasSuffix(streams[0].Stdout, truncationMarker) && time.Now().Before(deadline) {
		runtime.Gosched()
		streams = manager.ListExecStreams("frame-stream")
		if len(streams) != 1 {
			t.Fatalf("live stream disappeared before stdout was observed: %#v", streams)
		}
	}
	if len(streams[0].Stdout) != maxExecStreamStdoutBytes || !strings.HasSuffix(streams[0].Stdout, truncationMarker) {
		t.Fatalf("bounded stdout length=%d has marker=%t", len(streams[0].Stdout), strings.HasSuffix(streams[0].Stdout, truncationMarker))
	}
	if streams[0].ThroughChunkSequence == 0 || streams[0].StdoutEndByte < streams[0].StdoutStartByte ||
		streams[0].StdoutEndByte-streams[0].StdoutStartByte != uint64(len(streams[0].Stdout)) {
		t.Fatalf("stdout watermarks = %#v", streams[0])
	}
	if result := manager.Interrupt("frame-stream", "exec-stream"); !result.Interrupted {
		t.Fatalf("interrupt result = %#v", result)
	}
	_ = waitForLifecycleOutcome(t, handle)
	if streams := manager.ListExecStreams("frame-stream"); len(streams) != 0 {
		t.Fatalf("completed streams = %#v", streams)
	}
}

func TestManagedExecutionStdoutObserverPublishesSequenceAndUTF8ByteWatermarks(t *testing.T) {
	chunks := make([]ExecStdoutChunk, 0, 2)
	startedAt := time.Date(2026, time.September, 11, 8, 0, 0, 123, time.UTC)
	execution := &managedExecution{
		state:     &workerLifecycle{spec: SessionSpec{RootFrameID: "root-frame"}},
		status:    "running",
		startedAt: startedAt,
		request: SubmitRequest{
			FrameID: "frame", ExecID: "exec", ToolUseID: "tool", ToolName: "python", Background: true,
		},
		stdoutObserver: func(chunk ExecStdoutChunk) { chunks = append(chunks, chunk) },
	}
	execution.appendStdout("A")
	execution.appendStdout("生")

	if len(chunks) != 2 || chunks[0].Sequence != 1 || chunks[0].StartByte != 0 || chunks[0].EndByte != 1 ||
		chunks[1].Sequence != 2 || chunks[1].StartByte != 1 || chunks[1].EndByte != 4 ||
		chunks[1].RootFrameID != "root-frame" || !chunks[1].StartedAt.Equal(startedAt) ||
		!chunks[1].Background || chunks[1].Status != "running" {
		t.Fatalf("chunks=%#v", chunks)
	}
}

func TestManagerRestartCleansOldExecutionAndNamespace(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	oldWorker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-restart", FrameID: "frame-restart", RootFrameID: "root-restart",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "frame-restart", Language: "python", Environment: "python",
		ExecID: "exec-restart", ToolUseID: "user-exec-restart", Origin: "user",
		Code: "from pathlib import Path\nrestart_state = 99\nPath('restart-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "restart-started"))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	newWorker, err := manager.Restart(ctx, "kernel-restart")
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForLifecycleOutcome(t, handle)
	if oldWorker == newWorker || newWorker.Generation() <= oldWorker.Generation() {
		t.Fatalf("restart generations old=%d new=%d", oldWorker.Generation(), newWorker.Generation())
	}
	if manager.ActiveCount() != 1 || manager.ActiveExecutionCount() != 0 {
		t.Fatalf("post-restart active kernels=%d executions=%d", manager.ActiveCount(), manager.ActiveExecutionCount())
	}
	missing := manager.Interrupt("frame-restart", "exec-restart")
	if missing.Interrupted || missing.Reason != "no such terminal cell" {
		t.Fatalf("stale execution interrupt = %#v", missing)
	}
	response, err := newWorker.Execute(ctx, "print('restart_state' in globals())", "user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Stdout) != "False" {
		t.Fatalf("new namespace response = %#v", response)
	}
}

func TestManagerSubmitRejectsExpectedGenerationDriftBeforeExecution(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-generation", FrameID: "frame-generation", RootFrameID: "root-generation",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(SubmitRequest{
		KernelID: "kernel-generation", ExpectedGeneration: worker.Generation() + 1,
		FrameID: "frame-generation", Language: "python", Environment: "python",
		ExecID: "exec-generation", ToolUseID: "tool-generation", Origin: "agent",
		Code: "generation_drift_should_not_run = True",
	}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("generation drift error=%v", err)
	}
	if manager.ActiveExecutionCount() != 0 || len(manager.ListExecStreams("frame-generation")) != 0 {
		t.Fatalf("generation drift registered an execution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, "print('generation_drift_should_not_run' in globals())", "user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Stdout) != "False" {
		t.Fatalf("stale execution mutated worker state: %#v", response)
	}
}

func newLifecycleTestManager(t *testing.T, overrides Config) *Manager {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(repositoryRoot, "assets", "optional")
	overrides.Python = python
	overrides.AssetRoot = assetRoot
	overrides.ManifestPath = filepath.Join(assetRoot, "kernel-compute.manifest.json")
	overrides.WorkerPath = filepath.Join(assetRoot, "kernels", "kernel_worker.py")
	if overrides.ShutdownTimeout <= 0 {
		overrides.ShutdownTimeout = 2 * time.Second
	}
	manager := NewManager(overrides)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
	})
	return manager
}

func waitForLifecycleFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("kernel marker %s was not created", path)
}

func waitForLifecycleOutcome(t *testing.T, handle *ExecutionHandle) ExecutionOutcome {
	t.Helper()
	select {
	case outcome := <-handle.Done():
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("kernel execution did not finish")
		return ExecutionOutcome{}
	}
}

func waitForExecStreams(t *testing.T, manager *Manager, frameID string) []ExecStream {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if streams := manager.ListExecStreams(frameID); len(streams) > 0 {
			return streams
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("frame %s did not expose a live stdout stream", frameID)
	return nil
}
