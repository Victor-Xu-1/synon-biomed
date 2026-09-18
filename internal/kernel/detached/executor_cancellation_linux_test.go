//go:build linux

package detached

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestDetachedCancellationOutcomeRequiresAckedDurableAuthority(t *testing.T) {
	now := time.Now().UTC()
	base := workspace.DetachedKernelExecution{
		State:           workspace.DetachedKernelExecutionStateCancelRequested,
		CancelRequestID: "cancel-authority", CancelAckAt: &now,
	}
	for _, test := range []struct {
		name, signal, wantSignal string
		cancelled                bool
	}{
		{name: "sigint", signal: "sigint", cancelled: true, wantSignal: "SIGINT"},
		{name: "sigterm", signal: "sigterm", cancelled: true, wantSignal: "SIGTERM"},
		{name: "sigkill", signal: "sigkill", cancelled: true, wantSignal: "SIGKILL"},
		{name: "dequeue", signal: "dequeue", cancelled: true},
		{name: "provider cancellation is not an OS signal", signal: "provider_cancel", cancelled: true},
		{name: "unknown", signal: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := base
			execution.CancelSignal = test.signal
			cancelled, signal := detachedCancellationOutcome(execution)
			if cancelled != test.cancelled || signal != test.wantSignal {
				t.Fatalf("cancelled=%t signal=%q", cancelled, signal)
			}
		})
	}
	withoutAck := base
	withoutAck.CancelAckAt = nil
	if cancelled, signal := detachedCancellationOutcome(withoutAck); cancelled || signal != "" {
		t.Fatalf("unacknowledged cancellation classified as terminal: %t %q", cancelled, signal)
	}
}

func TestCancellationDoesNotInventOutcomeForLostStartedObserver(t *testing.T) {
	request := workspace.KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: "operation", ExecutionID: "execution", OwnerUserID: "owner", ProjectID: "project",
		RootFrameID: "frame", RootFrameIncarnationID: "incarnation", FrameID: "frame", FrameIncarnationID: "incarnation",
		KernelID: "kernel", KernelGeneration: 1, ToolCallID: "call", ToolName: "python", Language: "python",
		KernelKind: "operon", Environment: "python", Code: "pass", OutputLimitBytes: 1024, Origin: "agent",
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	executor := &Executor{}
	_, err = executor.applyDurableCancellation(context.Background(), workspace.DetachedKernelExecution{
		ExecutionID: request.ExecutionID, RequestJSON: string(encoded), WorkerStartedAt: &now, CancelRequestID: "cancel",
	})
	if err == nil || !strings.Contains(err.Error(), "no owned outcome observer") {
		t.Fatalf("lost observer was reported as a dequeued computation: %v", err)
	}
}
