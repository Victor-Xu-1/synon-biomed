package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestKernelTaskTerminationFencesOwnerAndCreatesOneCancellationIntent(t *testing.T) {
	ctx := context.Background()
	store, backend, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	for _, status := range []string{"processing", "awaiting_user_response", "awaiting_plan_approval"} {
		if _, err := store.db.Exec("UPDATE frames SET status=? WHERE id=?", status, backend.FrameID); err != nil {
			t.Fatal(err)
		}
		if ended, err := store.ReconcileKernelTaskTermination(ctx, backend.BackendID, backend.BackendGeneration, backend.ExecutorInstanceID); err != nil || ended {
			t.Fatalf("live task %s retired: %t %v", status, ended, err)
		}
	}
	if _, err := store.db.Exec("UPDATE frames SET status='failed' WHERE id=?", backend.FrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileKernelTaskTermination(ctx, backend.BackendID, backend.BackendGeneration+1, backend.ExecutorInstanceID); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("wrong generation: %v", err)
	}
	if _, err := store.ReconcileKernelTaskTermination(ctx, backend.BackendID, backend.BackendGeneration, "other-executor"); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("wrong executor: %v", err)
	}
	for i := 0; i < 2; i++ {
		if ended, err := store.ReconcileKernelTaskTermination(ctx, backend.BackendID, backend.BackendGeneration, backend.ExecutorInstanceID); err != nil || !ended {
			t.Fatalf("terminal reconciliation: %t %v", ended, err)
		}
		current, found, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
		if err != nil || !found || current.State != DetachedKernelExecutionStateCancelRequested || current.StateVersion != execution.StateVersion+1 || current.CancelRequestID != "task-terminal:"+execution.ExecutionID || current.CancelAckAt != nil {
			t.Fatalf("cancellation intent not idempotent: state=%s version=%d err=%v", current.State, current.StateVersion, err)
		}
	}
	if b, _, err := store.GetKernelExecutionBackend(ctx, backend.BackendID); err != nil || b.State != KernelExecutionBackendStateDraining {
		t.Fatalf("backend still admits work: %s %v", b.State, err)
	}
}

func TestKernelTaskTerminationHonorsReplacementIncarnation(t *testing.T) {
	store, backend, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	if _, err := store.db.Exec("UPDATE frames SET incarnation_id='replacement' WHERE id=?", backend.FrameID); err != nil {
		t.Fatal(err)
	}
	if ended, err := store.ReconcileKernelTaskTermination(context.Background(), backend.BackendID, backend.BackendGeneration, backend.ExecutorInstanceID); err != nil || !ended {
		t.Fatalf("old incarnation retained: %v %v", ended, err)
	}
	current, _, err := store.GetDetachedKernelExecution(context.Background(), execution.ExecutionID)
	if err != nil || current.State != DetachedKernelExecutionStateCancelRequested {
		t.Fatal("old execution not fenced")
	}
	var status string
	if err := store.db.QueryRow("SELECT status FROM frames WHERE id=?", backend.FrameID).Scan(&status); err != nil || status != "processing" {
		t.Fatalf("replacement task was mutated: %s %v", status, err)
	}
}

func TestPendingKernelCancellationUsesDurableGenerationAndAck(t *testing.T) {
	ctx := context.Background()
	store, backend, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	if ids, err := store.ListPendingKernelCancellationIDs(ctx, backend.BackendID, backend.BackendGeneration); err != nil || len(ids) != 0 {
		t.Fatalf("unexpected pending cancellation: %v %v", ids, err)
	}
	if _, _, err := store.CancelFrameWithTranscript(ctx, backend.FrameID); err != nil {
		t.Fatal(err)
	}
	if ids, err := store.ListPendingKernelCancellationIDs(ctx, backend.BackendID, backend.BackendGeneration); err != nil || len(ids) != 1 || ids[0] != execution.ExecutionID {
		t.Fatalf("accepted cancellation not inventoried: %v %v", ids, err)
	}
	if ids, err := store.ListPendingKernelCancellationIDs(ctx, backend.BackendID, backend.BackendGeneration+1); err != nil || len(ids) != 0 {
		t.Fatalf("cross-generation cancellation: %v %v", ids, err)
	}
	current, _, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcknowledgeKernelExecutionCancel(ctx, AcknowledgeKernelExecutionCancelInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID,
		ExpectedVersion: current.StateVersion, CancelRequestID: current.CancelRequestID, AckSequence: 1, Signal: "dequeue",
	}); err != nil {
		t.Fatal(err)
	}
	if ids, err := store.ListPendingKernelCancellationIDs(ctx, backend.BackendID, backend.BackendGeneration); err != nil || len(ids) != 1 {
		t.Fatalf("acknowledged but unsettled intent was lost: %v %v", ids, err)
	}
}
