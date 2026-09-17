package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

const marketplaceTestSHA = "0123456789abcdef0123456789abcdef01234567"

type marketplaceRoundTripFunc func(*http.Request) (*http.Response, error)

func (function marketplaceRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestP4SkillMarketplacePreviewPinnedImportAndProtectedRemoval(t *testing.T) {
	archive := marketplaceSkillArchive(t, map[string]string{
		"skills-main/skills/market-skill/SKILL.md": `---
name: p4-market-skill
description: Imported from a pinned marketplace commit.
---
Use the pinned marketplace skill.`,
		"skills-main/skills/market-skill/references/evidence.md": "# Evidence\n",
	})
	client := &http.Client{Transport: marketplaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		contentType := "application/json"
		body := ""
		switch {
		case request.URL.Host == "api.github.com" && strings.Contains(request.URL.Path, "/commits/"):
			body = `{"sha":"` + marketplaceTestSHA + `"}`
		case request.URL.Host == "api.github.com" && strings.HasSuffix(request.URL.Path, "/license"):
			body = `{"license":{"spdx_id":"Apache-2.0"}}`
		case request.URL.Host == "codeload.github.com":
			contentType = "application/zip"
		default:
			status = http.StatusNotFound
			body = `{}`
		}
		var reader io.ReadCloser
		if contentType == "application/zip" {
			reader = io.NopCloser(bytes.NewReader(archive))
		} else {
			reader = io.NopCloser(strings.NewReader(body))
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status), Body: reader,
			Header: http.Header{"Content-Type": []string{contentType}}, Request: request,
		}, nil
	})}
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	appServer := New(Options{FileRoot: root, Workspace: store, HTTPClient: client})
	app := appServer.Handler()
	userID := "market-owner"

	preview := compatJSONRequest(t, app, http.MethodPost, "/api/marketplace/preview", userID, map[string]any{
		"repo": "acme/skills",
	}, http.StatusOK)
	if preview["sha"] != marketplaceTestSHA || preview["license"] != "Apache-2.0" || preview["slug"] != "acme-skills" {
		t.Fatalf("marketplace preview=%#v", preview)
	}
	previewSkills, _ := preview["skills"].([]any)
	if len(previewSkills) != 1 {
		t.Fatalf("marketplace preview skills=%#v", previewSkills)
	}
	imported := compatJSONRequest(t, app, http.MethodPost, "/api/marketplace/import", userID, map[string]any{
		"repo": "https://github.com/acme/skills", "sha": marketplaceTestSHA,
		"skills": []string{"p4-market-skill"}, "license": "Untrusted-Client-Value",
	}, http.StatusCreated)
	if imported["slug"] != "acme-skills" || !webSkillTestStringArrayContains(imported["imported"], "p4-market-skill") {
		t.Fatalf("marketplace import=%#v", imported)
	}
	sources := compatJSONRequest(t, app, http.MethodGet, "/api/marketplace/sources", userID, nil, http.StatusOK)
	rows, _ := sources["sources"].([]any)
	if len(rows) != 1 {
		t.Fatalf("marketplace sources=%#v", sources)
	}
	source := rows[0].(map[string]any)
	if source["license"] != "Apache-2.0" || source["sha"] != marketplaceTestSHA {
		t.Fatalf("marketplace source provenance=%#v", source)
	}

	manifestPath := filepath.Join(root, "skills", "p4-market-skill", "SKILL.md")
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(original, []byte("\nlocal edit\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, app, http.MethodDelete, "/api/marketplace/sources/acme-skills", userID, nil, http.StatusConflict)
	if err := os.WriteFile(manifestPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	removed := httptest.NewRecorder()
	app.ServeHTTP(removed, compatRequest(t, http.MethodDelete, "/api/marketplace/sources/acme-skills", userID, nil))
	if removed.Code != http.StatusNoContent {
		t.Fatalf("marketplace remove status=%d body=%s", removed.Code, removed.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(manifestPath)); !os.IsNotExist(err) {
		t.Fatalf("marketplace skill directory remained: %v", err)
	}
}

func TestP4SkillMarketplaceRejectsUntrustedRepositoriesAndArchives(t *testing.T) {
	for _, value := range []string{
		"http://github.com/acme/skills", "https://example.com/acme/skills", "git@github.com:acme/skills.git",
		"https://github.com/acme/skills/extra", "../skills",
	} {
		if repository, err := parseGitHubSkillRepository(value); err == nil {
			t.Fatalf("untrusted repository %q accepted as %#v", value, repository)
		}
	}
	malicious := marketplaceSkillArchive(t, map[string]string{
		"../outside/SKILL.md": "unsafe",
	})
	if err := extractMarketplaceSkillArchive(t.TempDir(), malicious); err == nil {
		t.Fatal("marketplace archive traversal was accepted")
	}
}

func marketplaceSkillArchive(t *testing.T, files map[string]string) []byte {
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
