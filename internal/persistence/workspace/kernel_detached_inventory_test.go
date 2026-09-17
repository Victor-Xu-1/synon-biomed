package workspace

import (
	"context"
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
