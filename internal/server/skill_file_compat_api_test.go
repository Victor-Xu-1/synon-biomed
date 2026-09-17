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
	"sync"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
)

func TestCompatibilitySkillFilesDownloadEditConcurrencyAndRestart(t *testing.T) {
	root := t.TempDir()
	installedRoot := filepath.Join(root, "skills")
	skillRoot := filepath.Join(installedRoot, "editable")
	if err := os.MkdirAll(filepath.Join(skillRoot, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: editable\ndescription: Editable evidence\n---\nOriginal body."
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	guidePath := filepath.Join(skillRoot, "references", "guide.md")
	if err := os.WriteFile(guidePath, []byte("unique old\nrepeat repeat"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillRoot, "references", "escape.md")); err != nil {
		t.Fatal(err)
	}
	externalRoot := filepath.Join(root, "packaged", "readonly")
	if err := os.MkdirAll(externalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalRoot, "SKILL.md"), []byte("---\nname: readonly\ndescription: Read only\n---\nNo edits."), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := skills.Load([]string{installedRoot, filepath.Dir(externalRoot)})
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: root, Workspace: store, SkillCatalog: catalog})
	app := srv.Handler()

	files := compatJSONRequest(t, app, http.MethodGet, "/api/skills/catalog/editable/files", "user-1", nil, http.StatusOK)
	paths := files["files"].([]any)
	if len(paths) != 2 || paths[0] != "SKILL.md" || paths[1] != "references/guide.md" {
		t.Fatalf("skill files = %#v", files)
	}
	download := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/skills/catalog/editable/download", nil)
	request.Header.Set("X-Synon-User-Id", "user-1")
	app.ServeHTTP(download, request)
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download = %d %#v: %s", download.Code, download.Header(), download.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(download.Body.Bytes()), int64(download.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	archived := map[string]string{}
	for _, entry := range archive.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		_, copyErr := body.ReadFrom(file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("read archive entry: copy=%v close=%v", copyErr, closeErr)
		}
		archived[entry.Name] = body.String()
	}
	if archived["editable/SKILL.md"] != manifest ||
		archived["editable/references/guide.md"] != "unique old\nrepeat repeat" ||
		len(archived) != 2 {
		t.Fatalf("archive = %#v", archived)
	}

	edited := compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "references/guide.md", "old_string": "unique old", "new_string": "unique new",
	}, http.StatusOK)
	if edited["action"] != "edited" || edited["absolute_path"] != guidePath {
		t.Fatalf("edited response = %#v", edited)
	}
	created := compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "references/new.md", "old_string": "", "new_string": "new reference",
	}, http.StatusOK)
	if created["action"] != "created" {
		t.Fatalf("created response = %#v", created)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "references/new.md", "old_string": "", "new_string": "overwrite",
	}, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "references/guide.md", "old_string": "repeat", "new_string": "changed",
	}, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "../outside.txt", "old_string": "outside", "new_string": "leaked",
	}, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/editable/edit", "user-1", map[string]any{
		"path": "references/escape.md", "old_string": "outside", "new_string": "leaked",
	}, http.StatusBadRequest)
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/readonly/edit", "user-1", map[string]any{
		"path": "SKILL.md", "old_string": "No edits.", "new_string": "Changed.",
	}, http.StatusForbidden)

	statuses := make(chan int, 2)
	var group sync.WaitGroup
	for _, replacement := range []string{"winner-a", "winner-b"} {
		group.Add(1)
		go func(replacement string) {
			defer group.Done()
			body, _ := json.Marshal(map[string]any{
				"path": "references/guide.md", "old_string": "unique new", "new_string": replacement,
			})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/skills/editable/edit", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Synon-User-Id", "user-1")
			app.ServeHTTP(response, request)
			statuses <- response.Code
		}(replacement)
	}
	group.Wait()
	close(statuses)
	successes, conflicts := 0, 0
	for status := range statuses {
		if status == http.StatusOK {
			successes++
		} else if status == http.StatusBadRequest {
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent edit statuses: success=%d conflict=%d", successes, conflicts)
	}
	rawGuide, err := os.ReadFile(guidePath)
	if err != nil || !strings.Contains(string(rawGuide), "winner-") || strings.Contains(string(rawGuide), "unique new") {
		t.Fatalf("final guide = %q, err=%v", rawGuide, err)
	}
	rawOutside, err := os.ReadFile(outside)
	if err != nil || string(rawOutside) != "outside secret" {
		t.Fatalf("outside file = %q, err=%v", rawOutside, err)
	}

	restartedCatalog := skills.Load([]string{installedRoot, filepath.Dir(externalRoot)})
	restarted := New(Options{FileRoot: root, Workspace: store, SkillCatalog: restartedCatalog})
	restartedContent := compatJSONRequest(t, restarted.Handler(), http.MethodGet,
		"/api/skills/catalog/editable/content?path=references%2Fnew.md", "user-1", nil, http.StatusOK)
	if restartedContent["content"] != "new reference" {
		t.Fatalf("restarted content = %#v", restartedContent)
	}
}
