package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
)

func TestIncompletePlanCorrectionAutoResumesWithoutForcedToolOrder(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   sessionRunnerPlanStepsIncompleteReasonCode,
		"resume_detail": "before completing, call update_step_status for every unreported plan step and mark each completed, blocked, or skipped; exact remaining titles: Save validation; Publish report",
	}}}
	if !runnerInterruptionAutoResume(sessionRunnerPlanStepsIncompleteReasonCode) {
		t.Fatal("incomplete plan correction must use the bounded auto-resume path")
	}
	correctionContext := recoveredRunnerCorrectionContext(entries)
	for _, marker := range []string{"synon.runner_recovery.v1", `"required_transition":"advance_durable_plan"`, `"requires_tool":true`, "Save validation; Publish report"} {
		if !strings.Contains(correctionContext, marker) {
			t.Fatalf("correction context=%q missing %q", correctionContext, marker)
		}
	}
}

func TestUpdateStepStatusUsesOneApprovedPlanAuthority(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{ID: "step-project", UserID: "local", Name: "Step", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "step-frame", ProjectID: project.ID, AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Step"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_approved": true, "_plan_artifact_id": "plan-artifact", "_plan_version_id": "plan-version",
		"_plan_json": map[string]any{"phases": []any{map[string]any{"delegations": []any{map[string]any{"steps": []any{
			map[string]any{"id": "step-1", "title": "Collect evidence", "description": "Collect."},
			map[string]any{"id": "step-2", "title": "Analyze evidence", "description": "Analyze."},
		}}}}}},
		"_step_statuses": map[string]any{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	started, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-1", map[string]any{
		"human_description": "Starting evidence collection", "step": "Evidence phase -> Analyst -> Collect evidence", "status": "in_progress",
	})
	if err != nil || mapValue(started)["status"] != "in_progress" ||
		stringValue(mapValue(mapValue(started)["effect"])["state"]) != "changed" {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	for index, note := range []string{"First source read.", "Second source read; previous source retained."} {
		updated, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-note", map[string]any{
			"step": "step-1", "status": "in_progress", "notes": note,
		})
		if err != nil || mapValue(updated)["idempotent"] != false {
			t.Fatalf("note update %d was discarded: %#v, err=%v", index, updated, err)
		}
		saved, found, err := store.GetFrameRuntimeMetadata(frame.ID)
		state := mapValue(mapValue(saved.ContextData["_step_statuses"])["step-1"])
		if err != nil || !found || stringValue(state["notes"]) != note {
			t.Fatalf("note update %d not durable: %#v, err=%v", index, state, err)
		}
		replayed, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-note-replay", map[string]any{
			"step": "step-1", "status": "in_progress", "notes": note,
		})
		if err != nil || mapValue(replayed)["idempotent"] != true ||
			stringValue(mapValue(mapValue(replayed)["effect"])["state"]) != "unchanged" {
			t.Fatalf("identical status and note must remain idempotent: %#v, err=%v", replayed, err)
		}
		omitted, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-note-omitted", map[string]any{
			"step": "step-1", "status": "in_progress",
		})
		if err != nil || mapValue(omitted)["notes"] != note || mapValue(omitted)["idempotent"] != true {
			t.Fatalf("omitted note erased prior progress: %#v, err=%v", omitted, err)
		}
	}
	cleared, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-note-clear", map[string]any{
		"step": "step-1", "status": "in_progress", "notes": "",
		"observations": []any{"A material observation"}, "source_refs": []any{"source-call-1"},
		"follow_ups": []any{"Inspect the newly identified comparison"},
	})
	if err != nil || mapValue(cleared)["notes"] != "" || mapValue(cleared)["idempotent"] != false {
		t.Fatalf("explicit empty note was not applied: %#v, err=%v", cleared, err)
	}
	preserved, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-navigation-preserve", map[string]any{
		"step": "step-1", "status": "in_progress",
	})
	if err != nil || len(stringValueSlice(mapValue(preserved)["observations"])) != 1 ||
		len(stringValueSlice(mapValue(preserved)["follow_ups"])) != 1 {
		t.Fatalf("omitted research navigation was erased: %#v, err=%v", preserved, err)
	}
	clearedNavigation, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-navigation-clear", map[string]any{
		"step": "step-1", "status": "in_progress", "observations": []any{}, "source_refs": []any{}, "follow_ups": []any{},
	})
	if err != nil || len(stringValueSlice(mapValue(clearedNavigation)["observations"])) != 0 ||
		len(stringValueSlice(mapValue(clearedNavigation)["follow_ups"])) != 0 || mapValue(clearedNavigation)["idempotent"] != false {
		t.Fatalf("explicit research navigation clear was not applied: %#v, err=%v", clearedNavigation, err)
	}
	second, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-2", map[string]any{
		"human_description": "Starting analysis", "step": "step-2", "status": "in_progress",
	})
	if err != nil || mapValue(second)["applied"] != false || mapValue(second)["status"] != "pending" {
		t.Fatalf("later plan work was not redirected while evidence collection remained active: result=%#v err=%v", second, err)
	}
	completed, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-3", map[string]any{
		"human_description": "Completing evidence collection", "step": "step-1", "status": "completed", "notes": "Evidence saved.",
	})
	if err != nil || mapValue(completed)["status"] != "completed" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || stringValue(mapValue(mapValue(metadata.ContextData["_step_statuses"])["step-1"])["status"]) != "completed" {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	remaining, err := srv.incompleteGeneratedPlanCondition(frame.ID)
	if err != nil || remaining == nil || len(remaining.condition.Steps) != 1 || remaining.condition.Steps[0].Title != "Analyze evidence" || remaining.condition.Steps[0].ID != "step-2" {
		t.Fatalf("remaining plan steps=%#v err=%v", remaining, err)
	}
	if _, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "status-4", map[string]any{
		"step": "step-2", "status": "skipped", "notes": "No second analysis was required.",
	}); err != nil {
		t.Fatal(err)
	}
	remaining, err = srv.incompleteGeneratedPlanCondition(frame.ID)
	if err != nil || remaining != nil {
		t.Fatalf("terminal plan steps=%#v err=%v", remaining, err)
	}
}

