package server

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestArtifactHTMLDeliveryAlwaysUsesPassiveOriginSandbox(t *testing.T) {
	const payload = `<html><script>parent.document.body.textContent="unsafe"</script><form action="/api/action"></form></html>`
	for _, media := range []string{"text/html", "text/html; charset=utf-8", "application/xhtml+xml"} {
		for _, disposition := range []string{"inline", "attachment"} {
			t.Run(media+"/"+disposition, func(t *testing.T) {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/api/artifacts/source", nil)
				serveArtifactVersion(response, request, workspace.Artifact{ID: "source", Name: "page.html"}, workspace.ArtifactVersion{ID: "version", CreatedAt: time.Now()}, "page.html", media, disposition, strings.NewReader(payload))
				policy := response.Header().Get("Content-Security-Policy")
				for _, required := range []string{"sandbox;", "default-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
					if !strings.Contains(policy, required) {
						t.Fatalf("missing %q in CSP %q", required, policy)
					}
				}
				if strings.Contains(policy, "allow-scripts") || strings.Contains(policy, "allow-same-origin") || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Body.String() != payload {
					t.Fatalf("HTML integrity or passive boundary changed: %v", response.Header())
				}
			})
		}
	}
}

func TestBaselineArtifactAndVersionDownloads(t *testing.T) {
	app, _, artifactID, firstVersionID, _ := exportFixture(t)
	currentRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID+"?filename=renamed.txt", nil)
	currentRequest.Header.Set("X-Synon-User-Id", "user-1")
	current := httptest.NewRecorder()
	app.ServeHTTP(current, currentRequest)
	if current.Code != http.StatusOK || current.Body.String() != "version two" {
		t.Fatalf("current download = %d: %q", current.Code, current.Body.String())
	}
	if !strings.Contains(current.Header().Get("Content-Disposition"), "renamed.txt") ||
		!strings.HasPrefix(current.Header().Get("Content-Disposition"), "attachment") ||
		current.Header().Get("X-Content-SHA256") == "" {
		t.Fatalf("current headers = %#v", current.Header())
	}
	preflightRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID+"?filename=saved.txt", nil)
	preflightRequest.Header.Set("X-Synon-User-Id", "user-1")
	preflightRequest.Header.Set("Range", "bytes=0-0")
	preflight := httptest.NewRecorder()
	app.ServeHTTP(preflight, preflightRequest)
	if preflight.Code != http.StatusPartialContent || preflight.Body.String() != "v" ||
		preflight.Header().Get("Content-Range") != "bytes 0-0/11" ||
		!strings.Contains(preflight.Header().Get("Content-Disposition"), "saved.txt") {
		t.Fatalf("artifact preflight = %d headers=%#v body=%q", preflight.Code, preflight.Header(), preflight.Body.String())
	}
	currentMetadataRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID+"?include_metadata=true", nil)
	currentMetadataRequest.Header.Set("X-Synon-User-Id", "user-1")
	currentMetadata := httptest.NewRecorder()
	app.ServeHTTP(currentMetadata, currentMetadataRequest)
	currentMetadataBody := decodeJSONMap(t, currentMetadata.Body.Bytes())
	if currentMetadata.Code != http.StatusOK || currentMetadataBody["artifact_id"] != artifactID ||
		currentMetadataBody["version_number"] != float64(2) || currentMetadataBody["checksum"] == nil ||
		!strings.Contains(currentMetadata.Header().Get("Content-Disposition"), "report.txt.metadata.json") {
		t.Fatalf("current metadata = %d headers=%#v body=%#v", currentMetadata.Code, currentMetadata.Header(), currentMetadataBody)
	}
	versionRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/versions/"+firstVersionID, nil)
	versionRequest.Header.Set("X-Synon-User-Id", "user-1")
	version := httptest.NewRecorder()
	app.ServeHTTP(version, versionRequest)
	if version.Code != http.StatusOK || version.Body.String() != "version one" {
		t.Fatalf("version download = %d: %q", version.Code, version.Body.String())
	}
	if !strings.HasPrefix(version.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("text version disposition = %q", version.Header().Get("Content-Disposition"))
	}
	versionMetadataRequest := httptest.NewRequest(http.MethodGet,
		"/api/artifacts/versions/"+firstVersionID+"?include_metadata=true", nil)
	versionMetadataRequest.Header.Set("X-Synon-User-Id", "user-1")
	versionMetadata := httptest.NewRecorder()
	app.ServeHTTP(versionMetadata, versionMetadataRequest)
	versionMetadataBody := decodeJSONMap(t, versionMetadata.Body.Bytes())
	if versionMetadata.Code != http.StatusOK || versionMetadataBody["version_id"] != firstVersionID ||
		versionMetadataBody["version_number"] != float64(1) || versionMetadataBody["filename"] != "report.txt" {
		t.Fatalf("version metadata = %d: %#v", versionMetadata.Code, versionMetadataBody)
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/versions/"+firstVersionID+"?filename=version-saved.txt", nil)
	rangeRequest.Header.Set("X-Synon-User-Id", "user-1")
	rangeRequest.Header.Set("Range", "bytes=0-6")
	ranged := httptest.NewRecorder()
	app.ServeHTTP(ranged, rangeRequest)
	if ranged.Code != http.StatusPartialContent || ranged.Body.String() != "version" ||
		ranged.Header().Get("Content-Range") != "bytes 0-6/11" ||
		!strings.Contains(ranged.Header().Get("Content-Disposition"), "version-saved.txt") {
		t.Fatalf("version range = %d headers=%#v body=%q", ranged.Code, ranged.Header(), ranged.Body.String())
	}
}

