package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSkillBundleImportIsAtomicSafeAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()

	archive := buildSkillArchive(t, map[string]string{
		"imported-skill/SKILL.md": `---
name: imported-skill
description: Imported through the compatibility API.
tags: [runtime, imported]
---
Use the imported capability with real files.`,
		"imported-skill/references/contract.md": "# Contract\n\nDurable imported reference.",
	})
	result := postSkillBundle(t, app, "/api/skills/import", "user-1", "imported.skill", archive, http.StatusCreated)
	if result["imported"] != true || result["name"] != "imported-skill" {
		t.Fatalf("import result=%#v", result)
	}
	files := result["files"].([]any)
	if len(files) != 2 || files[0] != "SKILL.md" || files[1] != "references/contract.md" || result["description"] != "Imported through the compatibility API." {
		t.Fatalf("v1.1 import projection=%#v", result)
	}
	installed := filepath.Join(root, "skills", "imported-skill", "SKILL.md")
	if raw, err := os.ReadFile(installed); err != nil || !bytes.Contains(raw, []byte("real files")) {
		t.Fatalf("installed skill=%q err=%v", raw, err)
	}
	if matches := server.skillCatalog.Search("select:imported-skill", 10); len(matches) != 1 || matches[0].Name != "imported-skill" {
		t.Fatalf("live imported catalog=%#v", matches)
	}

	restarted := New(Options{FileRoot: root, Workspace: store})
	if matches := restarted.skillCatalog.Search("select:imported-skill", 10); len(matches) != 1 || matches[0].Name != "imported-skill" {
		t.Fatalf("restarted imported catalog=%#v", matches)
	}

	traversal := buildSkillArchive(t, map[string]string{
		"safe/SKILL.md":  "---\nname: unsafe-skill\ndescription: no\n---\nbody",
		"../escaped.txt": "must not escape",
	})
	postSkillBundle(t, app, "/api/go/preferences/import-bundle", "user-1", "unsafe.zip", traversal, http.StatusBadRequest)
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("zip traversal created escaped file: %v", err)
	}
}

func TestMarkdownSkillImportUsesFilenameFallback(t *testing.T) {
	root := t.TempDir()
	server := New(Options{FileRoot: root})
	result := postSkillBundle(t, server.Handler(), "/api/skills/import", "local", "plain-helper.md", []byte("Use this plain helper."), http.StatusCreated)
	if result["name"] != "plain-helper" {
		t.Fatalf("markdown import=%#v", result)
	}
	if result["description"] != nil {
		t.Fatalf("plain markdown description=%#v", result["description"])
	}
	if matches := server.skillCatalog.Search("select:plain-helper", 5); len(matches) != 1 || matches[0].Name != "plain-helper" {
		t.Fatalf("markdown catalog=%#v", matches)
	}
}

func buildSkillArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func postSkillBundle(t *testing.T, app http.Handler, target, userID, filename string, content []byte, wantStatus int) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Synon-User-Id", userID)
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s status=%d body=%s", target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if wantStatus == http.StatusOK && strings.Contains(response.Body.String(), rootLeakSentinel) {
		t.Fatal("skill import response leaked a sentinel path")
	}
	return decoded
}

const rootLeakSentinel = "never-present-root-sentinel"
