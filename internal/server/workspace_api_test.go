package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceHTTPAPIProvidesContractAndArtifactLineage(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	serverApp := New(Options{Workspace: store})
	startServerRealtimeOutbox(t, store, serverApp)
	app := serverApp.Handler()

	response := httptest.NewRecorder()
	app.ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/api/contracts/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("contract summary status = %d: %s", response.Code, response.Body.String())
	}
	var summary struct {
		ServiceMethods int `json:"service_methods"`
		HTTPRoutes     int `json:"http_routes"`
		EventTypes     int `json:"event_types"`
		QueryKeys      int `json:"query_keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		t.Fatalf("decode contract summary: %v", err)
	}
	if summary.ServiceMethods != 190 || summary.HTTPRoutes != 34 || summary.EventTypes != 47 || summary.QueryKeys != 60 {
		t.Fatalf("contract summary = %#v", summary)
	}
	routesResponse := httptest.NewRecorder()
	app.ServeHTTP(routesResponse, newLoopbackTestRequest(http.MethodGet, "/api/contracts/http-routes", nil))
	if routesResponse.Code != http.StatusOK {
		t.Fatalf("contract routes status = %d: %s", routesResponse.Code, routesResponse.Body.String())
	}
	var routes []map[string]any
	if err := json.NewDecoder(routesResponse.Body).Decode(&routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 34 || routes[0]["path"] != "/api/compute/gpu" || routes[33]["path"] != "/api/compute/local/import" {
		t.Fatalf("contract routes = %#v", routes)
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/llm/providers", map[string]any{"id": "provider-1", "userId": "local", "name": "primary", "type": "openai-compatible", "baseUrl": "https://models.example.test/v1", "model": "model-a", "secretRef": "secret://primary"}, http.StatusOK)
	providersResponse := httptest.NewRecorder()
	app.ServeHTTP(providersResponse, localWorkspaceRequest(http.MethodGet, "/api/llm/providers", nil))
	if providersResponse.Code != http.StatusOK || !bytes.Contains(providersResponse.Body.Bytes(), []byte("provider-1")) || bytes.Contains(providersResponse.Body.Bytes(), []byte("secret://primary")) {
		t.Fatalf("model providers = %d: %s", providersResponse.Code, providersResponse.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents", map[string]any{"id": "agent-1", "userId": "user-1", "name": "research", "displayName": "Research", "description": "Find evidence", "systemPrompt": "Be precise", "skillNames": []string{"literature-review"}}, http.StatusOK)
	agentsResponse := httptest.NewRecorder()
	app.ServeHTTP(agentsResponse, newLoopbackTestRequest(http.MethodGet, "/api/go/agents?user_id=user-1", nil))
	if agentsResponse.Code != http.StatusOK || !bytes.Contains(agentsResponse.Body.Bytes(), []byte("research")) {
		t.Fatalf("agents = %d: %s", agentsResponse.Code, agentsResponse.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects", map[string]any{"id": "project-1", "name": "Migration"}, http.StatusOK)
	projectsResponse := httptest.NewRecorder()
	app.ServeHTTP(projectsResponse, localWorkspaceRequest(http.MethodGet, "/api/go/projects", nil))
	if projectsResponse.Code != http.StatusOK || !bytes.Contains(projectsResponse.Body.Bytes(), []byte("project-1")) {
		t.Fatalf("projects = %d: %s", projectsResponse.Code, projectsResponse.Body.String())
	}
	projectResponse := httptest.NewRecorder()
	app.ServeHTTP(projectResponse, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-1", nil))
	if projectResponse.Code != http.StatusOK || !bytes.Contains(projectResponse.Body.Bytes(), []byte("Migration")) {
		t.Fatalf("project = %d: %s", projectResponse.Code, projectResponse.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/projects/project-1", map[string]any{"name": "Migrated"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects", map[string]any{"id": "project-delete", "name": "Delete"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/projects/project-delete", nil, http.StatusOK)
	waitServerRealtimeOutbox(t, store)
	deletedProjectResponse := httptest.NewRecorder()
	app.ServeHTTP(deletedProjectResponse, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-delete", nil))
	if deletedProjectResponse.Code != http.StatusNotFound {
		t.Fatalf("deleted project status = %d: %s", deletedProjectResponse.Code, deletedProjectResponse.Body.String())
	}
	deletedProjectEvents := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?project_id=project-delete", "local", nil, http.StatusOK)
	projectEvents := deletedProjectEvents["events"].([]any)
	if len(projectEvents) != 2 {
		t.Fatalf("deleted project realtime events=%#v", projectEvents)
	}
	createdEvent := projectEvents[0].(map[string]any)
	createdPayload := createdEvent["payload"].(map[string]any)
	deletedEvent := projectEvents[1].(map[string]any)
	if createdEvent["type"] != "frame_update" || createdPayload["action"] != "project_created" ||
		deletedEvent["type"] != "project_deleted" || len(deletedEvent["invalidations"].([]any)) == 0 ||
		numberValue(deletedEvent["sequence"]) <= numberValue(createdEvent["sequence"]) {
		t.Fatalf("deleted project realtime event sequence=%#v", projectEvents)
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/frames", map[string]any{"id": "frame-1", "agentName": "planner", "status": "running", "conversationType": "task"}, http.StatusOK)
	framesResponse := httptest.NewRecorder()
	app.ServeHTTP(framesResponse, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-1/frames", nil))
	if framesResponse.Code != http.StatusOK || !bytes.Contains(framesResponse.Body.Bytes(), []byte("frame-1")) {
		t.Fatalf("frames = %d: %s", framesResponse.Code, framesResponse.Body.String())
	}
	frameResponse := httptest.NewRecorder()
	app.ServeHTTP(frameResponse, localWorkspaceRequest(http.MethodGet, "/api/go/frames/frame-1", nil))
	if frameResponse.Code != http.StatusOK || !bytes.Contains(frameResponse.Body.Bytes(), []byte("frame-1")) {
		t.Fatalf("frame = %d: %s", frameResponse.Code, frameResponse.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/frames/frame-1", map[string]any{"status": "completed", "name": "Finished"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/frames", map[string]any{"id": "frame-delete", "agentName": "planner", "status": "queued", "conversationType": "task"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/frames/frame-delete", nil, http.StatusOK)
	eventsResponse := httptest.NewRecorder()
	app.ServeHTTP(eventsResponse, localWorkspaceRequest(http.MethodGet, "/api/go/events?frame_id=frame-1", nil))
	if eventsResponse.Code != http.StatusOK || !bytes.Contains(eventsResponse.Body.Bytes(), []byte("frame_created")) {
		t.Fatalf("frame creation events = %d: %s", eventsResponse.Code, eventsResponse.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-1/versions", map[string]any{"projectId": "project-1", "name": "plan.md", "kind": "markdown", "content": "version one", "createdBy": "planner"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-1/versions", map[string]any{"projectId": "project-1", "name": "plan.md", "kind": "markdown", "content": "version two", "createdBy": "planner"}, http.StatusOK)
	artifactsResponse := httptest.NewRecorder()
	app.ServeHTTP(artifactsResponse, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-1/artifacts", nil))
	if artifactsResponse.Code != http.StatusOK || !bytes.Contains(artifactsResponse.Body.Bytes(), []byte("artifact-1")) {
		t.Fatalf("artifacts = %d: %s", artifactsResponse.Code, artifactsResponse.Body.String())
	}
	artifactResponse := httptest.NewRecorder()
	app.ServeHTTP(artifactResponse, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1", nil))
	if artifactResponse.Code != http.StatusOK || !bytes.Contains(artifactResponse.Body.Bytes(), []byte("plan.md")) {
		t.Fatalf("artifact = %d: %s", artifactResponse.Code, artifactResponse.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1", map[string]any{"name": "final.md"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-delete/versions", map[string]any{"projectId": "project-1", "name": "delete.txt", "kind": "text", "content": "delete", "createdBy": "planner"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/artifacts/artifact-delete", nil, http.StatusOK)
	deletedArtifactResponse := httptest.NewRecorder()
	app.ServeHTTP(deletedArtifactResponse, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-delete", nil))
	if deletedArtifactResponse.Code != http.StatusNotFound {
		t.Fatalf("deleted artifact status = %d: %s", deletedArtifactResponse.Code, deletedArtifactResponse.Body.String())
	}

	lineageResponse := httptest.NewRecorder()
	app.ServeHTTP(lineageResponse, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1/lineage", nil))
	if lineageResponse.Code != http.StatusOK {
		t.Fatalf("artifact lineage status = %d: %s", lineageResponse.Code, lineageResponse.Body.String())
	}
	var lineage struct {
		OK      bool                        `json:"ok"`
		Lineage []workspace.ArtifactVersion `json:"lineage"`
	}
	if err := json.NewDecoder(lineageResponse.Body).Decode(&lineage); err != nil {
		t.Fatalf("decode artifact lineage: %v", err)
	}
	if !lineage.OK || len(lineage.Lineage) != 2 || lineage.Lineage[0].VersionNumber != 2 || lineage.Lineage[1].VersionNumber != 1 {
		t.Fatalf("lineage = %#v", lineage)
	}
}

func serveWorkspaceJSON(t *testing.T, app http.Handler, method, target string, body any, wantStatus int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	response := httptest.NewRecorder()
	request := newLoopbackTestRequest(method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d: %s", method, target, response.Code, response.Body.String())
	}
}

func localWorkspaceRequest(method, target string, body io.Reader) *http.Request {
	return authenticatedWorkspaceRequest(method, target, "local", body)
}

func authenticatedWorkspaceRequest(method, target, userID string, body io.Reader) *http.Request {
	request := newLoopbackTestRequest(method, target, body)
	request.Header.Set("X-Synon-User-Id", userID)
	return request
}

func authenticatedWorkspaceURLRequest(method, target string, body io.Reader) *http.Request {
	request := newLoopbackTestRequest(method, target, body)
	userID := request.URL.Query().Get("user_id")
	if userID == "" {
		userID = request.URL.Query().Get("userId")
	}
	if userID == "" {
		userID = "local"
	}
	request.Header.Set("X-Synon-User-Id", userID)
	return request
}
