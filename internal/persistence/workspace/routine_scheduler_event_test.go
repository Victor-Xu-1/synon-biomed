package workspace

import (
	"path/filepath"
	"testing"
)

func TestGetFrameEventByIDReturnsExactDurableEvent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(CreateProjectInput{ID: "project-event", UserID: "user-1", Name: "Event"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{ID: "frame-event", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	created, err := store.AppendFrameEvent(FrameEventInput{ID: "stable-event", FrameID: frame.ID, Type: "routine_tick_completed", Payload: map[string]any{"summary": "done"}})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	loaded, found, err := store.GetFrameEventByID("stable-event")
	if err != nil || !found || loaded.ID != created.ID || loaded.FrameID != frame.ID || loaded.Type != "routine_tick_completed" || loaded.Payload["summary"] != "done" {
		t.Fatalf("loaded event = %#v found=%v err=%v", loaded, found, err)
	}
	if _, found, err := store.GetFrameEventByID("missing-event"); err != nil || found {
		t.Fatalf("missing event found=%v err=%v", found, err)
	}
}
