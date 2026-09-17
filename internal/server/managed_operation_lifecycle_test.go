package server

import (
	"context"
	"encoding/json"
	"errors"
	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/outbox"
	workspace "synon-go/internal/persistence/workspace"
	"testing"
	"time"
)

func TestManagedOperationUnstartedClaimCanRecover(t *testing.T) {
	s, identity := managedEnvironmentToolFixture(t)
	ctx := context.Background()
	_, err := s.executeManagedEnvironmentOperation(ctx, identity.access, agentruntime.ToolCall{ID: "audit-delete"}, manageEnvironmentsToolName, true, map[string]any{"operation_id": "audit-op"}, managedOperationRequest{Kind: "delete", DeleteName: "analysis"}, &recordingManagedEnvironmentAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	claim := func() workspace.OutboxEvent {
		events, err := s.workspaceStore.ClaimOutbox(ctx, workspace.ClaimOutboxInput{WorkerID: "audit", Topics: []string{workspace.TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
		if err != nil || len(events) != 1 {
			t.Fatalf("claim=%v %v", events, err)
		}
		return events[0]
	}
	first := claim()
	// Deliberately never call the delivery adapter for the first claim.
	if _, err := s.workspaceStore.RetryOutbox(ctx, first.ID, first.ClaimToken, "controller stopped before dispatch", 0); err != nil {
		t.Fatal(err)
	}
	second := claim()
	authority := &recordingManagedEnvironmentAuthority{}
	if err := s.deliverTaskOperation(ctx, second, authority); !errors.Is(err, outbox.ErrDeliverySettled) {
		t.Fatal(err)
	}
	items, err := s.workspaceStore.ConsumeUnreadNotifications(ctx, identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("notifications=%v %v", items, err)
	}
	if authority.deleteName == "" {
		t.Fatalf("operation was never invoked, but settled payload=%#v", items[0].Payload)
	}
}
func TestManagedOperationBackgroundBindingMatchesForeground(t *testing.T) {
	s, identity := managedEnvironmentToolFixture(t)
	ctx := context.Background()
	input := map[string]any{"mode": "install", "environment": "analysis", "fork_to": "analysis-next"}
	metadata := map[string]any{"mode": "install", "operation_id": "audit-fork"}
	request := managedOperationRequest{Kind: "install", Packages: &kernelruntime.MutateManagedPackagesInput{Environment: "analysis", ForkTo: "analysis-next", Packages: []string{"example"}, OperationID: "audit-fork"}}
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis-next", Generation: "generation-next", Status: "ready"}}
	response, err := s.executeManagedEnvironmentOperation(ctx, identity.access, agentruntime.ToolCall{ID: "audit-sync"}, managePackagesToolName, false, metadata, request, authority)
	if err != nil {
		t.Fatal(err)
	}
	foreground := &sessionRunnerChatRun{}
	foreground.bindManagedEnvironmentToolResult(managePackagesToolName, input, response)
	if got := foreground.selectedManagedEnvironment("analysis"); got != "analysis-next" {
		t.Fatalf("foreground=%s", got)
	}
	_, err = s.executeManagedEnvironmentOperation(ctx, identity.access, agentruntime.ToolCall{ID: "audit-async"}, managePackagesToolName, true, metadata, request, authority)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.workspaceStore.ClaimOutbox(ctx, workspace.ClaimOutboxInput{WorkerID: "audit", Topics: []string{workspace.TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(events) != 1 {
		t.Fatalf("claim=%v %v", events, err)
	}
	if err := s.deliverTaskOperation(ctx, events[0], authority); !errors.Is(err, outbox.ErrDeliverySettled) {
		t.Fatal(err)
	}
	items, err := s.workspaceStore.ConsumeUnreadNotifications(ctx, identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("notifications=%v %v", items, err)
	}
	projected, err := s.projectAgentKernelNotifications(ctx, identity.access, items)
	if err != nil {
		t.Fatal(err)
	}
	background := &sessionRunnerChatRun{}
	background.bindManagedEnvironmentNotificationResponse(projected)
	if got := background.selectedManagedEnvironment("analysis"); got != "analysis-next" {
		t.Fatalf("background still selects %q; foreground selects analysis-next", got)
	}
}

func TestManagedOperationReadmissionPreservesAdmittedGeneration(t *testing.T) {
	s, identity := managedEnvironmentToolFixture(t)
	ctx := context.Background()
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis", Generation: "original", Status: "ready"}}
	call := agentruntime.ToolCall{ID: "same-delete-call"}
	request := managedOperationRequest{Kind: "delete", DeleteName: "analysis"}
	metadata := map[string]any{"operation_id": "same-delete"}
	if _, err := s.executeManagedEnvironmentOperation(ctx, identity.access, call, manageEnvironmentsToolName, true, metadata, request, authority); err != nil {
		t.Fatal(err)
	}
	authority.mutateResult.Generation = "successor"
	if _, err := s.executeManagedEnvironmentOperation(ctx, identity.access, call, manageEnvironmentsToolName, true, metadata, request, authority); err != nil {
		t.Fatalf("readmission changed its precondition: %v", err)
	}
	events, err := s.workspaceStore.ClaimOutbox(ctx, workspace.ClaimOutboxInput{WorkerID: "worker", Topics: []string{workspace.TaskOperationOutboxTopic}, Limit: 2, Lease: time.Minute})
	if err != nil || len(events) != 1 {
		t.Fatalf("claims=%v %v", events, err)
	}
	envelope, err := workspace.DecodeTaskOperation(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var admitted managedOperationRequest
	if err := json.Unmarshal(envelope.Request, &admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.DeleteGeneration == nil || *admitted.DeleteGeneration != "original" {
		t.Fatalf("admitted=%#v", admitted)
	}
}
