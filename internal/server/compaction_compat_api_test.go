package server

import (
	"net/http"
	"path/filepath"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityCompactionArchivesRecoverJournalAndSurviveRestart(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: root})
	for index, message := range []eventjournal.Message{
		{"type": "message", "role": "user", "text": "question"},
		{"type": "message", "role": "assistant", "text": "answer"},
		{"type": "session_compact", "role": "system", "summary": "Recovered summary", "compactedThroughEventId": 2},
	} {
		if _, err := server.eventJournal.Append("frame", message, eventjournal.Metadata{ClientMessageID: "compact-fixture-" + string(rune('0'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	app := server.Handler()
	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/frame/compaction-archives", "local", nil, http.StatusOK)
	if len(listed) != 1 || listed[0]["compaction_index"] != float64(0) || listed[0]["message_count"] != float64(2) || listed[0]["messages"] != nil {
		t.Fatalf("listed compaction archives = %#v", listed)
	}
	archive := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame/compaction-archives/0", "local", nil, http.StatusOK)
	messages, ok := archive["messages"].([]any)
	if archive["summary"] != "Recovered summary" || !ok || len(messages) != 2 || messages[0].(map[string]any)["text"] != "question" {
		t.Fatalf("compaction archive = %#v", archive)
	}
	missing := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame/compaction-archives/9", "local", nil, http.StatusNotFound)
	if missing["detail"] != "Compaction archive 9 not found for frame frame" {
		t.Fatalf("missing archive = %#v", missing)
	}
	invalid := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame/compaction-archives/not-an-index", "local", nil, http.StatusBadRequest)
	if invalid["detail"] == nil {
		t.Fatalf("invalid archive index = %#v", invalid)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := New(Options{Workspace: reopened, FileRoot: root}).Handler()
	restored := compatJSONRequest(t, restarted, http.MethodGet, "/api/frames/frame/compaction-archives/0", "local", nil, http.StatusOK)
	if restored["id"] != archive["id"] || restored["summary"] != archive["summary"] || len(restored["messages"].([]any)) != 2 {
		t.Fatalf("restored compaction archive = %#v", restored)
	}
}
