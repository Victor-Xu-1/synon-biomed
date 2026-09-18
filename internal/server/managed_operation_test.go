package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/outbox"
	workspace "synon-go/internal/persistence/workspace"
)

func enqueueManagedOperationTest(t *testing.T, s *Server, identity *agentKernelContext) workspace.OutboxEvent {
	t.Helper()
	_, err := s.executeManagedEnvironmentOperation(context.Background(), identity.access, agentruntime.ToolCall{ID: "durable-install"}, manageEnvironmentsToolName, true, map[string]any{"operation_id": "installer-receipt", "mode": "create"}, managedOperationRequest{Kind: "create", Create: &kernelruntime.CreateManagedEnvironmentInput{Name: "analysis", Language: "python", Packages: []string{"numpy"}, OperationID: "installer-receipt"}}, s.kernelManager)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.workspaceStore.ClaimOutbox(context.Background(), workspace.ClaimOutboxInput{WorkerID: "test-dispatcher", Topics: []string{workspace.TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	return claimed[0]
}

func TestManagedOperationRecoveredServerUsesDurableRequest(t *testing.T) {
	s, identity := managedEnvironmentToolFixture(t)
	event := enqueueManagedOperationTest(t, s, identity)
	pending, hasWork, err := s.agentKernelPendingWork(context.Background(), identity.access)
	if err != nil || !hasWork || numberValue(pending["pending_operations"]) != 1 {
		t.Fatalf("pending=%#v hasWork=%v err=%v", pending, hasWork, err)
	}
	// No in-memory closure or task run from the admitting server is retained.
	restarted := &Server{workspaceStore: s.workspaceStore}
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis", Generation: "generation-1", Status: "ready"}}
	if err := restarted.deliverTaskOperation(context.Background(), event, authority); !errors.Is(err, outbox.ErrDeliverySettled) {
		t.Fatal(err)
	}
	if authority.createInput.OperationID != "installer-receipt" || authority.createInput.Name != "analysis" {
		t.Fatalf("request lost after restart: %#v", authority.createInput)
	}
	if err := restarted.deliverTaskOperation(context.Background(), event, authority); !errors.Is(err, workspace.ErrOutboxClaimLost) {
		t.Fatalf("old claim was not fenced: %v", err)
	}
	if authority.mutations != 1 {
		t.Fatalf("stale owner repeated mutation %d times", authority.mutations)
	}
}

func TestManagedOperationCancellationBeforeAndDuringExecution(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "running"}[running], func(t *testing.T) {
			s, identity := managedEnvironmentToolFixture(t)
			event := enqueueManagedOperationTest(t, s, identity)
			authority := &recordingManagedEnvironmentAuthority{started: make(chan struct{}), release: make(chan struct{})}
			done := make(chan error, 1)
			if running {
				go func() { done <- s.deliverTaskOperation(context.Background(), event, authority) }()
				select {
				case <-authority.started:
				case <-time.After(time.Second):
					t.Fatal("operation did not start")
				}
			}
			if _, err := s.workspaceStore.CancelCompatibilityFrameTree(identity.access.Frame.ID, "test cancellation"); err != nil {
				t.Fatal(err)
			}
			if !running {
				go func() { done <- s.deliverTaskOperation(context.Background(), event, authority) }()
			}
			select {
			case err := <-done:
				if !errors.Is(err, outbox.ErrDeliverySettled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				close(authority.release)
				t.Fatal("cancelled operation did not settle")
			}
			if !running {
				select {
				case <-authority.started:
					t.Fatal("queued cancellation started a mutation")
				default:
				}
			}
			items, err := s.workspaceStore.ConsumeUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, 10)
			if err != nil || len(items) != 1 || items[0].Payload["status"] != "cancelled" {
				t.Fatalf("cancel receipt=%#v %v", items, err)
			}
		})
	}
}

func TestManagedOperationRejectsUnknownPersistedRequestFields(t *testing.T) {
	s, identity := managedEnvironmentToolFixture(t)
	event := enqueueManagedOperationTest(t, s, identity)
	operation, err := workspace.DecodeTaskOperation(event)
	if err != nil {
		t.Fatal(err)
	}
	operation.Request = json.RawMessage(`{"kind":"create","untrusted_extra":true}`)
	event.Payload, _ = json.Marshal(operation)
	if err := s.deliverTaskOperation(context.Background(), event, &recordingManagedEnvironmentAuthority{}); err == nil {
		t.Fatal("unknown persisted operation fields accepted")
	}
}

func TestManagedOperationRequestHasOneTypedAuthority(t *testing.T) {
	valid := managedOperationRequest{Kind: "create", Create: &kernelruntime.CreateManagedEnvironmentInput{OperationID: "op"}, Metadata: map[string]any{"operation_id": "op"}}
	if err := valid.validate(manageEnvironmentsToolName); err != nil {
		t.Fatal(err)
	}
	if err := valid.validate(managePackagesToolName); err == nil {
		t.Fatal("tool authority mismatch accepted")
	}
	valid.Packages = &kernelruntime.MutateManagedPackagesInput{OperationID: "op"}
	if err := valid.validate(manageEnvironmentsToolName); err == nil {
		t.Fatal("ambiguous operation variants accepted")
	}
}

func TestDetachedKernelResultReportsMemoryResetOnlyOnFirstCellOfReplacement(t *testing.T) {
	for _, tc := range []struct {
		generation    int64
		reused, reset bool
	}{{1, false, false}, {2, false, true}, {2, true, false}} {
		result := map[string]any{"stdout": "evidence"}
		annotateDetachedKernelState(result, tc.reused, tc.generation)
		if (result["kernel_memory_state"] == "reset") != tc.reset || result["stdout"] != "evidence" || result["kernel_backend_generation"] != tc.generation {
			t.Fatalf("state=%#v for %#v", result, tc)
		}
	}
}
