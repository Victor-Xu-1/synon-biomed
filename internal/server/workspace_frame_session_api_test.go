package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFrameMessagesCursorAndCancelHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-1", ProjectID: "project-1", AgentName: "research",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	for index, messageID := range []string{"m1", "m2", "m3"} {
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: "frame-1", Type: "assistant_message",
			Payload: map[string]any{"messageUuid": messageID, "text": "message", "index": index},
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()

	page := httptest.NewRecorder()
	app.ServeHTTP(page, localWorkspaceRequest(http.MethodGet, "/api/go/frames/frame-1/messages?after_sequence=0&limit=2", nil))
	if page.Code != http.StatusOK || !bytes.Contains(page.Body.Bytes(), []byte("\"hasMore\":true")) ||
		!bytes.Contains(page.Body.Bytes(), []byte("\"nextSequence\":2")) ||
		bytes.Contains(page.Body.Bytes(), []byte("\"messageUuid\":\"m3\"")) {
		t.Fatalf("message page = %d: %s", page.Code, page.Body.String())
	}

	located := httptest.NewRecorder()
	app.ServeHTTP(located, localWorkspaceRequest(http.MethodGet, "/api/go/frames/frame-1/messages/m2", nil))
	if located.Code != http.StatusOK || !bytes.Contains(located.Body.Bytes(), []byte("\"messageUuid\":\"m2\"")) {
		t.Fatalf("located message = %d: %s", located.Code, located.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/frames/frame-1/read-cursor", map[string]any{
		"messageUuid": "m2", "messageIndex": 1,
	}, http.StatusOK)
	cursor := httptest.NewRecorder()
	app.ServeHTTP(cursor, localWorkspaceRequest(http.MethodGet, "/api/go/frames/frame-1/read-cursor", nil))
	if cursor.Code != http.StatusOK || !bytes.Contains(cursor.Body.Bytes(), []byte("\"messageUuid\":\"m2\"")) ||
		!bytes.Contains(cursor.Body.Bytes(), []byte("\"messageIndex\":1")) {
		t.Fatalf("read cursor = %d: %s", cursor.Code, cursor.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/frame-1/cancel", map[string]any{}, http.StatusOK)
	frame, found, err := store.GetFrame("frame-1")
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("cancelled frame = %#v, found=%v, err=%v", frame, found, err)
	}
	retry := httptest.NewRecorder()
	app.ServeHTTP(retry, localWorkspaceRequest(http.MethodPost, "/api/go/frames/frame-1/cancel", nil))
	if retry.Code != http.StatusOK || !bytes.Contains(retry.Body.Bytes(), []byte("\"alreadyCancelled\":true")) {
		t.Fatalf("idempotent cancel = %d: %s", retry.Code, retry.Body.String())
	}
	events, err := store.ListFrameEvents("frame-1", 3, 10)
	if err != nil || len(events) != 1 || events[0].Type != "frame_cancelled" {
		t.Fatalf("cancel events = %#v, err=%v", events, err)
	}
}
