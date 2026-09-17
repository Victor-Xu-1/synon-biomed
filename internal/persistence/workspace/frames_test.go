package workspace

import (
	"path/filepath"
	"testing"
)

func TestFrameReadUpdateAndDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "f", ProjectID: "p", AgentName: "a", Status: "queued", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	status := "running"
	frame, err := store.UpdateFrame("f", UpdateFrameInput{Status: &status})
	if err != nil || frame.Status != FrameStatusProcessing {
		t.Fatalf("update = %#v, %v", frame, err)
	}
	frames, err := store.ListFrames("p", 10, 0)
	if err != nil || len(frames) != 1 || frames[0].ID != "f" {
		t.Fatalf("list = %#v, %v", frames, err)
	}
	if err := store.DeleteFrame("f"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.GetFrame("f"); err != nil || ok {
		t.Fatalf("deleted = %v, %v", ok, err)
	}
}
