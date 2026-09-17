package workspace

import (
	"path/filepath"
	"testing"
)

func TestFrameEventsAreDurableAndCursorReadable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{ID: "frame-1", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	first, err := store.AppendFrameEvent(FrameEventInput{FrameID: frame.ID, Type: "frame_started", Payload: map[string]any{"phase": "plan"}})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second, err := store.AppendFrameEvent(FrameEventInput{FrameID: frame.ID, Type: "artifact_created", Payload: map[string]any{"artifactId": "artifact-1"}})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("event sequences = %d, %d", first.Sequence, second.Sequence)
	}
	events, err := store.ListFrameEvents(frame.ID, first.Sequence, 10)
	if err != nil {
		t.Fatalf("list events after cursor: %v", err)
	}
	if len(events) != 1 || events[0].ID != second.ID || events[0].Type != "artifact_created" {
		t.Fatalf("events = %#v", events)
	}
}
