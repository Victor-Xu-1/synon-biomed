package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/webfetch"
)

func TestWorkspaceDocumentRealHTTPAndVersionRetainOriginalSource(t *testing.T) {
	body := "<html><head><script>" + strings.Repeat("asset();", 10000) + "</script></head><body><h1>Research</h1><p>Actual observed data</p><a href='/paper'>Source paper</a></body></html>"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	// This controlled origin is explicit test authority; public-origin SSRF
	// checks remain covered by webfetch/mcpdirectory's normal regression set.
	fetched, err := webfetch.FetchWithOptions(context.Background(), origin.URL, 1<<20, webfetch.Options{ClientForURL: func(context.Context, string) (*http.Client, error) { return origin.Client(), nil }})
	if err != nil || fetched.Body != body || fetched.StatusCode != 200 {
		t.Fatalf("real HTTP fetch failed: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"ok": true, "result": fetched})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAgentSaveArtifactsFixture(t)
	_, version, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{ArtifactID: "document-source", ProjectID: fixture.identity.access.Frame.ProjectID, Name: "source.json", Kind: "application/json", Content: raw, CreatedBy: fixture.identity.access.UserID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), 16000)
	value, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", map[string]any{"version_id": version.ID})
	if err != nil {
		t.Fatal(err)
	}
	view := mapValue(value)
	if !strings.Contains(stringValue(view["content"]), "Actual observed data") || view["source_complete"] != true {
		t.Fatalf("source body not exposed: %v", view)
	}
	path := stringValue(view["file_path"])
	if !filepath.IsAbs(path) || view["file_path_scope"] != "original_source" || !agentWorkspaceReadResultFits(ctx, view) {
		t.Fatal("reading view broke location/budget contract")
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatal("derived view changed immutable materialized source")
	}
	selected := mapValue(view["raw_read_with"])
	selected["offset"] = 1
	selected["limit"] = 1
	result, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", selected)
	if err != nil || !strings.Contains(stringValue(mapValue(result)["content"]), "<html>") {
		t.Fatalf("raw continuation failed: %v", err)
	}
}

func TestWorkspaceDocumentPartialTransportAndLongTitleRemainReadable(t *testing.T) {
	raw := workspaceHTTPDocumentFixture(t, "<html><head><title>"+strings.Repeat("long title ", 5000)+"</title></head><body><p>Body</p></body></html>")
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["result"].(map[string]any)["truncated"] = true
	raw, _ = json.Marshal(envelope)
	ctx := withAgentWorkspaceReadBudget(context.Background(), 3000)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": "ltr-partial"})
	if err != nil {
		t.Fatal(err)
	}
	view := mapValue(value)
	if view["source_complete"] != false || view["truncated"] != true || !agentWorkspaceReadResultFits(ctx, view) {
		t.Fatal("partial source or long title misrepresented")
	}
	if view["document_title_truncated"] != true || stringValue(view["content"]) == "" || int(numberValue(view["next_offset"])) <= 1 {
		t.Fatal("long title must retain readable content and continuation beyond its metadata preview")
	}
}

// Opt-in read-only replay of operator-selected source blobs. Never edits the
// captured task or treats its scientific statements as expected answers.
func TestWorkspaceDocumentCapturedSourceReplay(t *testing.T) {
	paths := filepath.SplitList(os.Getenv("SYNON_TEST_SOURCE_BLOBS"))
	if len(paths) == 0 {
		t.Skip("no captured source blobs selected")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx := withAgentWorkspaceReadBudget(context.Background(), 16000)
			input := map[string]any{"version_id": "ltr-replay"}
			pages, characters := 0, 0
			for {
				value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), input)
				if err != nil {
					t.Fatal(err)
				}
				view := mapValue(value)
				if view["view_format"] != "html-readable-display-lines" && view["view_format"] != "search-results-display-lines" {
					t.Fatalf("source still opaque: %v", view["view_format"])
				}
				if stringValue(view["content"]) == "" || !agentWorkspaceReadResultFits(ctx, view) {
					t.Fatal("empty or oversized view")
				}
				pages++
				characters += len([]rune(stringValue(view["content"])))
				if view["truncated"] != true {
					t.Logf("source_bytes=%d view=%s pages=%d displayed_chars=%d total_lines=%v", len(raw), view["view_format"], pages, characters, view["total_lines"])
					break
				}
				next := int(numberValue(view["next_offset"]))
				if next <= int(numberValue(input["offset"])) || next > int(numberValue(view["total_lines"])) {
					t.Fatal("invalid or non-progressing source cursor")
				}
				input["offset"] = next
			}
		})
	}
}
