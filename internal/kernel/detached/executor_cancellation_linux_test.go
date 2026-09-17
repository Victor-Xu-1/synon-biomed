//go:build linux

package detached

import (
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
