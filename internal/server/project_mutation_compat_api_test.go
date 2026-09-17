package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityProjectDeleteReturnsSuccessAfterCommittedArtifactCleanupFailure(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-delete-artifact", UserID: "local", Name: "Delete artifact project",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "project-delete-artifact-frame", ProjectID: "project-delete-artifact", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "project-delete-artifact-blob", ProjectID: "project-delete-artifact", Name: "delete.txt",
		ContentType: "text/plain", Content: strings.NewReader("artifact content"), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, goodVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "project-delete-good-blob", ProjectID: "project-delete-artifact", Name: "keep-clean.txt",
		ContentType: "text/plain", Content: strings.NewReader("clean me"), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	goodAbs := filepath.Join(databasePath+".blobs", filepath.FromSlash(goodVersion.StoragePath))
	t.Cleanup(func() { _ = os.Remove(goodAbs) })
	folder, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "project-delete-artifact-folder", ProjectID: "project-delete-artifact", Name: "Frame artifacts",
		RootFrameID: frame.RootFrameID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetArtifactFolder(artifact.ID, folder.ID); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(databasePath+".blobs", filepath.FromSlash(version.StoragePath))
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	if err := os.WriteFile(filepath.Join(abs, "undeletable-child"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	serverApp := New(Options{FileRoot: runtimeRoot, Workspace: store})
	t.Cleanup(func() { _ = serverApp.Close(context.Background()) })
	response := compatJSONRequest(t, serverApp.Handler(), http.MethodDelete, "/api/projects/project-delete-artifact", "local", nil, http.StatusOK)
	if response["status"] != "deleted" || response["project_id"] != "project-delete-artifact" || response["cleanup_warnings"] != float64(1) {
		t.Fatalf("project delete response=%#v", response)
	}
	if _, found, err := store.GetCompatibilityProject("local", "project-delete-artifact"); err != nil || found {
		t.Fatalf("deleted project found=%t err=%v", found, err)
	}
	if _, err := os.Stat(goodAbs); !os.IsNotExist(err) {
		t.Fatalf("healthy project blob still exists: path=%s err=%v", goodAbs, err)
	}
}

func TestCompatibilityProjectMutationRequestLifecycleIsDurableAndOwnerScoped(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateCompatibilityProjectInput{
		{ID: "owned", UserID: "local", Name: "Original", Description: "old", ContextData: "old context"},
		{ID: "foreign", UserID: "other", Name: "Foreign", Description: "private", ContextData: "private context"},
	} {
		if _, _, err := store.CreateCompatibilityProject(input); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()

	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned", "local", map[string]any{
		"name": "  Renamed  ", "description": nil, "context": "durable context", "ignored_by_v11": true,
	}, http.StatusOK)
	if updated["name"] != "Renamed" || updated["description"] != nil || updated["context"] != "durable context" {
		t.Fatalf("updated project = %#v", updated)
	}
	updatedAt := updated["updated_at"]
	noop := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned", "local", map[string]any{}, http.StatusOK)
	if noop["updated_at"] != updatedAt {
		t.Fatalf("empty patch advanced revision: before=%v after=%v", updatedAt, noop["updated_at"])
	}
	invalid := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned", "local", map[string]any{"name": "  "}, http.StatusBadRequest)
	if invalid["detail"] != "name must contain between 1 and 255 characters" {
		t.Fatalf("invalid patch = %#v", invalid)
	}
	foreign := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/foreign", "local", map[string]any{"name": "Leak"}, http.StatusNotFound)
	if foreign["detail"] != "Project foreign not found" {
		t.Fatalf("foreign patch = %#v", foreign)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server = New(Options{FileRoot: runtimeRoot, Workspace: store})
	startServerRealtimeOutbox(t, store, server)
	app = server.Handler()
	reloaded := compatJSONRequest(t, app, http.MethodGet, "/api/projects/owned", "local", nil, http.StatusOK)
	if reloaded["name"] != "Renamed" || reloaded["description"] != nil || reloaded["context"] != "durable context" {
		t.Fatalf("reloaded project = %#v", reloaded)
	}

	intentID := "00000000-0000-4000-8000-000000000091"
	submitted := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/request", "local", map[string]any{
		"input_data":    map[string]any{"request": "Run the owned project task.", "source": "real project context"},
		"intent_id":     intentID,
		"session_knobs": map[string]any{"temperature": 0.2, "as_routine": true},
	}, http.StatusOK)
	frameID, _ := submitted["frame_id"].(string)
	if len(submitted) != 3 || frameID == "" || submitted["root_frame_id"] != frameID || submitted["status"] != "accepted" {
		t.Fatalf("project request = %#v", submitted)
	}
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found || frame.ProjectID != "owned" || frame.AgentName != "OPERON" ||
		frame.Status != "processing" || frame.Name != "Run the owned project task." || frame.TaskSummary != "Run the owned project task." {
		t.Fatalf("submitted frame = %#v found=%v err=%v", frame, found, err)
	}
	if frame.InputData["source"] != "real project context" {
		t.Fatalf("open input_data was not preserved: %#v", frame.InputData)
	}
	session, found, err := server.sessionStore.Get(frameID)
	if err != nil || !found || session.MessageCount != 1 || session.LastRole != "user" {
		t.Fatalf("submitted session = %#v found=%v err=%v", session, found, err)
	}
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if config["targetAgent"] != "OPERON" || config["intentId"] != intentID || config["temperature"] != float64(0.2) {
		t.Fatalf("session config = %#v", config)
	}
	if _, present := config["as_routine"]; present {
		t.Fatalf("as_routine leaked into session config: %#v", config)
	}
	journaled, err := server.eventJournal.HasClientMessage(frameID, intentID)
	if err != nil || !journaled {
		t.Fatalf("project request journaled=%v err=%v", journaled, err)
	}
	foreignSubmit := compatJSONRequest(t, app, http.MethodPost, "/api/projects/foreign/request", "local", map[string]any{
		"input_data": map[string]any{"request": "Must not run."},
	}, http.StatusNotFound)
	if foreignSubmit["detail"] != "Project foreign not found" {
		t.Fatalf("foreign submit = %#v", foreignSubmit)
	}

	client := server.sessionSockets.Register(frameID)
	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/projects/owned", "local", nil, http.StatusOK)
	if len(deleted) != 3 || deleted["status"] != "deleted" || deleted["project_id"] != "owned" || deleted["cleanup_warnings"] != float64(0) {
		t.Fatalf("delete response = %#v", deleted)
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("project deletion did not close the live session websocket")
	}
	if _, found, err := store.GetCompatibilityProject("local", "owned"); err != nil || found {
		t.Fatalf("deleted project found=%v err=%v", found, err)
	}
	if _, found, err := store.GetFrame(frameID); err != nil || found {
		t.Fatalf("deleted frame found=%v err=%v", found, err)
	}
	if _, found, err := server.sessionStore.Get(frameID); err != nil || found {
		t.Fatalf("deleted session found=%v err=%v", found, err)
	}
	if entries, err := server.eventJournal.ReadAll(frameID); err != nil || len(entries) != 0 {
		t.Fatalf("deleted journal entries=%#v err=%v", entries, err)
	}
	if _, found, err := store.GetCompatibilityProject("other", "foreign"); err != nil || !found {
		t.Fatalf("foreign project was affected: found=%v err=%v", found, err)
	}
	waitServerRealtimeOutbox(t, store)
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "project_deleted", Limit: 10,
	})
	if err != nil || len(events) != 1 || events[0].Payload["project_id"] != "owned" {
		t.Fatalf("project deletion event = %#v err=%v", events, err)
	}
}

