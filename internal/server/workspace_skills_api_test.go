package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceSkillCatalogHTTPAPI(t *testing.T) {
	skillRoot := t.TempDir()
	demoDir := filepath.Join(skillRoot, "demo")
	if err := os.MkdirAll(filepath.Join(demoDir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: demo\ndescription: Demo skill\n---\nUse primary evidence."
	if err := os.WriteFile(filepath.Join(demoDir, "SKILL.md"), []byte(skillMD), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(demoDir, "references", "guide.md"), []byte("reference guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{Workspace: store, SkillDirectories: []string{skillRoot}})
	app := server.Handler()

	list := httptest.NewRecorder()
	app.ServeHTTP(list, localWorkspaceRequest(http.MethodGet, "/api/go/skills?user_id=user-1", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte("\"name\":\"demo\"")) ||
		!bytes.Contains(list.Body.Bytes(), []byte("\"enabled\":true")) {
		t.Fatalf("skill catalog = %d: %s", list.Code, list.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/skills/demo/enabled", map[string]any{
		"userId": "user-1", "enabled": false,
	}, http.StatusOK)
	disabled := httptest.NewRecorder()
	app.ServeHTTP(disabled, localWorkspaceRequest(http.MethodGet, "/api/go/skills?user_id=user-1", nil))
	if disabled.Code != http.StatusOK || !bytes.Contains(disabled.Body.Bytes(), []byte("\"enabled\":false")) {
		t.Fatalf("disabled skill catalog = %d: %s", disabled.Code, disabled.Body.String())
	}
	if _, err := server.executeSkillTool(map[string]any{"skill": "demo"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled skill execution error = %v", err)
	}
	if selected, err := server.runtimeSkillsByName([]string{"demo"}, nil); err == nil || len(selected) != 0 {
		t.Fatalf("disabled explicit skill selection = %#v err=%v", selected, err)
	}

	searchResult, err := server.executeSkillSearchTool(map[string]any{"query": "demo", "max_results": 8})
	if err != nil {
		t.Fatal(err)
	}
	searchJSON, err := json.Marshal(searchResult)
	if err != nil || bytes.Contains(searchJSON, []byte("\"name\":\"demo\"")) {
		t.Fatalf("disabled skill search = %s, err = %v", searchJSON, err)
	}
	content := httptest.NewRecorder()
	app.ServeHTTP(content, localWorkspaceRequest(http.MethodGet, "/api/go/skills/demo", nil))
	if content.Code != http.StatusOK || !bytes.Contains(content.Body.Bytes(), []byte("Use primary evidence.")) {
		t.Fatalf("skill content = %d: %s", content.Code, content.Body.String())
	}
	files := httptest.NewRecorder()
	app.ServeHTTP(files, localWorkspaceRequest(http.MethodGet, "/api/go/skills/demo/files", nil))
	if files.Code != http.StatusOK || !bytes.Contains(files.Body.Bytes(), []byte("SKILL.md")) ||
		!bytes.Contains(files.Body.Bytes(), []byte("references/guide.md")) {
		t.Fatalf("skill files = %d: %s", files.Code, files.Body.String())
	}

	download := httptest.NewRecorder()
	app.ServeHTTP(download, localWorkspaceRequest(http.MethodGet, "/api/go/skills/demo/download", nil))
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("skill download = %d content-type=%q: %s", download.Code, download.Header().Get("Content-Type"), download.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(download.Body.Bytes()), int64(download.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range archive.File {
		names[file.Name] = true
	}
	if !names["demo/SKILL.md"] || !names["demo/references/guide.md"] {
		t.Fatalf("skill archive files = %#v", names)
	}
}