func TestUpdateStepStatusAcceptsAutonomousExecutablePlan(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "autonomous-step-project", UserID: "local", Name: "Autonomous step", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "autonomous-step-frame", ProjectID: project.ID, AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Autonomous step",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_execution_authorized": true,
		"_plan_control_mode":         "autonomous",
		"_plan_artifact_id":          "autonomous-plan-artifact",
		"_plan_version_id":           "autonomous-plan-version",
		"_plan_json": map[string]any{"phases": []any{map[string]any{"delegations": []any{map[string]any{"steps": []any{
			map[string]any{"id": "step-1", "title": "Collect evidence", "description": "Collect."},
		}}}}}},
		"_step_statuses": map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}

	srv := New(Options{FileRoot: root, Workspace: store})
	completed, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "autonomous-status", map[string]any{
		"human_description": "Completing evidence collection",
		"step":              "Collect evidence",
		"status":            "completed",
	})
	if err != nil || mapValue(completed)["status"] != "completed" {
		t.Fatalf("autonomous progress=%#v err=%v", completed, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found ||
		stringValue(mapValue(mapValue(metadata.ContextData["_step_statuses"])["step-1"])["status"]) != "completed" {
		t.Fatalf("autonomous metadata=%#v found=%t err=%v", metadata, found, err)
	}
	remaining, err := srv.incompleteGeneratedPlanCondition(frame.ID)
	if err != nil || remaining != nil {
		t.Fatalf("autonomous plan must remain non-blocking: remaining=%#v err=%v", remaining, err)
	}
}

func TestApprovedUpdateStepStatusRedirectsOutOfOrderStepWithoutMutation(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "ordered-progress-project", UserID: "local", Name: "Ordered progress", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "ordered-progress-frame", ProjectID: project.ID, AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Ordered progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_execution_authorized": true,
		"_plan_approved":             true,
		"_plan_control_mode":         "reviewed",
		"_plan_artifact_id":          "plan",
		"_plan_version_id":           "version",
		"_plan_json": map[string]any{
			"phases": []any{
				map[string]any{
					"id": "phase-1", "name": "Research",
					"delegations": []any{
						map[string]any{
							"id": "track-1", "name": "Modules",
							"steps": []any{
								map[string]any{"id": "step-1", "title": "Research first", "description": "Research first."},
								map[string]any{"id": "step-2", "title": "Write later", "description": "Write later."},
							},
						},
					},
				},
			},
		},
		"_step_statuses": map[string]any{"step-1": map[string]any{
			"status": "in_progress", "title": "Research first", "description": "Research first.",
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	srv := New(Options{FileRoot: root, Workspace: store})
	redirected, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "skip-ahead", map[string]any{
		"step": "step-2", "status": "completed",
	})
	transition := mapValue(mapValue(redirected)["research_transition"])
	next := anySliceValue(transition["next_investigations"])
	if err != nil || mapValue(redirected)["applied"] != false || len(next) != 1 || stringValue(mapValue(next[0])["id"]) != "step-1" {
		t.Fatalf("out-of-order transition was not redirected to active work: result=%#v err=%v", redirected, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || mapValue(mapValue(metadata.ContextData["_step_statuses"])["step-2"])["status"] != nil {
		t.Fatalf("rejected transition mutated later work: metadata=%#v found=%t err=%v", metadata, found, err)
	}
}

func TestUpdateStepStatusRejectsUnsafeNavigationWithoutMutatingProgress(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{ID: "navigation-project", UserID: "local", Name: "Navigation", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "navigation-frame", ProjectID: project.ID, AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Navigation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
		"_plan_artifact_id": "plan", "_plan_version_id": "version",
		"_plan_json": map[string]any{"phases": []any{map[string]any{"delegations": []any{map[string]any{"steps": []any{
			map[string]any{"id": "step-1", "title": "Inspect", "description": "Inspect evidence."},
		}}}}}}, "_step_statuses": map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	tooMany := make([]any, maxGeneratedPlanNavigationItems+1)
	for index := range tooMany {
		tooMany[index] = "bounded item"
	}
	if _, err := srv.executeAgentUpdateStepStatus(context.Background(), frame.ID, "unsafe-navigation", map[string]any{
		"step": "step-1", "status": "in_progress", "follow_ups": tooMany,
	}); err == nil || !strings.Contains(err.Error(), "storage safety boundary") {
		t.Fatalf("unsafe navigation was accepted: %v", err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || len(mapValue(metadata.ContextData["_step_statuses"])) != 0 {
		t.Fatalf("rejected navigation mutated progress: %#v found=%t err=%v", metadata, found, err)
	}
}