func TestArtifactVersionDownloadRequiresExactArtifactVersionPair(t *testing.T) {
	app, _, artifactID, firstVersionID, otherArtifactID := exportFixture(t)
	for _, test := range []struct {
		name       string
		path       string
		ownerID    string
		status     int
		body       string
		metadata   bool
		artifactID string
		versionID  string
	}{
		{
			name: "exact version", path: "/api/artifacts/" + artifactID + "/versions/" + firstVersionID,
			ownerID: "user-1", status: http.StatusOK, body: "version one", artifactID: artifactID, versionID: firstVersionID,
		},
		{
			name: "exact metadata", path: "/api/artifacts/" + artifactID + "/versions/" + firstVersionID + "?include_metadata=true",
			ownerID: "user-1", status: http.StatusOK, metadata: true, artifactID: artifactID, versionID: firstVersionID,
		},
		{
			name: "version belongs to another artifact", path: "/api/artifacts/" + otherArtifactID + "/versions/" + firstVersionID,
			ownerID: "user-1", status: http.StatusNotFound,
		},
		{
			name: "foreign owner", path: "/api/artifacts/" + artifactID + "/versions/" + firstVersionID,
			ownerID: "user-2", status: http.StatusNotFound,
		},
		{
			name: "missing version", path: "/api/artifacts/" + artifactID + "/versions/missing-version",
			ownerID: "user-1", status: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Synon-User-Id", test.ownerID)
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("GET %s = %d: %s", test.path, response.Code, response.Body.String())
			}
			if test.status == http.StatusNotFound {
				body := decodeJSONMap(t, response.Body.Bytes())
				if len(body) != 1 || body["detail"] != "Artifact version unavailable" {
					t.Fatalf("not found body=%#v", body)
				}
				return
			}
			if test.metadata {
				body := decodeJSONMap(t, response.Body.Bytes())
				if body["artifact_id"] != test.artifactID || body["version_id"] != test.versionID {
					t.Fatalf("metadata=%#v", body)
				}
				return
			}
			if response.Body.String() != test.body || response.Header().Get("X-Artifact-Id") != test.artifactID ||
				response.Header().Get("X-Artifact-Version-Id") != test.versionID {
				t.Fatalf("body=%q headers=%#v", response.Body.String(), response.Header())
			}
		})
	}
}

