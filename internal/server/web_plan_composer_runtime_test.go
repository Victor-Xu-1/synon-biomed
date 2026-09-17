package server

import (
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestPlanApprovalRuntimeKeepsComposerAvailableForTextDecision(t *testing.T) {
	runtime := webConversationRuntimeWithActions(workspace.CompatibilityFrame{
		Frame: workspace.Frame{ID: "plan-frame", Status: "awaiting_plan_approval"},
	}, 0, true)
	if runtime["state"] != "waiting_approval" || runtime["can_send_message"] != true ||
		runtime["is_processing"] != false || runtime["has_task"] != true || runtime["pending_confirmations"] != 1 {
		t.Fatalf("plan composer runtime=%#v", runtime)
	}
}

func TestPendingConfirmationRuntimeOwnsProcessingContract(t *testing.T) {
	runtime := webConversationRuntimeWithActions(workspace.CompatibilityFrame{
		Frame: workspace.Frame{ID: "confirmation-frame", Status: "completed"},
	}, 1, false)
	if runtime["state"] != "waiting_confirmation" || runtime["can_send_message"] != false ||
		runtime["is_processing"] != true || runtime["has_task"] != true || runtime["pending_confirmations"] != 1 {
		t.Fatalf("pending confirmation runtime=%#v", runtime)
	}
}
