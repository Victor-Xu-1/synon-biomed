package server

import (
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityProjectReadModelUsesOwnedDurableState(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, input := range []workspace.CreateCompatibilityProjectInput{
		{ID: "alpha", UserID: "local", Name: "Alpha", Description: "primary"},
		{ID: "beta", UserID: "local", Name: "Beta", Description: "secondary"},
		{ID: "foreign", UserID: "other", Name: "Foreign", Description: "private"},
	} {
		if _, _, err := store.CreateCompatibilityProject(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []workspace.CreateFrameInput{
		{ID: "empty", ProjectID: "alpha", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "processing", ProjectID: "alpha", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Processing"},
		{ID: "waiting", ProjectID: "alpha", AgentName: "OPERON", Status: "awaiting_user_response", ConversationType: "agent", Name: "Waiting"},
		{ID: "completed", ProjectID: "alpha", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Completed"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
		if input.Name != "" {
			if _, err := store.SetFrameRuntimeMetadata(input.ID, workspace.FrameRuntimeMetadata{
				TaskSummary: input.Name + " summary", ContextData: map[string]any{},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, input := range []workspace.SaveArtifactVersionInput{
		{ArtifactID: "report", ProjectID: "alpha", Name: "report.md", Kind: "text/markdown", Content: []byte("report")},
		{ArtifactID: "image", ProjectID: "alpha", Name: "figure.png", Kind: "image/png", Content: []byte("png")},
	} {
		if _, _, err := store.SaveArtifactVersion(input); err != nil {
			t.Fatal(err)
		}
	}

	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	listed := compatJSONRequest(t, app, http.MethodGet, "/api/projects?limit=1&offset=0", "local", nil, http.StatusOK)
	projects, _ := listed["projects"].([]any)
	if listed["total"] != float64(2) || len(projects) != 1 {
		t.Fatalf("project list = %#v", listed)
	}
	item := projects[0].(map[string]any)
	if item["project_id"] == "foreign" || item["last_active_at"] != nil {
		t.Fatalf("project list item = %#v", item)
	}
	allProjects := compatJSONRequest(t, app, http.MethodGet, "/api/projects?limit=10&offset=0", "local", nil, http.StatusOK)
	alphaList := compatibilityTestProjectByID(t, allProjects["projects"], "alpha")
	if alphaList["last_active_at"] == nil {
		t.Fatalf("active project list item = %#v", alphaList)
	}
	if alphaList["latest_conversation_id"] != "waiting" {
		t.Fatalf("latest project conversation = %#v", alphaList)
	}

	alpha := compatJSONRequest(t, app, http.MethodGet, "/api/projects/alpha", "local", nil, http.StatusOK)
	if alpha["project_id"] != "alpha" || alpha["description"] != "primary" ||
		alpha["conversation_count"] != float64(3) || alpha["artifact_count"] != float64(2) ||
		alpha["latest_conversation_id"] != "waiting" {
		t.Fatalf("project get = %#v", alpha)
	}
	compatJSONRequest(t, app, http.MethodGet, "/api/projects/foreign", "local", nil, http.StatusNotFound)
	compatJSONRequest(t, app, http.MethodGet, "/api/projects/missing", "local", nil, http.StatusNotFound)

	dashboard := compatJSONRequest(t, app, http.MethodGet, "/api/projects/dashboard", "local", nil, http.StatusOK)
	if dashboard["total_projects"] != float64(2) || dashboard["total_processing"] != float64(1) ||
		dashboard["total_needs_input"] != float64(1) || dashboard["total_recently_completed"] != float64(1) {
		t.Fatalf("dashboard totals = %#v", dashboard)
	}
	alphaDashboard := compatibilityTestProjectByID(t, dashboard["projects"], "alpha")
	for key, want := range map[string]float64{
		"conversation_count": 3, "total_session_count": 3, "artifact_count": 2,
		"image_artifact_count": 1, "processing_count": 1,
		"needs_input_count": 1, "completed_count": 1,
	} {
		if alphaDashboard[key] != want {
			t.Fatalf("dashboard %s = %#v, want %v: %#v", key, alphaDashboard[key], want, alphaDashboard)
		}
	}
	for key, count := range map[string]int{
		"processing_benches": 1, "needs_input_benches": 1,
		"recently_completed": 1, "recently_updated": 3, "recent_artifacts": 2,
	} {
		values, _ := alphaDashboard[key].([]any)
		if len(values) != count {
			t.Fatalf("dashboard %s = %#v", key, alphaDashboard[key])
		}
	}
	if compatibilityTestProjectExists(dashboard["projects"], "foreign") {
		t.Fatalf("foreign project leaked into dashboard: %#v", dashboard)
	}

	restarted := New(Options{FileRoot: root, Workspace: store})
	restartedDashboard := compatJSONRequest(t, restarted.Handler(), http.MethodGet, "/api/projects/dashboard", "local", nil, http.StatusOK)
	restartedAlpha := compatibilityTestProjectByID(t, restartedDashboard["projects"], "alpha")
	if restartedAlpha["artifact_count"] != float64(2) || restartedAlpha["total_session_count"] != float64(3) {
		t.Fatalf("restarted dashboard = %#v", restartedAlpha)
	}
}

func compatibilityTestProjectByID(t *testing.T, raw any, projectID string) map[string]any {
	t.Helper()
	projects, _ := raw.([]any)
	for _, value := range projects {
		project, _ := value.(map[string]any)
		if project["project_id"] == projectID {
			return project
		}
	}
	t.Fatalf("project %s not found in %#v", projectID, raw)
	return nil
}

func compatibilityTestProjectExists(raw any, projectID string) bool {
	projects, _ := raw.([]any)
	for _, value := range projects {
		project, _ := value.(map[string]any)
		if project["project_id"] == projectID {
			return true
		}
	}
	return false
}
