package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelIdleReaperProtectsWaitingFrameThenClosesExactIdleGeneration(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, t.TempDir()+"/workspace.db", true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if _, err := manager.StartSession(kernelruntime.SessionSpec{
		KernelID: "kernel-idle-reaper", FrameID: identity.access.Frame.ID,
		RootFrameID: identity.access.Frame.RootFrameID, AgentName: "OPERON",
		Language: "python", Environment: "python", WorkspaceDir: identity.workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	waiting := workspace.FrameStatusAwaitingUserResponse
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &waiting}); err != nil {
		t.Fatal(err)
	}
	var offsetSeconds atomic.Int64
	offsetSeconds.Store(int64((time.Hour) / time.Second))
	app.kernelIdleNow = func() time.Time {
		return time.Now().UTC().Add(time.Duration(offsetSeconds.Load()) * time.Second)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunKernelIdleReaper(ctx) }()
	time.Sleep(100 * time.Millisecond)
	if manager.ActiveCount() != 1 {
		t.Fatalf("waiting frame kernel was reaped: active=%d", manager.ActiveCount())
	}
	completed := workspace.FrameStatusCompleted
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.ActiveCount() != 0 {
		t.Fatalf("terminal idle kernel was not reaped: active=%d", manager.ActiveCount())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle reaper did not stop")
	}
}
