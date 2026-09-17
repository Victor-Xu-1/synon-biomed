package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFrameSystemPromptSnapshotPersistsCurrentImmutableContract(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(CreateProjectInput{ID: "prompt-project", UserID: "local", Name: "Prompt", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{ID: "prompt-frame", ProjectID: project.ID, AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Prompt"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PutFrameSystemPromptSnapshot(context.Background(), frame.ID, map[string]any{
		"schema": "synon.frame_system_prompt.v1", "systemPrompt": "first", "toolNames": []string{"Read"},
	})
	if err != nil || len(first.Hash) != 64 {
		t.Fatalf("first snapshot=%#v err=%v", first, err)
	}
	second, err := store.PutFrameSystemPromptSnapshot(context.Background(), frame.ID, map[string]any{
		"schema": "synon.frame_system_prompt.v1", "systemPrompt": "second", "toolNames": []string{"Read", "Skill"},
	})
	if err != nil || first.Hash == second.Hash {
		t.Fatalf("second snapshot=%#v err=%v", second, err)
	}
	loaded, found, err := store.GetFrameSystemPromptSnapshot(context.Background(), frame.ID)
	if err != nil || !found || loaded.Hash != second.Hash || loaded.Payload["systemPrompt"] != "second" {
		t.Fatalf("loaded snapshot=%#v found=%t err=%v", loaded, found, err)
	}
}
