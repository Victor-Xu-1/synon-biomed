package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestLocalKernelObserverCloseJoinsSettlementAndRejectsNewWork(t *testing.T) {
	app := &Server{}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	observerCtx, observerCancel, reserved := app.reserveDetachedKernelObserver(context.Background(), "local:settling")
	if !reserved {
		t.Fatal("observer reservation rejected before shutdown")
	}
	done := app.launchReservedAgentKernelExecutionObserver(observerCtx, "local:settling", observerCancel, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release // A durable write already in flight must finish before Close.
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- app.Close(ctx) }()
	select {
	case <-cancelled:
	case <-ctx.Done():
		close(release)
		t.Fatal("server did not cancel the local observer")
	}
	select {
	case err := <-closed:
		close(release)
		t.Fatalf("server closed before settlement exited: %v", err)
	default:
	}
	if _, _, reserved := app.reserveDetachedKernelObserver(context.Background(), "local:late"); reserved {
		close(release)
		t.Fatal("observer admitted after shutdown")
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestLocalKernelSubmissionAfterShutdownDoesNotCreateUnobservedExecution(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	app.beginDetachedKernelObserverShutdown()
	result, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import time; time.sleep(60)", "environment": "python", "background": true,
	})
	if !errors.Is(err, ErrRuntimeDraining) || result != nil {
		t.Fatalf("submission after shutdown result=%#v err=%v", result, err)
	}
	if manager.ActiveExecutionCount() != 0 {
		t.Fatalf("rejected submission left %d unobserved executions", manager.ActiveExecutionCount())
	}
}

func TestLocalKernelForegroundHandoffDuringShutdownPersistsTerminalReceipt(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	returned := make(chan error, 1)
	go func() {
		_, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
			"code":        "from pathlib import Path; import time; Path('started').write_text('ready'); time.sleep(60)",
			"environment": "python",
		})
		returned <- err
	}()
	waitForKernelAPIFile(t, filepath.Join(identity.workspaceDir, "started"))
	// Close now wins before the foreground call has transferred its already
	// running execution to an observer. No production scheduling hook is used.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-returned:
		if !errors.Is(err, ErrRuntimeDraining) {
			t.Fatalf("foreground drain result=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("foreground execution did not finish during shutdown")
	}
	events, err := store.ClaimOutbox(context.Background(), workspace.ClaimOutboxInput{
		WorkerID: "shutdown-receipt-test", Topics: []string{workspace.KernelResultSettlementOutboxTopic},
		Limit: 2, Lease: time.Second,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("durable terminal receipts=%d err=%v", len(events), err)
	}
	if _, err := workspace.DecodeKernelBackgroundSettlement(events[0]); err != nil {
		t.Fatalf("invalid durable terminal receipt: %v", err)
	}
	if manager.ActiveExecutionCount() != 0 {
		t.Fatal("shutdown left a live local execution after settlement")
	}
}

func TestLocalKernelBackgroundExecutionIsJoinedOnServerClose(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	result, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code": "import time; time.sleep(60)", "environment": "python", "background": true,
	})
	if err != nil || result["status"] != "running" {
		t.Fatalf("background execution result=%#v err=%v", result, err)
	}
	observerID := "local:" + stringValue(result["exec_id"])
	app.detachedKernelObserverMu.Lock()
	_, tracked := app.detachedKernelObservers[observerID]
	app.detachedKernelObserverMu.Unlock()
	if !tracked {
		t.Fatal("local background execution escaped the server shutdown fence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	app.detachedKernelObserverMu.Lock()
	remaining := len(app.detachedKernelObservers)
	app.detachedKernelObserverMu.Unlock()
	if remaining != 0 || manager.ActiveExecutionCount() != 0 {
		t.Fatalf("server left observers=%d executions=%d", remaining, manager.ActiveExecutionCount())
	}
}
