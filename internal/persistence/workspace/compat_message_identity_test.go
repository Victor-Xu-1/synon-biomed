package workspace

import (
	"path/filepath"
	"testing"
)

func TestCompatibilityMessagesRetainDurableEventIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{
		ID: "durable-message-id", FrameID: "frame", Type: "assistant_message",
		Payload: map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "tool-call", "name": "Agent",
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.CompatibilityFrameMessages("frame", 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0]["_uuid"] != "durable-message-id" {
		t.Fatalf("compatibility messages = %#v, err=%v", page, err)
	}
	position, err := store.LocateCompatibilityFrameMessage("frame", "durable-message-id")
	if err != nil || position == nil || *position != 0 {
		t.Fatalf("durable identity position = %v, err=%v", position, err)
	}
}
