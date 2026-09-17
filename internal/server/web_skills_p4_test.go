package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestP4WebSkillDirectoryImportDraftLifecycleAndPersistence(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	appServer := New(Options{FileRoot: root, Workspace: store})
	app := appServer.Handler()
	userID := "skill-owner"

	externalRoot := t.TempDir()
	skillRoot := filepath.Join(externalRoot, "directory-skill")
	if err := os.Mkdir(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `---
name: p4-directory-skill
description: Imported from a real granted directory.
---
Use the real directory skill.`
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appServer.upsertHostGrant(userID, externalRoot, "read"); err != nil {
		t.Fatal(err)
	}

	compatJSONRequest(t, app, http.MethodPost, "/api/skills/external-paths", userID, map[string]any{
		"name": "P4 granted source", "path": externalRoot,
	}, http.StatusOK)
	external := compatJSONArrayRequest(t, app, http.MethodGet, "/api/skills/detect-external", userID, nil, http.StatusOK)
	if len(external) != 1 || external[0]["count"] != float64(1) {
		t.Fatalf("external skills=%#v", external)
	}
	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/skills", userID, nil, http.StatusOK)
	if !webSkillTestContains(listed, "p4-directory-skill") {
		t.Fatalf("legacy skill list does not include external skill: %#v", listed)
	}
	info := compatJSONRequest(t, app, http.MethodPost, "/api/skills/info", userID, map[string]any{
		"skill_path": skillRoot,
	}, http.StatusOK)
	if info["name"] != "p4-directory-skill" {
		t.Fatalf("skill info=%#v", info)
	}
	imported := compatJSONRequest(t, app, http.MethodPost, "/api/skills/import", userID, map[string]any{
		"skill_path": skillRoot,
	}, http.StatusCreated)
	if imported["skill_name"] != "p4-directory-skill" {
		t.Fatalf("path import=%#v", imported)
	}
	installed := filepath.Join(root, "skills", "p4-directory-skill", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed skill: %v", err)
	}
	history := compatJSONArrayRequest(t, app, http.MethodGet, "/api/skills/import-history", userID, nil, http.StatusOK)
	if len(history) != 1 || history[0]["status"] != "imported" || history[0]["skill_name"] != "p4-directory-skill" {
		t.Fatalf("import history=%#v", history)
	}

	duplicate := compatJSONRequest(t, app, http.MethodPost, "/api/skills/p4-directory-copy/duplicate", userID, map[string]any{
		"sourceName": "p4-directory-skill",
	}, http.StatusCreated)
	if duplicate["draft"] != true {
		t.Fatalf("duplicate=%#v", duplicate)
	}
	drafts := compatJSONRequest(t, app, http.MethodGet, "/api/skills/drafts", userID, nil, http.StatusOK)
	if !webSkillTestStringArrayContains(drafts["drafts"], "p4-directory-copy") {
		t.Fatalf("drafts=%#v", drafts)
	}
	published := compatJSONRequest(t, app, http.MethodPost, "/api/skills/p4-directory-copy/publish", userID, map[string]any{
		"overwrite": false,
	}, http.StatusOK)
	if published["published"] != true {
		t.Fatalf("published=%#v", published)
	}

	deleteResponse := httptest.NewRecorder()
	app.ServeHTTP(deleteResponse, compatRequest(t, http.MethodDelete, "/api/skills/p4-directory-copy/full", userID, nil))
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if _, found := findCatalogSkill(appServer.skillCatalog, "p4-directory-copy"); found {
		t.Fatal("deleted skill remained in the live catalog")
	}

	restarted := New(Options{FileRoot: root, Workspace: store})
	if _, found := findCatalogSkill(restarted.skillCatalog, "p4-directory-skill"); !found {
		t.Fatal("directory-imported skill did not survive restart")
	}
	restartedHistory := compatJSONArrayRequest(t, restarted.Handler(), http.MethodGet, "/api/skills/import-history", userID, nil, http.StatusOK)
	if len(restartedHistory) != 1 || restartedHistory[0]["operation_id"] != history[0]["operation_id"] {
		t.Fatalf("restarted import history=%#v", restartedHistory)
	}
}

func TestP4WebSkillPathsRequireGrantAndCannotOverrideBundledCatalog(t *testing.T) {
	root := t.TempDir()
	appServer := New(Options{FileRoot: root})
	app := appServer.Handler()
	userID := "skill-owner"
	externalRoot := t.TempDir()
	skillRoot := filepath.Join(externalRoot, "collision")
	if err := os.Mkdir(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(`---
name: synon-runtime
description: Must not override the bundled runtime skill.
---
Unsafe replacement.`), 0o600); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/info", userID, map[string]any{
		"skill_path": skillRoot,
	}, http.StatusForbidden)
	if _, err := appServer.upsertHostGrant(userID, externalRoot, "read"); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/import", userID, map[string]any{
		"skill_path": skillRoot,
	}, http.StatusConflict)
	bundled, found := findCatalogSkill(appServer.skillCatalog, "synon-runtime")
	if !found || bundled.Body == "Unsafe replacement." {
		t.Fatalf("bundled skill was replaced: %#v", bundled)
	}

	outside := t.TempDir()
	outsideSkill := filepath.Join(outside, "escaped")
	if err := os.Mkdir(outsideSkill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSkill, "SKILL.md"), []byte("escaped"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(externalRoot, "linked-outside")
	if err := os.Symlink(outsideSkill, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/skills/info", userID, map[string]any{
		"skill_path": link,
	}, http.StatusForbidden)
}

func webSkillTestContains(values []map[string]any, name string) bool {
	for _, value := range values {
		if value["name"] == name {
			return true
		}
	}
	return false
}

func webSkillTestStringArrayContains(value any, want string) bool {
	items, _ := value.([]any)
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
