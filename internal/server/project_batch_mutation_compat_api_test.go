package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityProjectArtifactBatchBenchUpdateAndProcessingCountsUseRealStateAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "local", Name: "Project A"},
		{ID: "project-b", UserID: "local", Name: "Project B"},
		{ID: "project-foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []workspace.CreateFrameInput{
		{ID: "root-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Original Bench"},
		{ID: "child-a", ProjectID: "project-a", ParentFrameID: "root-a", AgentName: "ANALYST", Status: "processing", ConversationType: "delegate", Name: "Child"},
		{ID: "hidden-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Hidden"},
		{ID: "uploads-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "uploads", Name: "Uploads"},
		{ID: "concierge-a", ProjectID: "project-a", AgentName: "CONCIERGE", Status: "processing", ConversationType: "agent", Name: "System"},
		{ID: "completed-a", ProjectID: "project-a", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Done"},
		{ID: "root-b", ProjectID: "project-b", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Project B Bench"},
		{ID: "root-foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Foreign"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetFrameSubmissionMetadata("hidden-a", map[string]any{"request": "hidden"}, true); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "report", ProjectID: "project-a", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("real report"), MaxBytes: 1 << 20, RootFrameID: "root-a", FrameID: "root-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	assertCompatibilityProjectBatchReadState(t, app, version.ID)
	assertCompatibilityBenchMutation(t, app, store)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	assertCompatibilityProjectBatchReadState(t, restarted, version.ID)
	reloaded := compatJSONRequest(t, restarted, http.MethodPatch, "/api/benches/root-a", "local", map[string]any{}, http.StatusOK)
	if reloaded["id"] != "root-a" || reloaded["name"] != "Renamed Bench" || reloaded["task_summary"] != "Durable task summary" {
		t.Fatalf("restarted bench update = %#v", reloaded)
	}
	if artifact.ID != "report" {
		t.Fatalf("artifact identity = %#v", artifact)
	}
}

func assertCompatibilityProjectBatchReadState(t *testing.T, app http.Handler, versionID string) {
	t.Helper()
	batch := compatJSONRequest(t, app, http.MethodGet,
		"/api/projects/batch/artifacts?pids=project-a,project-b,project-foreign,missing&limit=10", "local", nil, http.StatusOK)
	if len(batch) != 4 {
		t.Fatalf("artifact batch keys = %#v", batch)
	}
	projectA, _ := batch["project-a"].([]any)
	if len(projectA) != 1 {
		t.Fatalf("project-a artifact batch = %#v", batch["project-a"])
	}
	report, _ := projectA[0].(map[string]any)
	filePath, _ := report["file_path"].(string)
	if report["id"] != "report" || report["version_id"] != versionID || filePath == "" || !filepath.IsAbs(filePath) {
		t.Fatalf("batched artifact projection = %#v", report)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("batched artifact blob is not readable: %v", err)
	}
	for _, projectID := range []string{"project-b", "project-foreign", "missing"} {
		if values, ok := batch[projectID].([]any); !ok || len(values) != 0 {
			t.Fatalf("isolated artifact batch %s = %#v", projectID, batch[projectID])
		}
	}
	invalid := compatJSONRequest(t, app, http.MethodGet,
		"/api/projects/batch/artifacts?pids=project-a&limit=0", "local", nil, http.StatusBadRequest)
	if invalid["detail"] != "limit must be a positive integer" {
		t.Fatalf("invalid artifact batch limit = %#v", invalid)
	}

	counts := compatJSONRequest(t, app, http.MethodGet, "/api/projects/processing-counts", "local", nil, http.StatusOK)
	if len(counts) != 2 || counts["project-a"] != float64(1) || counts["project-b"] != float64(1) {
		t.Fatalf("processing counts = %#v", counts)
	}
	foreignCounts := compatJSONRequest(t, app, http.MethodGet, "/api/projects/processing-counts", "other", nil, http.StatusOK)
	if len(foreignCounts) != 1 || foreignCounts["project-foreign"] != float64(1) {
		t.Fatalf("foreign processing counts = %#v", foreignCounts)
	}
}

func assertCompatibilityBenchMutation(t *testing.T, app http.Handler, store *workspace.Store) {
	t.Helper()
	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/benches/root-a", "local", map[string]any{
		"name": "Renamed Bench", "task_summary": "Durable task summary",
	}, http.StatusOK)
	if len(updated) != 3 || updated["id"] != "root-a" || updated["name"] != "Renamed Bench" || updated["task_summary"] != "Durable task summary" {
		t.Fatalf("updated bench = %#v", updated)
	}
	unchanged := compatJSONRequest(t, app, http.MethodPatch, "/api/benches/root-a", "local", map[string]any{}, http.StatusOK)
	if unchanged["name"] != "Renamed Bench" || unchanged["task_summary"] != "Durable task summary" {
		t.Fatalf("empty bench update = %#v", unchanged)
	}
	nullName := compatJSONRequest(t, app, http.MethodPatch, "/api/benches/root-a", "local", map[string]any{"name": nil}, http.StatusBadRequest)
	if nullName["detail"] != "Invalid arguments: name: Expected string, received null" {
		t.Fatalf("null bench name = %#v", nullName)
	}
	child := compatJSONRequest(t, app, http.MethodPatch, "/api/benches/child-a", "local", map[string]any{"name": "No"}, http.StatusBadRequest)
	if child["detail"] != "Can only update root frames" {
		t.Fatalf("child bench update = %#v", child)
	}
	for _, target := range []string{"/api/benches/missing", "/api/benches/root-foreign"} {
		missing := compatJSONRequest(t, app, http.MethodPatch, target, "local", map[string]any{"name": "No leak"}, http.StatusNotFound)
		if missing["detail"] != "Bench "+filepath.Base(target)+" not found" {
			t.Fatalf("isolated bench update %s = %#v", target, missing)
		}
	}
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "project-a", Type: "frame_update", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("bench update events = %#v", events)
	}
	for _, event := range events {
		if event.FrameID != "root-a" || event.RootFrameID != "root-a" || event.Payload["action"] != "bench_updated" {
			t.Fatalf("bench update event = %#v", event)
		}
	}
	if count, err := store.CountOutboxEvents(context.Background()); err != nil || count != 2 {
		t.Fatalf("bench update outbox count = %d, err=%v", count, err)
	}
}
