package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

type artifactReadFixture struct {
	handler           http.Handler
	artifactID        string
	firstVersionID    string
	secondVersionID   string
	foreignArtifactID string
	foreignVersionID  string
}

func TestArtifactVersionsCompatibilityReadsPersistedHistoryHTTP(t *testing.T) {
	fixture := newArtifactReadFixture(t)

	response := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/"+fixture.artifactID+"/versions")
	if response.Code != http.StatusOK {
		t.Fatalf("versions status = %d: %s", response.Code, response.Body.String())
	}
	var versions []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &versions); err != nil {
		t.Fatalf("decode versions: %v\n%s", err, response.Body.String())
	}
	if len(versions) != 2 {
		t.Fatalf("versions = %#v", versions)
	}
	if versions[0]["version_id"] != fixture.firstVersionID || versions[0]["version_number"] != float64(1) {
		t.Fatalf("first version order/identity = %#v", versions[0])
	}
	if versions[1]["version_id"] != fixture.secondVersionID || versions[1]["version_number"] != float64(2) {
		t.Fatalf("second version order/identity = %#v", versions[1])
	}
	wantFirst := map[string]any{
		"artifact_id": fixture.artifactID, "frame_id": "frame-first", "agent_name": nil,
		"language": "bash", "content_type": "text/plain", "size_bytes": float64(len("first")),
		"file_path": "", "parent_version_id": nil,
	}
	for key, want := range wantFirst {
		if got := versions[0][key]; got != want {
			t.Fatalf("first version %s = %#v, want %#v; row=%#v", key, got, want, versions[0])
		}
	}
	if versions[1]["parent_version_id"] != fixture.firstVersionID || versions[1]["content_type"] != "text/markdown" || versions[1]["agent_name"] != "OPERON" {
		t.Fatalf("second version persisted fields = %#v", versions[1])
	}
	if _, ok := versions[0]["created_at"].(string); !ok {
		t.Fatalf("created_at is not an ISO string: %#v", versions[0]["created_at"])
	}
}

func TestArtifactLineageCompatibilityReadsFullSlimAndLatestHTTP(t *testing.T) {
	fixture := newArtifactReadFixture(t)

	fullResponse := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/versions/"+fixture.secondVersionID+"/lineage")
	if fullResponse.Code != http.StatusOK {
		t.Fatalf("full lineage status = %d: %s", fullResponse.Code, fullResponse.Body.String())
	}
	var full map[string]any
	if err := json.Unmarshal(fullResponse.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"artifact_id", "version_id", "version_number", "filename", "code", "code_description",
		"messages", "environment_snapshot", "language", "interactions", "has_cell_sources",
		"has_messages", "has_environment", "pending", "dependency_mappings",
	}
	if len(full) != len(wantKeys) {
		t.Fatalf("full lineage keys = %#v", full)
	}
	for _, key := range wantKeys {
		if _, ok := full[key]; !ok {
			t.Fatalf("full lineage missing %q: %#v", key, full)
		}
	}
	if full["artifact_id"] != fixture.artifactID || full["version_id"] != fixture.secondVersionID || full["version_number"] != float64(2) {
		t.Fatalf("full lineage identity = %#v", full)
	}
	if full["filename"] != "report.md" || full["code"] != "print('lineage')" || full["code_description"] != "generated code" || full["language"] != "python" {
		t.Fatalf("full lineage scalar fields = %#v", full)
	}
	if full["has_cell_sources"] != true || full["has_messages"] != true || full["has_environment"] != true || full["pending"] != false {
		t.Fatalf("full lineage state flags = %#v", full)
	}
	if len(full["messages"].([]any)) != 1 || full["environment_snapshot"].(map[string]any)["python"] != "3.12" || len(full["interactions"].([]any)) != 1 {
		t.Fatalf("full lineage JSON fields = %#v", full)
	}
	if full["dependency_mappings"].(map[string]any)["mapping_status"] != "complete" {
		t.Fatalf("dependency mappings = %#v", full["dependency_mappings"])
	}

	slimResponse := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/versions/"+fixture.secondVersionID+"/lineage?slim=1")
	if slimResponse.Code != http.StatusOK {
		t.Fatalf("slim lineage status = %d: %s", slimResponse.Code, slimResponse.Body.String())
	}
	var slim map[string]any
	if err := json.Unmarshal(slimResponse.Body.Bytes(), &slim); err != nil {
		t.Fatal(err)
	}
	if slim["messages"] != nil || slim["environment_snapshot"] != nil || slim["interactions"] != nil {
		t.Fatalf("slim lineage retained heavy fields = %#v", slim)
	}
	if slim["code"] != full["code"] || slim["dependency_mappings"].(map[string]any)["mapping_status"] != "complete" || slim["has_messages"] != true {
		t.Fatalf("slim lineage lost retained fields = %#v", slim)
	}

	latestResponse := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/"+fixture.artifactID+"/lineage?slim=1")
	if latestResponse.Code != http.StatusOK {
		t.Fatalf("latest lineage status = %d: %s", latestResponse.Code, latestResponse.Body.String())
	}
	var latest map[string]any
	if err := json.Unmarshal(latestResponse.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest["version_id"] != fixture.secondVersionID {
		t.Fatalf("latest lineage = %#v", latest)
	}
}

func TestArtifactLineageCompatibilityUsesLocalUserWithoutHeader(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "local-project", UserID: "local", Name: "Local"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "local-artifact", ProjectID: "local-project", Name: "local.txt",
		ContentType: "text/plain", Content: strings.NewReader("local lineage"),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	for _, target := range []string{
		"/api/artifacts/local-artifact/lineage?slim=1",
		"/api/artifacts/versions/" + version.ID + "/lineage?slim=1",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("local lineage %s = %d: %s", target, response.Code, response.Body.String())
		}
	}
}

