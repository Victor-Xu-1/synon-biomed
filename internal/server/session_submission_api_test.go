package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionSubmissionAndConfigHTTPAPIUseRealRunnerQueue(t *testing.T) {
	root := t.TempDir()
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
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	message := map[string]any{
		"messageUuid": "message-1", "clientMessageId": "client-1",
		"text": "Analyze this evidence.",
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/frame-1/messages", message, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/requests", map[string]any{
		"frameId": "frame-1", "messageUuid": "message-1",
		"clientMessageId": "client-1", "text": "Analyze this evidence.",
	}, http.StatusOK)

	session, found, err := server.sessionStore.Get("frame-1")
	if err != nil || !found || session.MessageCount != 1 || session.LastRole != "user" {
		t.Fatalf("runner queue session = %#v, found=%v, err=%v", session, found, err)
	}
	entries, err := server.eventJournal.ReadAfter("frame-1", 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Message["text"] != "Analyze this evidence." {
		t.Fatalf("session journal = %#v, err=%v", entries, err)
	}
	events, err := store.ListFrameEvents("frame-1", 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "user_message" {
		t.Fatalf("workspace message events = %#v, err=%v", events, err)
	}

	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/sessions/frame-1/config", map[string]any{
		"config": map[string]any{"model": "gpt-runtime", "maxTokens": 8192, "planMode": true},
	}, http.StatusOK)
	restarted := New(Options{FileRoot: root, Workspace: store}).Handler()
	config := httptest.NewRecorder()
	restarted.ServeHTTP(config, newLoopbackTestRequest(http.MethodGet, "/api/go/sessions/frame-1/config", nil))
	if config.Code != http.StatusOK || !bytes.Contains(config.Body.Bytes(), []byte("\"model\":\"gpt-runtime\"")) ||
		!bytes.Contains(config.Body.Bytes(), []byte("\"planMode\":true")) {
		t.Fatalf("persisted session config = %d: %s", config.Code, config.Body.String())
	}
}