func TestProjectDeletionDeliversLiveReplayAndExactInvalidations(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-1", UserID: "local", Name: "Delete me",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
	startServerRealtimeOutbox(t, store, server)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=local&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}
	deleted := compatJSONRequest(t, server.Handler(), http.MethodDelete, "/api/projects/project-1", "local", nil, http.StatusOK)
	if deleted["status"] != "deleted" || deleted["project_id"] != "project-1" {
		t.Fatalf("project deletion response=%#v", deleted)
	}
	waitServerRealtimeOutbox(t, store)
	target := map[string]int{"project_deleted": 1}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, target, 1)
	if err != nil {
		t.Fatalf("read live project deletion: %v", err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, target, 1)
	if err != nil {
		t.Fatalf("read replayed project deletion: %v", err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed project deletion differ: live=%#v replay=%#v", liveEvents, replayedEvents)
	}
	event := liveEvents[0]
	for _, expected := range []struct {
		query, policy, match string
		key                  []any
	}{
		{"project", "immediate", "exact", []any{"project", "project-1"}},
		{"artifacts", "immediate", "exact", []any{"artifacts", "project-1"}},
		{"collaborators", "immediate", "exact", []any{"collaborators", "project-1"}},
		{"folders", "immediate", "exact", []any{"folders", "project-1"}},
		{"projectConversation", "immediate", "exact", []any{"projectConversation", "project-1"}},
		{"dashboard", "immediate", "exact", []any{"dashboard"}},
		{"projectList", "immediate", "exact", []any{"project-list"}},
		{"benchesBatch", "immediate", "prefix", []any{"benches-batch"}},
		{"artifactsBatch", "immediate", "prefix", []any{"artifacts-batch"}},
	} {
		assertDomainInvalidation(t, event, expected.query, expected.key, expected.policy, expected.match)
	}
}
