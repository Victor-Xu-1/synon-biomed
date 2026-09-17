package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestArtifactTextVersionCompatibilityWritesCurrentVersionAndFreshProvenance(t *testing.T) {
	fixture := newArtifactReadFixture(t)
	body := strings.NewReader(`{"content":"third","content_type":"text/plain","parent_version_id":"` + fixture.firstVersionID + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/artifacts/"+fixture.artifactID+"/versions", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "owner-a")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("text version = %d: %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created) != 4 || created["artifact_id"] != fixture.artifactID || created["version_number"] != float64(3) ||
		len(created["carried_annotations"].([]any)) != 0 {
		t.Fatalf("text version response = %#v", created)
	}
	versionID, _ := created["version_id"].(string)
	lineage := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/versions/"+versionID+"/lineage")
	if lineage.Code != http.StatusOK {
		t.Fatalf("text lineage = %d: %s", lineage.Code, lineage.Body.String())
	}
	var record map[string]any
	if err := json.Unmarshal(lineage.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	mapping, _ := record["dependency_mappings"].(map[string]any)
	outputs, _ := mapping["outputs"].([]any)
	if mapping["user_edit"] != true || len(outputs) != 1 || record["has_environment"] != false ||
		record["has_messages"] != false || record["language"] != nil {
		t.Fatalf("fresh provenance = %#v", record)
	}
	versions := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/"+fixture.artifactID+"/versions")
	if versions.Code != http.StatusOK || !bytes.Contains(versions.Body.Bytes(), []byte(fixture.firstVersionID)) ||
		!bytes.Contains(versions.Body.Bytes(), []byte(versionID)) {
		t.Fatalf("version history = %d: %s", versions.Code, versions.Body.String())
	}
}

func TestArtifactTextVersionCompatibilityCarriesRealSQLiteAnnotations(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-annotations", UserID: "owner-a", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	artifact, first, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-annotations", ProjectID: "project-annotations",
		Name: "notes.txt", Kind: "text/plain", Content: []byte("first"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAnnotation(workspace.CreateAnnotationInput{
		ProjectID: artifact.ProjectID, TargetKind: "artifact", TargetKey: "av:" + first.ID,
		ContentChecksum: first.ContentSHA256,
		Body:            map[string]any{"artifact_id": artifact.ID, "type": "point", "text": "review"},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
	mux := http.NewServeMux()
	server.registerWorkspaceArtifactReadCompatibilityRoutes(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/artifacts/"+artifact.ID+"/versions",
		strings.NewReader("{\"content\":\"second\",\"parent_version_id\":\""+first.ID+"\"}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "owner-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("annotated text version = %d: %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	carried, ok := created["carried_annotations"].([]any)
	if !ok || len(carried) != 1 {
		t.Fatalf("carried annotations = %#v", created)
	}
	versionID := created["version_id"].(string)
	stored, err := store.ListAnnotations(artifact.ProjectID, "av:"+versionID)
	if err != nil || len(stored) != 1 || stored[0].ContentChecksum == first.ContentSHA256 ||
		stored[0].TargetKey != "av:"+versionID {
		t.Fatalf("stored carried annotations = %#v err=%v", stored, err)
	}
}

func TestArtifactBinaryVersionCompatibilityPersistsInteractionsAndBranch(t *testing.T) {
	fixture := newArtifactReadFixture(t)
	request := artifactBinaryWriteRequest(t, fixture.artifactID, fixture.secondVersionID, "", "owner-a")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("binary version = %d: %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	versionID := created["version_id"].(string)
	lineage := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/versions/"+versionID+"/lineage")
	if lineage.Code != http.StatusOK || !bytes.Contains(lineage.Body.Bytes(), []byte("user-edit")) ||
		!bytes.Contains(lineage.Body.Bytes(), []byte("cell_index")) {
		t.Fatalf("binary lineage = %d: %s", lineage.Code, lineage.Body.String())
	}

	branchRequest := artifactBinaryWriteRequest(t, fixture.artifactID, versionID, "branch.txt", "owner-a")
	branch := httptest.NewRecorder()
	fixture.handler.ServeHTTP(branch, branchRequest)
	if branch.Code != http.StatusCreated {
		t.Fatalf("binary branch = %d: %s", branch.Code, branch.Body.String())
	}
	var branched map[string]any
	if err := json.Unmarshal(branch.Body.Bytes(), &branched); err != nil {
		t.Fatal(err)
	}
	if branched["artifact_id"] == fixture.artifactID || branched["version_number"] != float64(1) {
		t.Fatalf("branch response = %#v", branched)
	}
	branchLineage := artifactCompatibilityRequest(t, fixture.handler, "owner-a",
		"/api/artifacts/"+branched["artifact_id"].(string)+"/lineage?slim=1")
	if branchLineage.Code != http.StatusOK || !bytes.Contains(branchLineage.Body.Bytes(), []byte(`"filename":"branch.txt"`)) {
		t.Fatalf("branch lineage = %d: %s", branchLineage.Code, branchLineage.Body.String())
	}
}

func TestArtifactWriteCompatibilityCamouflagesOwnerAndValidatesParent(t *testing.T) {
	fixture := newArtifactReadFixture(t)
	tests := []struct {
		name, userID, parentID, detail string
		status                         int
	}{
		{name: "foreign owner", userID: "owner-b", parentID: fixture.secondVersionID,
			status: http.StatusNotFound, detail: "Artifact " + fixture.artifactID + " not found"},
		{name: "foreign artifact parent", userID: "owner-a", parentID: fixture.foreignVersionID,
			status: http.StatusBadRequest, detail: "parent_version_id is not a version of this artifact"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := strings.NewReader(`{"content":"edit","parent_version_id":"` + testCase.parentID + `"}`)
			request := httptest.NewRequest(http.MethodPost, "/api/artifacts/"+fixture.artifactID+"/versions", body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Synon-User-Id", testCase.userID)
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, request)
			if response.Code != testCase.status || !bytes.Contains(response.Body.Bytes(), []byte(testCase.detail)) {
				t.Fatalf("write validation = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestArtifactBinaryBranchRejectsUnsafeFilename(t *testing.T) {
	fixture := newArtifactReadFixture(t)
	request := artifactBinaryWriteRequest(t, fixture.artifactID, fixture.secondVersionID, "../escape.bin", "owner-a")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!bytes.Contains(response.Body.Bytes(), []byte("branch_as_filename must be non-empty")) {
		t.Fatalf("unsafe branch filename = %d: %s", response.Code, response.Body.String())
	}
	versions := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/"+fixture.artifactID+"/versions")
	var history []map[string]any
	if err := json.Unmarshal(versions.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("unsafe branch mutated history: %#v", history)
	}
	emptyRequest := artifactBinaryWriteRequest(t, fixture.artifactID, fixture.secondVersionID, "", "owner-a")
	emptyRequest.URL.RawQuery += "&branch_as_filename="
	empty := httptest.NewRecorder()
	fixture.handler.ServeHTTP(empty, emptyRequest)
	if empty.Code != http.StatusBadRequest ||
		!bytes.Contains(empty.Body.Bytes(), []byte("branch_as_filename must be non-empty")) {
		t.Fatalf("empty branch filename = %d: %s", empty.Code, empty.Body.String())
	}
}

func TestArtifactVersionCompatibilityUsesLocalDefaultAndV11ValidationOrder(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "local-project", UserID: "local", Name: "Local"}); err != nil {
		t.Fatal(err)
	}
	artifact, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "local-artifact", ProjectID: "local-project", Name: "notes.txt", Kind: "text/plain", Content: []byte("first"),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	request := httptest.NewRequest(http.MethodPost, "/api/artifacts/"+artifact.ID+"/versions",
		strings.NewReader(`{"content":"second"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("local default text version = %d: %s", response.Code, response.Body.String())
	}

	emptyMissing := httptest.NewRequest(http.MethodPost, "/api/artifacts/missing/versions",
		strings.NewReader(`{"content":""}`))
	emptyMissing.RemoteAddr = "127.0.0.1:12345"
	emptyMissing.Header.Set("Content-Type", "application/json")
	emptyResponse := httptest.NewRecorder()
	app.ServeHTTP(emptyResponse, emptyMissing)
	if emptyResponse.Code != http.StatusBadRequest || !bytes.Contains(emptyResponse.Body.Bytes(), []byte("content is required")) {
		t.Fatalf("empty missing text version = %d: %s", emptyResponse.Code, emptyResponse.Body.String())
	}

	noFile := httptest.NewRequest(http.MethodPost, "/api/artifacts/missing/versions/binary",
		strings.NewReader("not multipart"))
	noFile.RemoteAddr = "127.0.0.1:12345"
	noFile.Header.Set("Content-Type", "text/plain")
	noFileResponse := httptest.NewRecorder()
	app.ServeHTTP(noFileResponse, noFile)
	if noFileResponse.Code != http.StatusNotAcceptable ||
		!bytes.Contains(noFileResponse.Body.Bytes(), []byte("the request is not multipart")) {
		t.Fatalf("missing binary without file = %d: %s", noFileResponse.Code, noFileResponse.Body.String())
	}

	var multipartBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&multipartBody)
	if err := multipartWriter.WriteField("interactions", `[]`); err != nil {
		t.Fatal(err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	emptyMultipart := httptest.NewRequest(http.MethodPost, "/api/artifacts/missing/versions/binary", &multipartBody)
	emptyMultipart.RemoteAddr = "127.0.0.1:12345"
	emptyMultipart.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	emptyMultipartResponse := httptest.NewRecorder()
	app.ServeHTTP(emptyMultipartResponse, emptyMultipart)
	if emptyMultipartResponse.Code != http.StatusBadRequest ||
		!bytes.Contains(emptyMultipartResponse.Body.Bytes(), []byte("No file uploaded")) {
		t.Fatalf("multipart without file = %d: %s", emptyMultipartResponse.Code, emptyMultipartResponse.Body.String())
	}
}

func artifactBinaryWriteRequest(t *testing.T, artifactID, parentID, branchFilename, userID string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "edit.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte{0, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("interactions", `[{"kind":"user-edit","cell_index":7}]`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	target := "/api/artifacts/" + artifactID + "/versions/binary?content_type=application/octet-stream"
	if parentID != "" {
		target += "&parent_version_id=" + parentID
	}
	if branchFilename != "" {
		target += "&branch_as_filename=" + branchFilename
	}
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Synon-User-Id", userID)
	return request
}
