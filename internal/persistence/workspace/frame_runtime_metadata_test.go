package workspace

import (
	"path/filepath"
	"testing"
)

func TestFrameRuntimeMetadataPersistsAndAdvancesTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetFrameTraceSnapshot("root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("root", FrameRuntimeMetadata{
		DelegateName: "worker",
		TaskSummary:  "Persist this task summary",
		ContextData:  map[string]any{"_latest_tool_block": map[string]any{"id": "tool-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	delta, err := store.GetFrameTraceSnapshot("root", &before.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Cursor <= before.Cursor || len(delta.Frames) != 1 || delta.Frames[0].DelegateName != "worker" {
		t.Fatalf("metadata delta = %#v", delta)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	metadata, found, err := reopened.GetFrameRuntimeMetadata("root")
	if err != nil || !found || metadata.DelegateName != "worker" {
		t.Fatalf("reopened metadata = %#v found=%v err=%v", metadata, found, err)
	}
	if metadata.TaskSummary != "Persist this task summary" {
		t.Fatalf("reopened task summary = %#v", metadata)
	}
	latest := metadata.ContextData["_latest_tool_block"].(map[string]any)
	if latest["id"] != "tool-1" {
		t.Fatalf("reopened context = %#v", metadata.ContextData)
	}
}
