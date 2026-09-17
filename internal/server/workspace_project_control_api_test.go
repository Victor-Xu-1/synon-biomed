package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceProjectControlPlaneHTTPAPI(t *testing.T) {
	root := t.TempDir()
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
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.md",
		Kind: "markdown", Content: []byte("report"),
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()

	requestResult := postProjectControlJSON(t, app, http.MethodPost, "/api/go/projects/project-1/request", map[string]any{
		"input_data":        map[string]any{"request": "Prepare release evidence."},
		"client_message_id": "project-request-1",
		"target_agent":      "reviewer", "model": "runtime-model",
	}, http.StatusOK)
	repeated := postProjectControlJSON(t, app, http.MethodPost, "/api/go/projects/project-1/request", map[string]any{
		"input_data":        map[string]any{"request": "Prepare release evidence."},
		"client_message_id": "project-request-1",
		"target_agent":      "reviewer", "model": "runtime-model",
	}, http.StatusOK)
	if repeated["sessionId"] != requestResult["sessionId"] || repeated["idempotent"] != true {
		t.Fatalf("repeated project request = %#v", repeated)
	}
	frame := decodeWorkspaceBranchFrame(t, requestResult)
	if frame.ProjectID != "project-1" || frame.RootFrameID != frame.ID ||
		frame.AgentName != "reviewer" || frame.Status != "processing" {
		t.Fatalf("project request frame = %#v", frame)
	}
	session, found, err := server.sessionStore.Get(frame.ID)
	if err != nil || !found || session.LastRole != "user" || session.MessageCount != 1 ||
		session.Orchestration["sessionConfig"].(map[string]any)["model"] != "runtime-model" {
		t.Fatalf("project request session = %#v, found=%v, err=%v", session, found, err)
	}

	update := postProjectControlJSON(t, app, http.MethodPatch, "/api/go/benches/"+frame.ID, map[string]any{
		"name": "Release bench", "task_summary": "Prepare and verify release evidence.",
	}, http.StatusOK)
	bench := update["bench"].(map[string]any)
	if bench["name"] != "Release bench" || bench["taskSummary"] != "Prepare and verify release evidence." {
		t.Fatalf("updated bench = %#v", bench)
	}

	list := getProjectControlJSON(t, app, "/api/go/projects/project-1/benches?limit=10&q=release", http.StatusOK)
	benches := list["benches"].([]any)
	if len(benches) != 1 || benches[0].(map[string]any)["rootFrameId"] != frame.ID {
		t.Fatalf("benches = %#v", benches)
	}

	batch := getProjectControlJSON(t, app, "/api/go/projects/batch/benches?pids=project-1,project-2&limit=10", http.StatusOK)
	batches := batch["projects"].(map[string]any)
	if len(batches["project-1"].([]any)) != 1 || len(batches["project-2"].([]any)) != 0 {
		t.Fatalf("bench batch = %#v", batches)
	}

	artifactBatch := getProjectControlJSON(t, app, "/api/go/projects/batch/artifacts?pids=project-1,project-2&limit=10", http.StatusOK)
	artifactProjects := artifactBatch["projects"].(map[string]any)
	if len(artifactProjects["project-1"].([]any)) != 1 || len(artifactProjects["project-2"].([]any)) != 0 {
		t.Fatalf("artifact batch = %#v", artifactProjects)
	}

	dashboard := getProjectControlJSON(t, app, "/api/go/projects/dashboard", http.StatusOK)
	totals := dashboard["totals"].(map[string]any)
	if totals["projects"] != float64(2) || totals["benches"] != float64(1) ||
		totals["artifacts"] != float64(1) || totals["processing"] != float64(1) {
		t.Fatalf("dashboard totals = %#v", totals)
	}

	counts := getProjectControlJSON(t, app, "/api/go/projects/processing-counts", http.StatusOK)
	if counts["totalProcessing"] != float64(1) || counts["byStatus"].(map[string]any)["processing"] != float64(1) {
		t.Fatalf("processing counts = %#v", counts)
	}
}

func postProjectControlJSON(t *testing.T, app http.Handler, method, target string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d: %s", method, target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func getProjectControlJSON(t *testing.T, app http.Handler, target string, wantStatus int) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	app.ServeHTTP(response, localWorkspaceRequest(http.MethodGet, target, nil))
	if response.Code != wantStatus {
		t.Fatalf("GET %s status = %d: %s", target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