func TestBaselineArtifactDownloadMissingResourceDetails(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()
	for _, test := range []struct {
		path, detail string
	}{
		{path: "/api/artifacts/missing-artifact", detail: "Artifact missing-artifact not found"},
		{path: "/api/artifacts/missing-artifact?include_metadata=true", detail: "Artifact missing-artifact not found"},
		{path: "/api/artifacts/versions/missing-version", detail: "Version missing-version not found"},
		{path: "/api/artifacts/versions/missing-version?include_metadata=true", detail: "Version missing-version not found"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("X-Synon-User-Id", "local")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		body := decodeJSONMap(t, response.Body.Bytes())
		if response.Code != http.StatusNotFound || body["detail"] != test.detail {
			t.Fatalf("GET %s = %d: %#v", test.path, response.Code, body)
		}
	}
}

func TestBaselineSelectedAndFrameArtifactZIPDownloads(t *testing.T) {
	app, rootFrameID, artifactID, _, secondArtifactID := exportFixture(t)
	selectedRequest := httptest.NewRequest(http.MethodGet,
		"/api/artifacts/download?ids="+artifactID+","+secondArtifactID+"&include_metadata=true", nil)
	selectedRequest.Header.Set("X-Synon-User-Id", "user-1")
	selected := httptest.NewRecorder()
	app.ServeHTTP(selected, selectedRequest)
	if selected.Code != http.StatusOK || selected.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("selected zip = %d: %s", selected.Code, selected.Body.String())
	}
	entries := zipEntryContents(t, selected.Body.Bytes())
	if string(entries["report.txt"]) != "version two" || string(entries["data.json"]) != `{"ok":true}` {
		t.Fatalf("selected entries = %#v", entries)
	}
	if _, ok := entries["report.txt.metadata.json"]; !ok {
		t.Fatalf("selected ZIP missing artifact metadata: %#v", entries)
	}

	postBody := bytes.NewBufferString(`["` + secondArtifactID + `"]`)
	postRequest := httptest.NewRequest(http.MethodPost, "/api/artifacts/download", postBody)
	postRequest.Header.Set("Content-Type", "application/json")
	postRequest.Header.Set("X-Synon-User-Id", "user-1")
	posted := httptest.NewRecorder()
	app.ServeHTTP(posted, postRequest)
	if posted.Code != http.StatusOK || string(zipEntryContents(t, posted.Body.Bytes())["data.json"]) != `{"ok":true}` {
		t.Fatalf("POST selected ZIP = %d", posted.Code)
	}

	frameRequest := httptest.NewRequest(http.MethodGet,
		"/api/frames/"+rootFrameID+"/artifacts/download?include_metadata=true", nil)
	frameRequest.Header.Set("X-Synon-User-Id", "user-1")
	frame := httptest.NewRecorder()
	app.ServeHTTP(frame, frameRequest)
	if frame.Code != http.StatusOK {
		t.Fatalf("frame ZIP = %d: %s", frame.Code, frame.Body.String())
	}
	if !strings.Contains(frame.Header().Get("Content-Disposition"), "artifacts_frame-ro.zip") {
		t.Fatalf("frame ZIP disposition = %q", frame.Header().Get("Content-Disposition"))
	}
	frameEntries := zipEntryContents(t, frame.Body.Bytes())
	if string(frameEntries["report.txt"]) != "version two" || string(frameEntries["data.json"]) != `{"ok":true}` {
		t.Fatalf("frame ZIP entries = %#v", frameEntries)
	}
}

func exportFixture(t *testing.T) (http.Handler, string, string, string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-root", ProjectID: "project-1", AgentName: "planner",
		Status: "completed", ConversationType: "task", Name: "Export Session",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactID := "artifact-report"
	_, first, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: "project-1", Name: "report.txt",
		Kind: "text/plain", Content: []byte("version one"), CreatedBy: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: "project-1", Name: "report.txt",
		Kind: "text/plain", Content: []byte("version two"), CreatedBy: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{
			ID: "execution-1", FrameID: root.ID, CellIndex: 1, KernelID: "kernel-1",
			KernelKind: "python", CondaEnv: "replay", Language: "python",
			Source: `input_path = "{{artifact:artifact-data}}"` + "\n" + `print("version one")`,
			Stdout: "version one\n", ExitStatus: "success", Origin: "agent",
			FilesWritten: []string{"report.txt"},
		},
		VersionIDs: []string{first.ID},
	}); err != nil {
		t.Fatal(err)
	}
	secondID := "artifact-data"
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: secondID, ProjectID: "project-1", Name: "data.json",
		Kind: "application/json", Content: []byte(`{"ok":true}`), CreatedBy: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		UPDATE artifact_version_provenance
		SET extracted_code = ?, code_description = ?, agent_name = ?, language = ?,
			environment_snapshot = ?, dependency_mappings = ?
		WHERE version_id = ?`,
		`input_path = "{{artifact:artifact-data}}"`+"\n"+`print("version one")`,
		"reproduce report", "planner", "python",
		`{"name":"replay","channels":["conda-forge"],"dependencies":["python=3.12"]}`,
		`{"inputs":[{"artifact_id":"artifact-data"}],"mapping_status":"complete"}`,
		first.ID,
	); err != nil {
		t.Fatal(err)
	}
	for index, payload := range []map[string]any{
		{"messageUuid": "message-1", "role": "user", "text": "export this"},
		{"artifactId": artifactID, "versionId": first.ID, "step": "draft"},
		{"artifact_id": secondID, "step": "final"},
	} {
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: root.ID, Type: []string{"user_message", "artifact_created", "artifact_created"}[index], Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return New(Options{Workspace: store}).Handler(), root.ID, artifactID, first.ID, secondID
}

func zipEntryContents(t *testing.T, content []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{}
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		_ = opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[file.Name] = data
	}
	return entries
}

func decodeJSONMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
