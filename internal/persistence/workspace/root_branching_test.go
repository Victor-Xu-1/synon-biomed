package workspace

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestForkCompatibilityRootArchivesBranchesAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("root", FrameRuntimeMetadata{ContextData: map[string]any{
		"preserved": true, "_pending_input_requests": []any{"stale"},
	}}); err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"zero", "one", "two"} {
		if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "root", Type: "user_message", Payload: branchTestMessage(text)}); err != nil {
			t.Fatalf("append message %d: %v", index, err)
		}
	}
	if _, err := store.ForkCompatibilityRoot(CompatibilityRootForkInput{
		RootFrameID: "root", SourceBranchID: "br_deadbeef", MessageIndex: 0, EditedContent: "invalid",
	}); err == nil || !strings.Contains(err.Error(), "source branch") {
		t.Fatalf("missing source branch error = %v", err)
	}
	first, err := store.ForkCompatibilityRoot(CompatibilityRootForkInput{
		RootFrameID: "root", MessageIndex: 1, EditedContent: "edited-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RootFrameID != "root" || !validCompatibilityBranchID(first.BranchID) || len(first.Messages) != 2 || branchTestText(first.Messages[0]) != "zero" || branchTestText(first.Messages[1]) != "edited-one" {
		t.Fatalf("first fork = %#v", first)
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("forked root = %#v, found=%t, err=%v", frame, found, err)
	}
	children, err := store.ListFramesForRoot("root")
	if err != nil || len(children) != 1 {
		t.Fatalf("root tree = %#v, err=%v", children, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("root")
	if err != nil || !found || metadata.ContextData["preserved"] != true || metadata.ContextData["_pending_input_requests"] != nil {
		t.Fatalf("fork metadata = %#v, found=%t, err=%v", metadata, found, err)
	}
	branchMeta := metadata.ContextData["_branch_meta"].(map[string]any)
	if branchMeta["active_branch_id"] != first.BranchID {
		t.Fatalf("branch ledger = %#v", branchMeta)
	}
	branches := branchMeta["branches"].(map[string]any)
	if len(branches) != 2 {
		t.Fatalf("branch entries = %#v", branches)
	}
	baseBranchID := ""
	for branchID := range branches {
		if branchID != first.BranchID {
			baseBranchID = branchID
		}
	}
	var archivedRaw string
	if err := store.db.QueryRow(`SELECT payload FROM frame_branch_archives WHERE frame_id = 'root' AND branch_id = ?`, baseBranchID).Scan(&archivedRaw); err != nil {
		t.Fatal(err)
	}
	var archived []map[string]any
	if err := json.Unmarshal([]byte(archivedRaw), &archived); err != nil || len(archived) != 3 || branchTestText(archived[2]) != "two" {
		t.Fatalf("base archive = %#v, err=%v", archived, err)
	}
	cancelled := "cancelled"
	if _, err := store.UpdateFrame("root", UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	second, err := store.ForkCompatibilityRoot(CompatibilityRootForkInput{
		RootFrameID: "root", SourceBranchID: baseBranchID,
		MessageIndex: 2, EditedContent: "edited-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.BranchID == first.BranchID || len(second.Messages) != 3 || branchTestText(second.Messages[2]) != "edited-two" {
		t.Fatalf("source branch fork = %#v", second)
	}
	completed := "completed"
	if _, err := store.UpdateFrame("root", UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCompatibilityRootBranch("root", first.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	if !activated.Changed || activated.BranchID != first.BranchID || activated.Event.Type != "branch_activated" {
		t.Fatalf("branch activation = %#v", activated)
	}
	activePage, err := store.CompatibilityFrameMessages("root", 0, 20)
	if err != nil || len(activePage.Messages) != 2 || branchTestText(activePage.Messages[1]) != "edited-one" {
		t.Fatalf("activated messages = %#v, err=%v", activePage, err)
	}
	unchanged, err := store.ActivateCompatibilityRootBranch("root", first.BranchID)
	if err != nil || unchanged.Changed {
		t.Fatalf("idempotent branch activation = %#v, err=%v", unchanged, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	page, err := reopened.CompatibilityFrameMessages("root", 0, 20)
	if err != nil || len(page.Messages) != 2 || branchTestText(page.Messages[1]) != "edited-one" {
		t.Fatalf("restarted active branch = %#v, err=%v", page, err)
	}
}

func TestForkCompatibilityRootAllowsOnlyOneConcurrentCancelledFork(t *testing.T) {
	store := newCompactionTestStore(t)
	cancelled := "cancelled"
	if _, err := store.UpdateFrame("frame", UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "frame", Type: "user_message", Payload: branchTestMessage("original")}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, edited := range []string{"first", "second"} {
		group.Add(1)
		go func(edited string) {
			defer group.Done()
			_, err := store.ForkCompatibilityRoot(CompatibilityRootForkInput{RootFrameID: "frame", MessageIndex: 0, EditedContent: edited})
			results <- err
		}(edited)
	}
	group.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case strings.Contains(strings.ToLower(err.Error()), "processing"):
			conflicted++
		default:
			t.Fatalf("concurrent fork error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent forks succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestForkCompatibilityRootAtAnswerSupportsEveryV11ResponseAction(t *testing.T) {
	tests := []struct {
		name     string
		response CompatibilityAskUserForkResponse
		want     string
	}{
		{name: "answer", response: CompatibilityAskUserForkResponse{Action: "answer", RawAnswers: json.RawMessage(`{"later":"kept","Which channel?":"stable"}`)}, want: `{"status":"answered","answers":{"later":"kept","Which channel?":"stable"}}`},
		{name: "decide", response: CompatibilityAskUserForkResponse{Action: "decide_for_me"}, want: "User delegated this choice. Decide only within the unchanged canonical task: preserve every explicit priority and requirement, do not turn a suggested preference into a new hard constraint, and choose the option that best satisfies the canonical objective."},
		{name: "discuss", response: CompatibilityAskUserForkResponse{Action: "discuss", Message: "Compare the risks"}, want: "The user wants to discuss these questions further. Their message: Compare the risks. Respond to their input, then use ask_user again if you still need answers."},
		{name: "cancel", response: CompatibilityAskUserForkResponse{Action: "cancel"}, want: "User cancelled the question. Continue without an answer — use your best judgment or skip this step."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAnswerForkTestStore(t)
			result, err := store.ForkCompatibilityRootAtAnswer(CompatibilityRootAnswerForkInput{
				RootFrameID: "root", ToolUseID: "ask-1", Response: test.response,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.RootFrameID != "root" || len(result.Messages) != 2 || answerForkResultContent(result.Messages[1], "ask-1") != test.want {
				t.Fatalf("answer fork = %#v", result)
			}
			if _, found := result.Messages[1]["_intent_id"]; found {
				t.Fatalf("answer result retained intent: %#v", result.Messages[1])
			}
			metadata, found, err := store.GetFrameRuntimeMetadata("root")
			if err != nil || !found {
				t.Fatalf("metadata found=%t err=%v", found, err)
			}
			toolMap := metadata.ContextData["_tool_id_to_frame_id"].(map[string]any)
			running := metadata.ContextData["_running_children"].(map[string]any)
			delegated := metadata.ContextData["_delegated_agents"].([]any)
			if len(toolMap) != 1 || toolMap["ask-1"] != "child-keep" || len(running) != 1 || running["child-keep"] == nil || len(delegated) != 1 || delegated[0] != "child-keep" {
				t.Fatalf("pruned answer context = %#v", metadata.ContextData)
			}
		})
	}
}

func TestForkCompatibilityRootAtAnswerRejectsInvalidQuestionEditsWithoutMutation(t *testing.T) {
	store := newAnswerForkTestStore(t)
	for _, input := range []CompatibilityRootAnswerForkInput{
		{RootFrameID: "root", ToolUseID: "missing", Response: CompatibilityAskUserForkResponse{Action: "cancel"}},
		{RootFrameID: "root", ToolUseID: "ask-1", Response: CompatibilityAskUserForkResponse{Action: "answer", Answers: map[string]string{"Other": "value"}}},
		{RootFrameID: "root", ToolUseID: "ask-1", Response: CompatibilityAskUserForkResponse{Action: "discuss", Message: "  "}},
	} {
		if _, err := store.ForkCompatibilityRootAtAnswer(input); err == nil {
			t.Fatalf("invalid answer fork accepted: %#v", input)
		}
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("invalid answer changed frame = %#v, found=%t, err=%v", frame, found, err)
	}
	page, err := store.CompatibilityFrameMessages("root", 0, 20)
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("invalid answer changed messages = %#v, err=%v", page, err)
	}
}

func newAnswerForkTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	contextData := map[string]any{
		"_tool_id_to_frame_id":    map[string]any{"ask-1": "child-keep", "later-tool": "child-drop"},
		"_running_children":       map[string]any{"child-keep": map[string]any{"status": "done"}, "child-drop": map[string]any{"status": "running"}},
		"_delegated_agents":       []any{"child-keep", "child-drop"},
		"_pending_input_requests": []any{map[string]any{"tool_id": "ask-1"}},
	}
	if _, err := store.SetFrameRuntimeMetadata("root", FrameRuntimeMetadata{ContextData: contextData}); err != nil {
		t.Fatal(err)
	}
	messages := []map[string]any{
		{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "ask-1", "name": "ask_user", "input": map[string]any{"question": "Which channel?"}}}, "_uuid": "assistant-1"},
		{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`, "is_error": true}}, "_uuid": "result-1", "_intent_id": "intent-1"},
		{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "later-tool", "name": "Agent", "input": map[string]any{}}}, "_uuid": "assistant-2"},
	}
	for _, message := range messages {
		if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "root", Type: compatibilityMessageEventType(message), Payload: message}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func answerForkResultContent(message map[string]any, toolUseID string) string {
	for _, rawBlock := range message["content"].([]any) {
		block := rawBlock.(map[string]any)
		if block["type"] == "tool_result" && block["tool_use_id"] == toolUseID {
			return block["content"].(string)
		}
	}
	return ""
}

func branchTestMessage(text string) map[string]any {
	return map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}, "_uuid": text + "-uuid"}
}

func branchTestText(message map[string]any) string {
	content := message["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}
