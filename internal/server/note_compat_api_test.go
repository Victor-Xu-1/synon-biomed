package server

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityNotesCreateListUpdatePersistAndPublishExactEvents(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateProjectInput{
		{ID: "owned", UserID: "local", Name: "Owned"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []workspace.CreateFrameInput{
		{ID: "bench", ProjectID: "owned", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Synthesis Bench"},
		{ID: "foreign-bench", ProjectID: "foreign", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Private Bench"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	messageText := strings.Repeat("a", 98) + "😀" + "truncated"
	if _, err := store.SetFrameRuntimeMetadata("bench", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"_messages": []any{map[string]any{"content": messageText}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: "owned", Name: "result.txt", ContentType: "text/plain",
		Content: strings.NewReader("result"), MaxBytes: 1 << 20, RootFrameID: "bench", FrameID: "bench",
	}); err != nil {
		t.Fatal(err)
	}

	app := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	if got := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/notes", "", nil, http.StatusOK); len(got) != 0 {
		t.Fatalf("initial notes = %#v", got)
	}
	messageNote := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/notes", "", map[string]any{
		"target_type": "message", "target_frame_id": "bench", "target_message_index": 0,
		"content": "Review Alpha", "ignored_by_v11": true,
	}, http.StatusOK)
	assertCompatibilityNoteProjection(t, messageNote, "owned", "message", "bench", "Review Alpha")
	if messageNote["target_message_index"] != float64(0) || messageNote["target_artifact_id"] != nil ||
		messageNote["target_name"] != "Synthesis Bench" || messageNote["message_preview"] != strings.Repeat("a", 98)+"😀" {
		t.Fatalf("message note enrichment = %#v", messageNote)
	}
	time.Sleep(2 * time.Millisecond)
	artifactNote := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/notes", "local", map[string]any{
		"target_type": "artifact", "target_frame_id": "bench", "target_artifact_id": "artifact", "content": "Artifact evidence",
	}, http.StatusOK)
	assertCompatibilityNoteProjection(t, artifactNote, "owned", "artifact", "bench", "Artifact evidence")
	if artifactNote["target_artifact_id"] != "artifact" || artifactNote["message_preview"] != nil {
		t.Fatalf("artifact note enrichment = %#v", artifactNote)
	}
	time.Sleep(2 * time.Millisecond)
	benchNote := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/notes", "local", map[string]any{
		"target_type": "bench", "target_frame_id": "bench", "content": "Bench summary",
	}, http.StatusOK)
	assertCompatibilityNoteProjection(t, benchNote, "owned", "bench", "bench", "Bench summary")

	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/notes", "local", nil, http.StatusOK)
	if len(listed) != 3 || listed[0]["id"] != benchNote["id"] || listed[1]["id"] != artifactNote["id"] || listed[2]["id"] != messageNote["id"] {
		t.Fatalf("notes are not newest first: %#v", listed)
	}
	query := url.Values{"q": {"review alpha"}, "target_type": {"message"}, "target_frame_id": {"bench"}}
	filtered := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/notes?"+query.Encode(), "local", nil, http.StatusOK)
	if len(filtered) != 1 || filtered[0]["id"] != messageNote["id"] {
		t.Fatalf("filtered notes = %#v", filtered)
	}

	time.Sleep(2 * time.Millisecond)
	noteID := messageNote["id"].(string)
	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/notes/"+noteID, "", map[string]any{
		"content": "Reviewed Alpha", "ignored_by_v11": "retained compatibility",
	}, http.StatusOK)
	assertCompatibilityNoteProjection(t, updated, "owned", "message", "bench", "Reviewed Alpha")
	if updated["created_at"] == updated["updated_at"] {
		t.Fatalf("update did not advance timestamp: %#v", updated)
	}

	deletedID := artifactNote["id"].(string)
	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/notes/"+deletedID, "local", nil, http.StatusOK)
	if len(deleted) != 2 || deleted["status"] != "deleted" || deleted["note_id"] != deletedID {
		t.Fatalf("deleted note response = %#v", deleted)
	}
	afterDelete := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/notes", "local", nil, http.StatusOK)
	if len(afterDelete) != 2 || findCompatibilityNoteContent(afterDelete, deletedID) != "" {
		t.Fatalf("notes after delete = %#v", afterDelete)
	}

	assertCompatibilityNoteErrors(t, app)
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 5 {
		t.Fatalf("note outbox count = %d, err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	serverApp := New(Options{FileRoot: runtimeRoot, Workspace: store})
	startServerRealtimeOutbox(t, store, serverApp)
	waitServerRealtimeOutbox(t, store)
	restarted := serverApp.Handler()
	reloaded := compatJSONArrayRequest(t, restarted, http.MethodGet, "/api/projects/owned/notes", "local", nil, http.StatusOK)
	if len(reloaded) != 2 || findCompatibilityNoteContent(reloaded, noteID) != "Reviewed Alpha" ||
		findCompatibilityNoteContent(reloaded, deletedID) != "" {
		t.Fatalf("restarted notes = %#v", reloaded)
	}
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "note_update", Limit: 10,
	})
	if err != nil || len(events) != 5 {
		t.Fatalf("note events = %#v, err=%v", events, err)
	}
	for index, event := range events {
		if index == len(events)-1 {
			if event.Payload["action"] != "deleted" || event.Payload["note_id"] != deletedID {
				t.Fatalf("deleted note event payload = %#v", event.Payload)
			}
			continue
		}
		note, ok := event.Payload["note"].(map[string]any)
		if !ok || len(note) != 12 || note["id"] == "" || event.Payload["project_id"] != "owned" {
			t.Fatalf("note event %d payload = %#v", index, event.Payload)
		}
		wantAction := "created"
		if index == len(events)-2 {
			wantAction = "updated"
		}
		if event.Payload["action"] != wantAction {
			t.Fatalf("note event %d action = %#v, want %q", index, event.Payload["action"], wantAction)
		}
	}
}

