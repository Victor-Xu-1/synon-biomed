package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestListActiveDetachedKernelInventoryIsOwnerScopedAndIncludesExecution(t *testing.T) {
	store, backend, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	entries, err := store.ListActiveDetachedKernelInventory(context.Background(), "owner", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("inventory = %#v", entries)
	}
	entry := entries[0]
	if entry.Backend.BackendID != backend.BackendID || entry.SessionSpec.KernelID != backend.KernelID ||
		entry.Execution == nil || entry.Execution.ExecutionID != execution.ExecutionID ||
		entry.Request == nil || entry.Request.ExecutionID != execution.ExecutionID ||
		entry.Operation == nil || entry.Operation.ExecutionID != execution.ExecutionID || entry.ExecutionCount != 1 {
		t.Fatalf("inventory entry = %#v", entry)
	}
	foreign, err := store.ListActiveDetachedKernelInventory(context.Background(), "other-owner", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("foreign inventory = %#v", foreign)
	}
	baseNow := store.now
	store.now = func() time.Time { return baseNow().UTC().Add(2 * time.Minute) }
	stale, err := store.ListActiveDetachedKernelInventory(context.Background(), "owner", 20)
	if err != nil || len(stale) != 0 {
		t.Fatalf("stale inventory = %#v err=%v", stale, err)
	}
}

func TestCountActiveDetachedKernelExecutionsUsesDurableFreshHeartbeat(t *testing.T) {
	store, _, _, _ := newAcceptedDetachedKernelExecutionFixture(t)
	count, err := store.CountActiveDetachedKernelExecutions(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("active detached executions=%d err=%v", count, err)
	}
	baseNow := store.now
	store.now = func() time.Time { return baseNow().UTC().Add(2 * time.Minute) }
	count, err = store.CountActiveDetachedKernelExecutions(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("stale detached executions=%d err=%v", count, err)
	}
}

func TestKernelExecutionDrainCountsDurableStatesAndClosesAdmissionAtomically(t *testing.T) {
	ctx := context.Background()
	store, backend, lease, execution := newAcceptedDetachedKernelExecutionFixture(t)
	count, err := store.CountNonterminalKernelExecutions(ctx, backend.BackendID, backend.BackendGeneration)
	if err != nil || count != 1 {
		t.Fatalf("accepted durable work=%d err=%v", count, err)
	}
	unchanged, idle, err := store.BeginKernelExecutionBackendDrainIfIdle(ctx, BeginKernelExecutionBackendDrainIfIdleInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID,
	})
	if err != nil || idle || unchanged.State != KernelExecutionBackendStateReady {
		t.Fatalf("active drain decision backend=%#v idle=%t err=%v", unchanged, idle, err)
	}

	dispatch, err := store.CommitKernelExecutionDispatch(ctx, CommitKernelExecutionDispatchInput{
		KernelDetachedExecutionControlInput: KernelDetachedExecutionControlInput{
			ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
			ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: execution.StateVersion,
		},
		DispatchSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	written, err := store.MarkKernelExecutionRequestWritten(ctx, MarkKernelExecutionRequestWrittenInput{
		KernelDetachedExecutionControlInput: KernelDetachedExecutionControlInput{
			ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
			ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: dispatch.StateVersion,
		},
		DispatchSequence: dispatch.DispatchSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC()
	resultJSON := `{"ok":true}`
	digest := sha256.Sum256([]byte(resultJSON))
	if _, _, err := store.CommitKernelExecutionResult(ctx, CommitKernelExecutionResultInput{
		ReceiptID: "drain-receipt", ExecutionID: execution.ExecutionID,
		BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID,
		TerminalSequence: written.LastObservationSequence + 1, Outcome: KernelExecutionResultCompleted,
		ResultJSON: resultJSON, ResultSHA256: hex.EncodeToString(digest[:]),
		StartedAt: finishedAt.Add(-time.Second), FinishedAt: finishedAt,
		FilesWrittenJSON: `[]`, DroppedRootsJSON: `[]`,
	}); err != nil {
		t.Fatal(err)
	}
	count, err = store.CountNonterminalKernelExecutions(ctx, backend.BackendID, backend.BackendGeneration)
	if err != nil || count != 0 {
		t.Fatalf("terminal durable work=%d err=%v", count, err)
	}
	transitioned, idle, err := store.BeginKernelExecutionBackendDrainIfIdle(ctx, BeginKernelExecutionBackendDrainIfIdleInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID,
	})
	if err != nil || !idle || transitioned.State != KernelExecutionBackendStateDraining {
		t.Fatalf("idle drain decision backend=%#v idle=%t err=%v", transitioned, idle, err)
	}
	controlled, takeover, err := store.AcquireKernelExecutionBackendControl(ctx, AcquireKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Token: "draining-controller-token-00000000", LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil || controlled.State != KernelExecutionBackendStateDraining || takeover.Epoch <= lease.Epoch {
		t.Fatalf("draining control takeover backend=%#v lease=%#v err=%v", controlled, takeover, err)
	}
}

func TestDetachedKernelCancelReasonIsDurableAndReplayFenced(t *testing.T) {
	store, backend, lease, execution := newAcceptedDetachedKernelExecutionFixture(t)
	input := RequestKernelExecutionCancelInput{
		KernelDetachedExecutionControlInput: KernelDetachedExecutionControlInput{
			ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
			ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: execution.StateVersion,
		},
		CancelRequestID: "cancel-with-reason", Reason: "use a cheaper path",
	}
	updated, err := store.RequestKernelExecutionCancel(context.Background(), input)
	if err != nil || updated.ReasonCode != input.Reason {
		t.Fatalf("cancelled execution=%#v err=%v", updated, err)
	}
	if replay, err := store.RequestKernelExecutionCancel(context.Background(), input); err != nil || replay.StateVersion != updated.StateVersion {
		t.Fatalf("cancel replay=%#v err=%v", replay, err)
	}
	conflict := input
	conflict.Reason = "different reason"
	if _, err := store.RequestKernelExecutionCancel(context.Background(), conflict); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("different replay reason error=%v", err)
	}
}
