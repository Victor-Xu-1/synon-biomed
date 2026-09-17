package workspace

import (
	"path/filepath"
	"testing"
)

func TestFrameTraceSnapshotUsesDurableMonotonicRevisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project-1", AgentName: "synon", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "child", ProjectID: "project-1", ParentFrameID: "root", AgentName: "worker", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	full, err := store.GetFrameTraceSnapshot("root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.FrameCount != 2 || len(full.Frames) != 2 || full.Cursor <= 0 {
		t.Fatalf("full trace=%#v", full)
	}
	cursor := full.Cursor
	if _, err := store.AppendFrameEvent(FrameEventInput{FrameID: "child", Type: "assistant_message", Payload: map[string]any{"role": "assistant", "content": "done"}}); err != nil {
		t.Fatal(err)
	}
	delta, err := store.GetFrameTraceSnapshot("root", &cursor)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Cursor <= cursor || delta.FrameCount != 2 || len(delta.Frames) != 1 || delta.Frames[0].ID != "child" {
		t.Fatalf("delta trace=%#v", delta)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetFrameTraceSnapshot("root", &cursor)
	if err != nil || persisted.Cursor != delta.Cursor || len(persisted.Frames) != 1 {
		t.Fatalf("persisted trace=%#v err=%v", persisted, err)
	}
}