func TestArtifactReadCompatibilityConcealsForeignAndReportsMissingHTTP(t *testing.T) {
	fixture := newArtifactReadFixture(t)
	tests := []struct {
		name   string
		path   string
		userID string
		detail string
	}{
		{name: "missing artifact versions", path: "/api/artifacts/missing-artifact/versions", userID: "owner-a", detail: "Artifact missing-artifact not found"},
		{name: "missing artifact lineage", path: "/api/artifacts/missing-artifact/lineage", userID: "owner-a", detail: "Artifact missing-artifact not found"},
		{name: "missing version lineage", path: "/api/artifacts/versions/missing-version/lineage", userID: "owner-a", detail: "Version missing-version not found"},
		{name: "foreign artifact versions", path: "/api/artifacts/" + fixture.foreignArtifactID + "/versions", userID: "owner-a", detail: "Artifact " + fixture.foreignArtifactID + " not found"},
		{name: "foreign version lineage", path: "/api/artifacts/versions/" + fixture.foreignVersionID + "/lineage", userID: "owner-a", detail: "Artifact " + fixture.foreignArtifactID + " not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := artifactCompatibilityRequest(t, fixture.handler, test.userID, test.path)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["detail"] != test.detail {
				t.Fatalf("body = %#v, want detail %q", body, test.detail)
			}
		})
	}
}

func newArtifactReadFixture(t *testing.T) artifactReadFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, input := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "Project A"},
		{ID: "project-b", UserID: "owner-b", Name: "Project B"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	artifact, first, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-history", ProjectID: "project-a", Name: "report.txt",
		Kind: "text/plain", Content: []byte("first"), CreatedBy: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifact.ID, ProjectID: "project-a", Name: "report.md",
		Kind: "text/markdown", Content: []byte("second"), CreatedBy: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignArtifact, foreignVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-foreign", ProjectID: "project-b", Name: "foreign.txt",
		Kind: "text/plain", Content: []byte("foreign"), CreatedBy: "owner-b",
	})
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO artifact_version_provenance
			(version_id, frame_id, content_type, extracted_code, code_description, lineage_messages,
			 agent_name, language, is_intermediate, dependency_mappings, environment_snapshot,
			 lineage_snapshot_hash, env_snapshot_hash, producing_cell_id, cell_sources, is_checkpoint)
		VALUES (?, ?, ?, NULL, NULL, NULL, NULL, ?, 0, NULL, NULL, NULL, NULL, NULL, NULL, 0)`,
		first.ID, "frame-first", "text/plain", "bash",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO artifact_version_provenance
			(version_id, frame_id, content_type, extracted_code, code_description, lineage_messages,
			 agent_name, language, is_intermediate, dependency_mappings, environment_snapshot,
			 lineage_snapshot_hash, env_snapshot_hash, producing_cell_id, cell_sources, is_checkpoint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, 1)`,
		second.ID, "frame-second", "text/markdown", "print('lineage')", "generated code",
		`[{"role":"assistant","content":"done"}]`, "OPERON", "python",
		`{"inputs":[{"version_id":"`+first.ID+`"}],"mapping_status":"complete"}`,
		`{"python":"3.12"}`, "lineage-hash", "env-hash", "cell-2",
		`[{"kind":"cell","cell_index":2}]`,
	); err != nil {
		t.Fatal(err)
	}

	server := New(Options{Workspace: store})
	mux := http.NewServeMux()
	server.registerWorkspaceArtifactReadCompatibilityRoutes(mux)
	return artifactReadFixture{
		handler: mux, artifactID: artifact.ID, firstVersionID: first.ID, secondVersionID: second.ID,
		foreignArtifactID: foreignArtifact.ID, foreignVersionID: foreignVersion.ID,
	}
}

func artifactCompatibilityRequest(t *testing.T, handler http.Handler, userID, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
