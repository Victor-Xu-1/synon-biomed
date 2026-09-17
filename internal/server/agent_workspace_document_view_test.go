package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func workspaceHTTPDocumentFixture(t *testing.T, body string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"statusCode": 200, "contentType": "text/html; charset=utf-8",
		"url": "https://example.org/articles/study", "body": body,
		"bytesRead": len(body),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWorkspaceHTTPDocumentViewReadsBodyInsteadOfAssetHead(t *testing.T) {
	body := "<html><head><title>Study</title><script>" + strings.Repeat("asset_noise;", 10000) + "</script></head><body><h1>Study findings</h1><p>Measured outcome: 42 ± 3.</p><table><tr><th>Group</th><th>Value</th></tr><tr><td>A</td><td>42</td></tr></table><a href='../references/1'>Original study</a></body></html>"
	raw := workspaceHTTPDocumentFixture(t, body)
	input := map[string]any{"version_id": "ltr-document", "human_description": "Reading a source"}
	result, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 16000), bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), input)
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	content, _ := view["content"].(string)
	for _, want := range []string{"Study findings", "Measured outcome: 42 ± 3.", "Group", "Value", "https://example.org/references/1"} {
		if !strings.Contains(content, want) {
			t.Errorf("readable view missing %q: %.400s", want, content)
		}
	}
	if strings.Contains(content, "asset_noise") {
		t.Fatal("executable head displaced document text")
	}
	if view["view_format"] != "html-readable-display-lines" || view["source_url"] != "https://example.org/articles/study" {
		t.Fatalf("missing document identity: %#v", view)
	}
	if view["truncated"] == true {
		t.Fatal("small complete body unnecessarily truncated")
	}
	// Explicit field selection still reads exact decoded source, not the derived view.
	input["json_pointer"] = "/result/body"
	input["offset"] = 1
	input["limit"] = 1
	original, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), input)
	if err != nil || !strings.Contains(original.(map[string]any)["content"].(string), "<html>") {
		t.Fatalf("original source inaccessible: %v %v", original, err)
	}
}

func TestWorkspaceHTTPDocumentViewRetainsPublisherNavigationAndBibliography(t *testing.T) {
	body := `<html><head><title>Observed study</title><meta name="citation_pdf_url" content="full.pdf"><meta name="citation_doi" content="10.1234/sample"><meta name="citation_author" content="Author One"><link rel="alternate" type="application/xml" href="study.xml"><base href="https://example.org/documents/"><meta name="csrf-token" content="not-bibliography"><script>untrustedExecutable()</script></head><body><p>Abstract only.</p></body></html>`
	raw := workspaceHTTPDocumentFixture(t, body)
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": "ltr-document"})
	if err != nil {
		t.Fatal(err)
	}
	content := stringValue(mapValue(result)["content"])
	for _, want := range []string{"https://example.org/documents/full.pdf", "https://example.org/documents/study.xml", "10.1234/sample", "Author One", "Abstract only."} {
		if !strings.Contains(content, want) {
			t.Errorf("source information discarded: %q", want)
		}
	}
	for _, unwanted := range []string{"not-bibliography", "untrustedExecutable"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("non-document head content exposed: %s", unwanted)
		}
	}
}

func TestWorkspaceHTTPDocumentViewPaginationPreservesEveryParagraph(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<html><head><meta name="citation_pdf_url" content="https://example.org/full.pdf"><meta name="citation_author" content="Expected Author"></head><body>`)
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&body, "<p>record-%03d 中文 %s</p>", i, strings.Repeat("value ", 12))
	}
	body.WriteString("</body></html>")
	raw := workspaceHTTPDocumentFixture(t, body.String())
	ctx := withAgentWorkspaceReadBudget(context.Background(), 3000)
	input := map[string]any{"version_id": "ltr-document", "human_description": "Reading a source"}
	var combined strings.Builder
	for page := 0; page < 150; page++ {
		result, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), input)
		if err != nil {
			t.Fatal(err)
		}
		view := result.(map[string]any)
		if !agentWorkspaceReadResultFits(ctx, view) {
			t.Fatal("page exceeds actual serialized budget")
		}
		combined.WriteString(view["content"].(string))
		combined.WriteByte('\n')
		if view["truncated"] != true {
			break
		}
		next := int(numberValue(view["next_offset"]))
		if next <= int(numberValue(input["offset"])) {
			t.Fatalf("cursor did not progress: %#v", view)
		}
		input["offset"] = next
		if page == 149 {
			t.Fatal("pagination never completed")
		}
	}
	for i := 0; i < 120; i++ {
		token := fmt.Sprintf("record-%03d", i)
		if strings.Count(combined.String(), token) != 1 {
			t.Errorf("record lost or duplicated: %s", token)
		}
	}
	for _, expected := range []string{"https://example.org/full.pdf", "Expected Author"} {
		if strings.Count(combined.String(), expected) != 1 {
			t.Errorf("document metadata lost or duplicated during pagination: %q", expected)
		}
	}
}

func TestWorkspaceHTTPDocumentViewDoesNotPromoteUnavailableOrUnrelatedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"ok":true,"result":{"statusCode":403,"contentType":"text/html","body":"<p>Challenge</p>","url":"https://example.org"}}`,
		`{"document":"<p>literal data</p>","status":"keep JSON"}`,
	} {
		result, err := readAgentWorkspaceFile(context.Background(), strings.NewReader(raw), "data.json", "application/json", int64(len(raw)), map[string]any{"version_id": "ltr-data"})
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["view_format"] == "html-readable-display-lines" {
			t.Fatal("unavailable or untyped data promoted to a source")
		}
	}
}

func TestWorkspaceTextWindowUsesActualMetadataBudgetAndOverflowSafeLimit(t *testing.T) {
	ctx := withAgentWorkspaceReadBudget(context.Background(), 2000)
	text := strings.Repeat("x", 1024) + "\ntail\n"
	view, err := readAgentWorkspaceTextWindow(ctx, strings.NewReader(text), "source.txt", "text/plain", int64(len(text)), 1, int(^uint(0)>>1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stringValue(view["content"]), "tail") || !agentWorkspaceReadResultFits(ctx, view) {
		t.Fatalf("usable read was lost to fixed metadata reserve or integer overflow: %v", view)
	}
}
