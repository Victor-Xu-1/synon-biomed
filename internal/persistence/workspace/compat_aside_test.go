package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCreateCompatibilityAsideBuildsHiddenRootAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "parent", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_user_response", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("parent", map[string]any{
		"request": "Parent request", "gpu_mode": "off", "auto_mode": true,
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("parent", FrameRuntimeMetadata{ContextData: map[string]any{
		"_model": "old-model", "_effort": "high",
		"_pending_input_requests": []any{map[string]any{"tool_id": "ask-1"}},
		"_running_children":       map[string]any{"child": "running"},
		"_plan_json":              map[string]any{"steps": []any{"old"}},
		"_goal_result":            "stale",
		"_original_input": map[string]any{
			"gpu_mode": "on", "verifier_mode": "on", "goal_text": "do not inherit",
		},
		"preserved": true,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []map[string]any{
		{
			"role": "assistant", "_uuid": "mixed",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "private"},
				map[string]any{"type": "text", "text": "kept"},
			},
		},
		{
			"role": "assistant", "_uuid": "thinking-only",
			"content": []any{map[string]any{"type": "redacted_thinking", "data": "opaque"}},
		},
		{
			"role": "assistant", "_uuid": "tool",
			"content": []any{map[string]any{"type": "tool_use", "id": "ask-1", "name": "ask_user"}},
		},
		{
			"role": "user", "_uuid": "pending",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "ask-1",
				"content": `{"status":"awaiting_user_response"}`,
			}},
		},
	} {
		if _, err := store.AppendFrameEvent(FrameEventInput{
			FrameID: "parent", Type: compatibilityMessageEventType(message), Payload: message,
		}); err != nil {
			t.Fatal(err)
		}
	}

	model := "new-model"
	result, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "aside", ParentRootFrameID: "parent",
		Request: "Investigate independently.", Model: &model, IntentID: "aside-intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Frame.ID != "aside" || result.Frame.RootFrameID != "aside" ||
		result.Frame.ParentFrameID != "" || result.Frame.ProjectID != "project" ||
		result.Frame.AgentName != "OPERON" || result.Frame.Status != "processing" ||
		result.Frame.Name != "Aside \u00b7 Investigate independently." || result.PrefixLen != 1 {
		t.Fatalf("aside result = %#v", result)
	}
	if len(result.SeedMessages) != 1 {
		t.Fatalf("seed messages = %#v", result.SeedMessages)
	}
	blocks := result.SeedMessages[0]["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "text" {
		t.Fatalf("thinking blocks were not stripped = %#v", blocks)
	}
	parentLink := result.InputData["_aside_parent"].(map[string]any)
	if parentLink["root_frame_id"] != "parent" || parentLink["prefix_len"] != 1 ||
		result.InputData["gpu_mode"] != "on" || result.InputData["verifier_mode"] != nil ||
		result.InputData["_intent_id"] != "aside-intent" {
		t.Fatalf("aside input = %#v", result.InputData)
	}
	for _, key := range []string{
		"_running_children", "_pending_input_requests", "_plan_json",
		"_goal_result", "_original_input",
	} {
		if _, found := result.ContextData[key]; found {
			t.Fatalf("transient context %q survived: %#v", key, result.ContextData)
		}
	}
	if result.ContextData["preserved"] != true || result.ContextData["_model"] != "new-model" ||
		result.ContextData["_effort"] != "high" {
		t.Fatalf("aside context = %#v", result.ContextData)
	}
	aside, found, err := store.GetCompatibilityFrame("aside")
	if err != nil || !found || !aside.IsHidden || aside.TaskSummary != "" {
		t.Fatalf("stored aside = %#v, found=%t, err=%v", aside, found, err)
	}
	page, err := store.CompatibilityFrameMessages("aside", 0, 20)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("stored aside messages = %#v, err=%v", page, err)
	}
	nested, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "nested-aside", ParentRootFrameID: "aside",
		Request: "Nested investigation.",
	})
	if err != nil {
		t.Fatal(err)
	}
	nestedParent := nested.InputData["_aside_parent"].(map[string]any)
	if nestedParent["root_frame_id"] != "aside" || nested.InputData["gpu_mode"] != "on" {
		t.Fatalf("nested aside ancestor inheritance = %#v", nested.InputData)
	}
	if _, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "aside-duplicate", ParentRootFrameID: "parent",
		Request: "Duplicate intent.", IntentID: "aside-intent",
	}); !errors.Is(err, ErrCompatibilityIntentUsed) {
		t.Fatalf("duplicate intent error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	reloaded, found, err := restarted.GetCompatibilityFrame("aside")
	if err != nil || !found || !reloaded.IsHidden || reloaded.RootFrameID != "aside" {
		t.Fatalf("restarted aside = %#v, found=%t, err=%v", reloaded, found, err)
	}
	reloadedPage, err := restarted.CompatibilityFrameMessages("aside", 0, 20)
	if err != nil || len(reloadedPage.Messages) != 1 {
		t.Fatalf("restarted messages = %#v, err=%v", reloadedPage, err)
	}
	intent, found, err := restarted.GetCompatibilityMessageIntent("aside-intent")
	if err != nil || !found || intent.FrameID != "aside" || intent.State != "drained" {
		t.Fatalf("restarted intent = %#v, found=%t, err=%v", intent, found, err)
	}
}

func TestCreateCompatibilityAsideSessionInheritsStickyKnobsAndAddsHarnessNotice(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "parent", ProjectID: "project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("parent", map[string]any{
		"request": "Parent", "auto_mode": true, "python_version": "3.12",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("parent", FrameRuntimeMetadata{ContextData: map[string]any{
		"_model": "same-model", "_input_tokens": 100, "_rc_fork_log": []any{"old"},
		"_original_input": map[string]any{
			"ultra_mode": true, "verifier_mode": "on", "memory_mode": "off",
			"gpu_mode": "on", "goal_text": "do not inherit",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	message := map[string]any{
		"role": "assistant", "_uuid": "answer",
		"content": []any{map[string]any{"type": "text", "text": "Parent answer"}},
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{
		FrameID: "parent", Type: "assistant_message", Payload: message,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "session", ParentRootFrameID: "parent",
		Request: "Forked visible session", AsSession: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrefixLen != 1 || len(result.SeedMessages) != 2 ||
		result.Frame.Name != "Forked visible session" {
		t.Fatalf("session result = %#v", result)
	}
	notice := result.SeedMessages[1]
	if notice["_harness_notice"] != true || notice["role"] != "user" {
		t.Fatalf("session harness notice = %#v", notice)
	}
	if _, found := result.InputData["_aside_parent"]; found {
		t.Fatalf("visible session has aside parent = %#v", result.InputData)
	}
	for key, want := range map[string]any{
		"ultra_mode": true, "verifier_mode": "on", "memory_mode": "off",
		"gpu_mode": "on", "auto_mode": true, "python_version": "3.12",
	} {
		if result.InputData[key] != want {
			t.Fatalf("inherited %s = %#v, want %#v; all=%#v", key, result.InputData[key], want, result.InputData)
		}
	}
	if _, found := result.InputData["goal_text"]; found {
		t.Fatalf("goal_text inherited into session = %#v", result.InputData)
	}
	if result.ContextData["_last_checkpoint_idx"] != 2 ||
		result.ContextData["_last_bookmark_idx"] != 2 {
		t.Fatalf("session checkpoints = %#v", result.ContextData)
	}
	if _, found := result.ContextData["_input_tokens"]; found {
		t.Fatalf("token ledger survived = %#v", result.ContextData)
	}
	if _, found := result.ContextData["_rc_fork_log"]; found {
		t.Fatalf("fork ledger survived = %#v", result.ContextData)
	}
	stored, found, err := store.GetCompatibilityFrame("session")
	if err != nil || !found || stored.IsHidden ||
		stored.TaskSummary != "Forked visible session" {
		t.Fatalf("stored session = %#v, found=%t, err=%v", stored, found, err)
	}
}

func TestCreateCompatibilityAsideRejectsChildParent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "root",
		AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "aside", ParentRootFrameID: "child", Request: "No",
	})
	if !errors.Is(err, ErrCompatibilityAsideParentNotRoot) {
		t.Fatalf("child aside error = %v", err)
	}
}
