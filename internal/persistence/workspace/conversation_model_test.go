package workspace

import (
	"path/filepath"
	"testing"
)

func TestSetCompatibilityConversationModelPreservesRuntimeStateAndNestedOverrides(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, err := store.CreateCompatibilityProject(CreateCompatibilityProjectInput{
		ID: "project-model-switch", UserID: "user-model-switch", Name: "Model switch",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root-model-switch", ProjectID: project.ID, AgentName: "GENERAL",
		Status: "processing", ConversationType: "agent", Name: "Running task",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child-model-switch", ProjectID: project.ID, ParentFrameID: root.ID,
		AgentName: "GENERAL", Status: "processing", ConversationType: "agent", Name: "Worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(root.ID, FrameRuntimeMetadata{
		ContextData: map[string]any{
			"preserved": "yes",
			"web_assistant": map[string]any{
				"id": "synonbiomed:operon",
				"conversation_overrides": map[string]any{
					"model": "ark-code-latest", "permission": "ask",
				},
			},
		},
		TaskSummary: "Keep the scientific task",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := store.SetCompatibilityConversationModel(child.ID, "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if result.RootFrameID != root.ID || result.Model != "deepseek-v4-flash" || result.Revision != 1 {
		t.Fatalf("model result = %#v", result)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(root.ID)
	if err != nil || !found {
		t.Fatalf("root metadata found=%v err=%v", found, err)
	}
	if metadata.ContextData["preserved"] != "yes" || metadata.TaskSummary != "Keep the scientific task" {
		t.Fatalf("preserved metadata = %#v", metadata)
	}
	assistant, _ := metadata.ContextData["web_assistant"].(map[string]any)
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	if assistant["id"] != "synonbiomed:operon" || overrides["permission"] != "ask" ||
		overrides["model"] != "deepseek-v4-flash" || overrides["model_revision"] != float64(1) {
		t.Fatalf("assistant overrides = %#v", assistant)
	}
	second, err := store.SetCompatibilityConversationModel(root.ID, "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if second.RootFrameID != root.ID || second.Model != "mimo-v2.5" || second.Revision != 2 {
		t.Fatalf("second model result = %#v", second)
	}
	for _, frameID := range []string{root.ID, child.ID} {
		stored, found, err := store.GetCompatibilityFrame(frameID)
		if err != nil || !found || stored.Status != "processing" {
			t.Fatalf("frame %s found=%v status=%q err=%v", frameID, found, stored.Status, err)
		}
	}
}
