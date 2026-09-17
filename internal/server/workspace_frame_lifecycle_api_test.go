package server

import (
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFrameLifecycleHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, projectID := range []string{"project-1", "project-2"} {
		if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, Name: projectID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-frame", ProjectID: "project-1", AgentName: "research",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "child-frame", ProjectID: "project-1", ParentFrameID: "root-frame",
		AgentName: "writer", Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "root-frame", Type: "queued_message",
		Payload: map[string]any{"messageUuid": "queued-1", "text": "send later"},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/messages/queued-1/retract", map[string]any{}, http.StatusOK)
	message, found, err := store.LocateFrameMessage("root-frame", "queued-1")
	if err != nil || !found || message.Type != "message_retracted" {
		t.Fatalf("retracted message = %#v, found=%v, err=%v", message, found, err)
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/move", map[string]any{
		"projectId": "project-2",
	}, http.StatusOK)
	for _, frameID := range []string{"root-frame", "child-frame"} {
		frame, found, err := store.GetFrame(frameID)
		if err != nil || !found || frame.ProjectID != "project-2" {
			t.Fatalf("moved frame %s = %#v, found=%v, err=%v", frameID, frame, found, err)
		}
	}

	paused := "failed"
	if _, err := store.UpdateFrame("root-frame", workspace.UpdateFrameInput{Status: &paused}); err != nil {
		t.Fatal(err)
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/resume", map[string]any{}, http.StatusOK)
	frame, found, err := store.GetFrame("root-frame")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("resumed frame = %#v, found=%v, err=%v", frame, found, err)
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/messages/queued-1/retract", map[string]any{}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/move", map[string]any{
		"projectId": "project-2",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/root-frame/resume", map[string]any{}, http.StatusOK)
	events, err := store.ListFrameEvents("root-frame", 0, 20)
	if err != nil || len(events) != 3 ||
		events[0].Type != "message_retracted" ||
		events[1].Type != "frame_moved" ||
		events[2].Type != "frame_resumed" {
		t.Fatalf("lifecycle events = %#v, err=%v", events, err)
	}
}
