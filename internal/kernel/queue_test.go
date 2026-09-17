package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerDequeuesQueuedCellWithoutExecutingIt(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-queue", FrameID: "frame-queue", RootFrameID: "root-queue",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Submit(SubmitRequest{
		FrameID: "frame-queue", Language: "python", Environment: "python",
		ExecID: "exec-running", ToolUseID: "user-exec-running", Origin: "user",
		Code: "from pathlib import Path\nPath('queue-running').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "queue-running"))
	queued, err := manager.Submit(SubmitRequest{
		FrameID: "frame-queue", Language: "python", Environment: "python",
		ExecID: "exec-queued", ToolUseID: "user-exec-queued", Origin: "user",
		Code: "from pathlib import Path\nPath('queue-should-not-run').write_text('bad')",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Interrupt("frame-queue", "exec-queued")
	if result.Interrupted || !result.Dequeued || result.Via != "" || result.Reason != "cell had not started \u2014 dequeued" {
		t.Fatalf("queued interrupt = %#v", result)
	}
	outcome := waitForLifecycleOutcome(t, queued)
	if !outcome.Dequeued || outcome.Err != nil || !outcome.StartedAt.IsZero() {
		t.Fatalf("queued outcome = %#v", outcome)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "queue-should-not-run")); !os.IsNotExist(err) {
		t.Fatalf("queued cell unexpectedly executed: %v", err)
	}
	if repeated := manager.Interrupt("frame-queue", "exec-queued"); repeated.Reason != "no such terminal cell" {
		t.Fatalf("repeated queued interrupt = %#v", repeated)
	}
	if running := manager.Interrupt("frame-queue", "exec-running"); !running.Interrupted {
		t.Fatalf("running interrupt = %#v", running)
	}
	_ = waitForLifecycleOutcome(t, first)
}

func TestManagerQueuesBackgroundCellsWithoutRejectingHealthyLongTask(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-background-queue", FrameID: "frame-background-queue", RootFrameID: "root-background-queue",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Submit(SubmitRequest{
		KernelID: "kernel-background-queue", FrameID: "frame-background-queue", Language: "python", Environment: "python",
		ExecID: "background-first", ToolUseID: "tool-background-first", Origin: "agent", Background: true,
		Code: "from pathlib import Path\nPath('background-first-started').write_text('ready')\nwhile True:\n    pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, filepath.Join(workspaceDir, "background-first-started"))
	second, err := manager.Submit(SubmitRequest{
		KernelID: "kernel-background-queue", FrameID: "frame-background-queue", Language: "python", Environment: "python",
		ExecID: "background-second", ToolUseID: "tool-background-second", Origin: "agent", Background: true,
		Code: "from pathlib import Path\nPath('background-second-finished').write_text('ok')",
	})
	if err != nil {
		t.Fatalf("second background unit was rejected instead of queued: %v", err)
	}
	if candidates := manager.SnapshotIdleCandidates(); len(candidates) != 0 {
		t.Fatalf("queued background work was exposed as idle: %#v", candidates)
	}
	if interrupted := manager.Interrupt("frame-background-queue", "background-first"); !interrupted.Interrupted {
		t.Fatalf("interrupt first background unit=%#v", interrupted)
	}
	_ = waitForLifecycleOutcome(t, first)
	secondOutcome := waitForLifecycleOutcome(t, second)
	if secondOutcome.Err != nil || secondOutcome.Response.Error != "" {
		t.Fatalf("queued background outcome=%#v", secondOutcome)
	}
	if raw, err := os.ReadFile(filepath.Join(workspaceDir, "background-second-finished")); err != nil || string(raw) != "ok" {
		t.Fatalf("queued background output=%q err=%v", raw, err)
	}
	first.AcknowledgePersistence()
	second.AcknowledgePersistence()
}
