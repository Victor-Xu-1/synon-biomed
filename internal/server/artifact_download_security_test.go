package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestArtifactZIPSanitizesTraversalAndDuplicateNames(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.SaveArtifactVersionInput{
		{ArtifactID: "artifact-1", ProjectID: "project-1", Name: "../same.txt", Kind: "text/plain", Content: []byte("one")},
		{ArtifactID: "artifact-2", ProjectID: "project-1", Name: "same.txt", Kind: "text/plain", Content: []byte("two")},
	} {
		if _, _, err := store.SaveArtifactVersion(input); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/artifacts/download?ids=artifact-1,artifact-2", nil)
	request.Header.Set("X-Synon-User-Id", "user-1")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ZIP = %d: %s", response.Code, response.Body.String())
	}
	entries := zipEntryContents(t, response.Body.Bytes())
	if string(entries["same.txt"]) != "one" || string(entries["same (2).txt"]) != "two" {
		t.Fatalf("sanitized entries = %#v", entries)
	}
	for name := range entries {
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			t.Fatalf("unsafe ZIP entry %q", name)
		}
	}
}

func TestArtifactDownloadSelectionRejectsMoreThanTwoHundredIDs(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()
	ids := make([]string, 201)
	for index := range ids {
		ids[index] = "artifact-" + strings.Repeat("x", index+1)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/artifacts/download?ids="+strings.Join(ids, ","), nil)
	request.Header.Set("X-Synon-User-Id", "user-1")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "max 200") {
		t.Fatalf("over-limit selection = %d: %s", response.Code, response.Body.String())
	}
}
