//go:build linux

package detached

import (
	"context"
	"os"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWaitForPredecessorKernelAuthorityReleaseLetsTheNextToolFollowDrain(t *testing.T) {
	startTicks, err := kernelruntime.CurrentProcessStartTicks()
	if err != nil {
		t.Fatal(err)
	}
	active := workspace.KernelExecutionBackend{
		BackendID: "backend-active", State: workspace.KernelExecutionBackendStateReady,
		ExecutorPID: int64(os.Getpid()), ExecutorPIDStartTicks: startTicks,
	}
	stopped := active
	stopped.State = workspace.KernelExecutionBackendStateStopped
	refreshes := 0
	got, dead, err := waitForPredecessorKernelAuthorityRelease(
		context.Background(), active,
		func(context.Context) (workspace.KernelExecutionBackend, bool, error) {
			refreshes++
			return stopped, true, nil
		},
	)
	if err != nil || dead || got.State != workspace.KernelExecutionBackendStateStopped || refreshes != 1 {
		t.Fatalf("predecessor=%#v dead=%t refreshes=%d err=%v", got, dead, refreshes, err)
	}
}

func TestBackendSessionLockDoesNotBlockUnrelatedKernelIdentity(t *testing.T) {
	backend := &Backend{}
	releaseBlocked := backend.lockSession("kernel-blocked")
	defer releaseBlocked()

	acquired := make(chan struct{})
	go func() {
		releaseIndependent := backend.lockSession("kernel-independent")
		close(acquired)
		releaseIndependent()
	}()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("an unrelated kernel identity was blocked by another session lock")
	}
}

func TestBackendSessionLockSerializesOneKernelIdentity(t *testing.T) {
	backend := &Backend{}
	releaseFirst := backend.lockSession("kernel-shared")

	acquiredSecond := make(chan struct{})
	go func() {
		releaseSecond := backend.lockSession("kernel-shared")
		close(acquiredSecond)
		releaseSecond()
	}()

	select {
	case <-acquiredSecond:
		releaseFirst()
		t.Fatal("the same kernel identity was admitted concurrently")
	case <-time.After(25 * time.Millisecond):
	}
	releaseFirst()

	select {
	case <-acquiredSecond:
	case <-time.After(time.Second):
		t.Fatal("the waiting kernel identity was not released")
	}
}

func TestPredecessorBackendDefinitelyDeadUsesExactProcessIdentity(t *testing.T) {
	now := time.Now().UTC()
	dead, err := predecessorBackendDefinitelyDead(workspace.KernelExecutionBackend{
		ExecutorPID:           1 << 30,
		ExecutorPIDStartTicks: 1,
	}, now)
	if err != nil {
		t.Fatalf("predecessor liveness: %v", err)
	}
	if !dead {
		t.Fatal("missing exact process identity must be considered dead")
	}
}

func TestPredecessorBackendDefinitelyDeadRecognizesLiveProcess(t *testing.T) {
	startTicks, err := kernelruntime.CurrentProcessStartTicks()
	if err != nil {
		t.Fatalf("current process identity: %v", err)
	}
	dead, err := predecessorBackendDefinitelyDead(workspace.KernelExecutionBackend{
		ExecutorPID:           int64(os.Getpid()),
		ExecutorPIDStartTicks: startTicks,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("predecessor liveness: %v", err)
	}
	if dead {
		t.Fatal("live exact process identity must remain active")
	}
}

func TestPredecessorBackendDefinitelyDeadFallsBackToHeartbeatAge(t *testing.T) {
	now := time.Now().UTC()
	staleAt := now.Add(-46 * time.Second)
	recentAt := now.Add(-1 * time.Second)

	stale, err := predecessorBackendDefinitelyDead(workspace.KernelExecutionBackend{
		UpdatedAt: staleAt,
	}, now)
	if err != nil {
		t.Fatalf("stale predecessor liveness: %v", err)
	}
	if !stale {
		t.Fatal("stale predecessor without PID must be considered dead")
	}

	recent, err := predecessorBackendDefinitelyDead(workspace.KernelExecutionBackend{
		UpdatedAt:   now.Add(-2 * time.Minute),
		HeartbeatAt: &recentAt,
	}, now)
	if err != nil {
		t.Fatalf("recent predecessor liveness: %v", err)
	}
	if recent {
		t.Fatal("recent heartbeat must keep predecessor active")
	}
}