func assertCompatibilityNoteProjection(t *testing.T, note map[string]any, projectID, targetType, frameID, content string) {
	t.Helper()
	if len(note) != 12 || note["id"] == "" || note["project_id"] != projectID || note["user_id"] != "local" ||
		note["target_type"] != targetType || note["target_frame_id"] != frameID || note["content"] != content {
		t.Fatalf("compatibility note projection = %#v", note)
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := note[key].(string)
		if !ok {
			t.Fatalf("note %s = %#v", key, note[key])
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", value); err != nil {
			t.Fatalf("note %s is not v1.1 ISO milliseconds: %q: %v", key, value, err)
		}
	}
}

func assertCompatibilityNoteErrors(t *testing.T, app http.Handler) {
	t.Helper()
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		detail string
	}{
		{name: "empty content", method: http.MethodPost, path: "/api/projects/owned/notes", body: map[string]any{"target_type": "bench", "target_frame_id": "bench", "content": "  "}, status: http.StatusBadRequest, detail: "content is required"},
		{name: "message index", method: http.MethodPost, path: "/api/projects/owned/notes", body: map[string]any{"target_type": "message", "target_frame_id": "bench", "content": "x"}, status: http.StatusBadRequest, detail: "target_message_index is required for message notes"},
		{name: "artifact id", method: http.MethodPost, path: "/api/projects/owned/notes", body: map[string]any{"target_type": "artifact", "target_frame_id": "bench", "content": "x"}, status: http.StatusBadRequest, detail: "target_artifact_id is required for artifact notes"},
		{name: "missing frame", method: http.MethodPost, path: "/api/projects/owned/notes", body: map[string]any{"target_type": "bench", "target_frame_id": "missing", "content": "x"}, status: http.StatusNotFound, detail: "Frame missing not found"},
		{name: "missing artifact", method: http.MethodPost, path: "/api/projects/owned/notes", body: map[string]any{"target_type": "artifact", "target_frame_id": "bench", "target_artifact_id": "missing", "content": "x"}, status: http.StatusBadRequest, detail: "target artifact not found"},
		{name: "foreign project", method: http.MethodGet, path: "/api/projects/foreign/notes", status: http.StatusNotFound, detail: "Project foreign not found"},
		{name: "missing note", method: http.MethodPatch, path: "/api/notes/missing", body: map[string]any{"content": "x"}, status: http.StatusNotFound, detail: "Note missing not found"},
		{name: "missing note delete", method: http.MethodDelete, path: "/api/notes/missing", status: http.StatusNotFound, detail: "Note missing not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := compatJSONRequest(t, app, test.method, test.path, "local", test.body, test.status)
			if response["detail"] != test.detail {
				t.Fatalf("error = %#v, want %q", response, test.detail)
			}
		})
	}
}

func findCompatibilityNoteContent(notes []map[string]any, id string) string {
	for _, note := range notes {
		if note["id"] == id {
			content, _ := note["content"].(string)
			return content
		}
	}
	return ""
}
