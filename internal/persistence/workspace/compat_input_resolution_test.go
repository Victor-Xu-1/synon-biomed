package workspace

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestResolveCompatibilityPendingInputsConcurrentSingleMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_user_response", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", FrameRuntimeMetadata{ContextData: map[string]any{
		"_pending_input_requests": []any{map[string]any{"tool_id": "ask-1", "kind": "ask"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "frame", Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-1",
			"content": `{"status":"awaiting_user_response"}`,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	results := make(chan CompatibilityInputResolutionResult, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for _, content := range []string{"first", "second"} {
		group.Add(1)
		go func(content string) {
			defer group.Done()
			result, err := store.ResolveCompatibilityPendingInputs("frame", []CompatibilityInputResolution{{
				ToolID: "ask-1", Content: content,
			}})
			results <- result
			errors <- err
		}(content)
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent resolution error = %v", err)
		}
	}
	accepted, repeated := 0, 0
	for result := range results {
		switch result.Status {
		case "accepted":
			accepted++
		case "already_resolved":
			repeated++
		default:
			t.Fatalf("unexpected result = %#v", result)
		}
	}
	if accepted != 1 || repeated != 1 {
		t.Fatalf("accepted=%d repeated=%d", accepted, repeated)
	}
	page, err := store.CompatibilityFrameMessages("frame", 0, 10)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages = %#v, err=%v", page, err)
	}
	blocks := page.Messages[0]["content"].([]any)
	content := blocks[0].(map[string]any)["content"]
	if content != "first" && content != "second" {
		t.Fatalf("resolved content = %#v", content)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.ResolveCompatibilityPendingInputs("frame", []CompatibilityInputResolution{{
		ToolID: "ask-1", Content: "third",
	}})
	if err != nil || replayed.Status != "already_resolved" {
		t.Fatalf("restarted replay = %#v, err=%v", replayed, err)
	}
}
