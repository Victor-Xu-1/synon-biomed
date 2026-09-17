package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceArtifactVersionContentAndCopyHTTPAPI(t *testing.T) {
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
	_, first, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "Report",
		Kind: "text/markdown", Content: []byte("first version"), CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "Report",
		Kind: "text/markdown", Content: []byte("second version"), CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	versions := httptest.NewRecorder()
	app.ServeHTTP(versions, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1/versions", nil))
	if versions.Code != http.StatusOK || !bytes.Contains(versions.Body.Bytes(), []byte(second.ID)) ||
		!bytes.Contains(versions.Body.Bytes(), []byte(first.ID)) {
		t.Fatalf("artifact versions = %d: %s", versions.Code, versions.Body.String())
	}
	lineage := httptest.NewRecorder()
	app.ServeHTTP(lineage, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1/lineage", nil))
	if lineage.Code != http.StatusOK || !bytes.Contains(lineage.Body.Bytes(), []byte(fmt.Sprintf("\"parentId\":\"%s\"", first.ID))) {
		t.Fatalf("artifact lineage = %d: %s", lineage.Code, lineage.Body.String())
	}

	currentText := httptest.NewRecorder()
	app.ServeHTTP(currentText, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1/text?max_bytes=6", nil))
	if currentText.Code != http.StatusOK || !bytes.Contains(currentText.Body.Bytes(), []byte("\"text\":\"second\"")) ||
		!bytes.Contains(currentText.Body.Bytes(), []byte("\"truncated\":true")) {
		t.Fatalf("current artifact text = %d: %s", currentText.Code, currentText.Body.String())
	}
	versionText := httptest.NewRecorder()
	app.ServeHTTP(versionText, localWorkspaceRequest(http.MethodGet, "/api/go/artifact-versions/"+first.ID+"/text", nil))
	if versionText.Code != http.StatusOK || !bytes.Contains(versionText.Body.Bytes(), []byte("first version")) {
		t.Fatalf("version text = %d: %s", versionText.Code, versionText.Body.String())
	}
	head := httptest.NewRecorder()
	app.ServeHTTP(head, localWorkspaceRequest(http.MethodGet, "/api/go/artifact-versions/"+first.ID+"/text/head?max_bytes=5", nil))
	if head.Code != http.StatusOK || !bytes.Contains(head.Body.Bytes(), []byte("\"text\":\"first\"")) {
		t.Fatalf("version text head = %d: %s", head.Code, head.Body.String())
	}
	content := httptest.NewRecorder()
	app.ServeHTTP(content, localWorkspaceRequest(http.MethodGet, "/api/go/artifact-versions/"+first.ID+"/content", nil))
	if content.Code != http.StatusOK || content.Body.String() != "first version" ||
		content.Header().Get("X-Content-SHA256") != first.ContentSHA256 {
		t.Fatalf("version content = %d hash=%q body=%q", content.Code, content.Header().Get("X-Content-SHA256"), content.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-1/copy", map[string]any{
		"newArtifactId": "artifact-copy", "targetProjectId": "project-2",
		"name": "Report Copy", "createdBy": "user-1",
	}, http.StatusOK)
	copied, found, err := store.GetArtifact("artifact-copy")
	if err != nil || !found || copied.ProjectID != "project-2" || copied.CurrentVersionNumber != 1 {
		t.Fatalf("copied artifact = %#v, found=%v, err=%v", copied, found, err)
	}
	copiedVersions, err := store.ArtifactLineage("artifact-copy")
	if err != nil || len(copiedVersions) != 1 || copiedVersions[0].ContentSHA256 != second.ContentSHA256 {
		t.Fatalf("copied versions = %#v, err=%v", copiedVersions, err)
	}
	_, binaryVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-binary", ProjectID: "project-1", Name: "Binary",
		Kind: "application/octet-stream", Content: []byte{0xff, 0x00, 0x80},
	})
	if err != nil {
		t.Fatal(err)
	}
	binaryText := httptest.NewRecorder()
	app.ServeHTTP(binaryText, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-binary/text", nil))
	if binaryText.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary text status = %d: %s", binaryText.Code, binaryText.Body.String())
	}
	binaryContent := httptest.NewRecorder()
	app.ServeHTTP(binaryContent, localWorkspaceRequest(http.MethodGet, "/api/go/artifact-versions/"+binaryVersion.ID+"/content", nil))
	if binaryContent.Code != http.StatusOK || !bytes.Equal(binaryContent.Body.Bytes(), []byte{0xff, 0x00, 0x80}) {
		t.Fatalf("binary content = %d: %x", binaryContent.Code, binaryContent.Body.Bytes())
	}
	binaryPayload := []byte{0x00, 0x01, 0xfe, 0xff}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/artifacts/artifact-1/versions/binary", map[string]any{
		"projectId": "project-1", "name": "Report Binary", "kind": "application/octet-stream",
		"contentBase64": base64.StdEncoding.EncodeToString(binaryPayload), "createdBy": "user-1",
	}, http.StatusOK)
	currentBinary := httptest.NewRecorder()
	app.ServeHTTP(currentBinary, localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-1/content", nil))
	if currentBinary.Code != http.StatusOK || !bytes.Equal(currentBinary.Body.Bytes(), binaryPayload) {
		t.Fatalf("current binary artifact = %d: %x", currentBinary.Code, currentBinary.Body.Bytes())
	}
}

func TestWorkspaceArtifactTextPreviewBoundsExternalBlobRead(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("abcd", 512*1024)
	_, version, err := store.SaveArtifactVersionFromReader(context.Background(), workspace.SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-stream", ProjectID: "project-1", Name: "large.txt",
		Kind: "text/plain", Content: strings.NewReader(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	if version.StoragePath == "" || len(version.Content) != 0 {
		t.Fatalf("version was not external: %+v", version)
	}

	response := httptest.NewRecorder()
	New(Options{Workspace: store}).Handler().ServeHTTP(response,
		localWorkspaceRequest(http.MethodGet, "/api/go/artifacts/artifact-stream/text?max_bytes=1024", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("text preview=%d: %s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["bytes"] != float64(len(payload)) || decoded["bytesReturned"] != float64(1024) || decoded["truncated"] != true {
		t.Fatalf("bounded preview metadata=%#v", decoded)
	}
	if len(decoded["text"].(string)) != 1024 {
		t.Fatalf("bounded preview returned %d bytes", len(decoded["text"].(string)))
	}
}
