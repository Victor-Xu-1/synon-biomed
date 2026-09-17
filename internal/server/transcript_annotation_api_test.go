package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptAnnotationAPIProvidesV11CRUDOwnershipAndRealtime(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project", UserID: "local", Name: "Project"},
		{ID: "other-project", UserID: "local", Name: "Other project"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "RESEARCHER", Status: "completed", ConversationType: "delegate",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "other-root", ProjectID: "other-project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "root", Type: "assistant_message",
		Payload: map[string]any{"_uuid": "message-1", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer text"}}},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	if initial := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/child/transcript-annotations", "local", nil, http.StatusOK); len(initial) != 0 {
		t.Fatalf("initial transcript annotations = %#v", initial)
	}
	foreign := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root/transcript-annotations", "foreign-user", nil, http.StatusNotFound)
	if foreign["detail"] != "Frame root not found" {
		t.Fatalf("foreign transcript annotation read = %#v", foreign)
	}
	invalid := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/transcript-annotations", "local", map[string]any{
		"source": "assistant", "anchor_text": "answer text", "kind": "annotation",
	}, http.StatusBadRequest)
	if invalid["detail"] != "message_index is required" {
		t.Fatalf("invalid transcript annotation = %#v", invalid)
	}

	created := compatJSONRequest(t, app, http.MethodPost, "/api/frames/child/transcript-annotations", "local", map[string]any{
		"id": "annotation-1", "message_uuid": nil, "message_index": 0, "block_index": 0,
		"source": "assistant", "tool_name": nil, "anchor_text": "answer text",
		"start_offset": 0, "end_offset": 6, "kind": "annotation", "note": "initial note",
	}, http.StatusCreated)
	if created["id"] != "annotation-1" || created["root_frame_id"] != "root" || created["message_uuid"] != "message-1" ||
		created["source"] != "assistant" || created["kind"] != "annotation" || created["origin"] != "user" ||
		created["note"] != "initial note" || created["read_at"] != nil || created["created_at"] == nil || created["updated_at"] == nil {
		t.Fatalf("created transcript annotation = %#v", created)
	}
	bookmark := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/transcript-annotations", "local", map[string]any{
		"id": "annotation-2", "message_uuid": "message-1", "message_index": 0,
		"source": "assistant", "anchor_text": "answer text", "kind": "bookmark",
	}, http.StatusCreated)
	if bookmark["id"] != "annotation-2" || bookmark["start_offset"] != nil || bookmark["end_offset"] != nil || bookmark["note"] != "" {
		t.Fatalf("created transcript bookmark = %#v", bookmark)
	}

	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/root/transcript-annotations", "local", nil, http.StatusOK)
	if len(listed) != 2 || listed[0]["id"] != "annotation-1" || listed[1]["id"] != "annotation-2" {
		t.Fatalf("listed transcript annotations = %#v", listed)
	}
	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/frames/child/transcript-annotations/annotation-1", "local", map[string]any{
		"note": "updated note", "read": true,
	}, http.StatusOK)
	if updated["note"] != "updated note" || updated["read_at"] == nil {
		t.Fatalf("updated transcript annotation = %#v", updated)
	}
	wrongRoot := compatJSONRequest(t, app, http.MethodPatch, "/api/frames/other-root/transcript-annotations/annotation-1", "local", map[string]any{
		"note": "must not move",
	}, http.StatusNotFound)
	if wrongRoot["detail"] != "Transcript annotation annotation-1 not found" {
		t.Fatalf("cross-root transcript annotation update = %#v", wrongRoot)
	}

	deleted := httptest.NewRecorder()
	app.ServeHTTP(deleted, compatRequest(t, http.MethodDelete, "/api/frames/child/transcript-annotations/annotation-1", "local", nil))
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete transcript annotation = %d body=%q", deleted.Code, deleted.Body.String())
	}
	drained := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/transcript-annotations/drain", "local", map[string]any{
		"ids": []string{"annotation-2", "annotation-2", "not-present"},
	}, http.StatusOK)
	if numberValue(drained["deleted"]) != 1 {
		t.Fatalf("drained transcript annotations = %#v", drained)
	}
	if remaining := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/root/transcript-annotations", "local", nil, http.StatusOK); len(remaining) != 0 {
		t.Fatalf("remaining transcript annotations = %#v", remaining)
	}

	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "local", ProjectID: "project", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, event := range events {
		if event.Type == "transcript_annotations_update" {
			updates++
			if event.Payload["root_frame_id"] != "root" || event.Payload["project_id"] != "project" {
				t.Fatalf("transcript annotation event = %#v", event)
			}
		}
	}
	if updates != 5 {
		t.Fatalf("transcript annotation realtime update count = %d, events=%#v", updates, events)
	}
}
