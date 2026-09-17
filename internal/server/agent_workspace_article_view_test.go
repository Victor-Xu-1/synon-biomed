package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/workspace"
)

func TestResearchSourceRecordViewPresentsJATSBodyBeforeFrontMatter(t *testing.T) {
	body := `<article><front><article-meta><title-group><article-title>Primary article</article-title></title-group>` +
		strings.Repeat(`<contrib><name><surname>FrontMatterNoise</surname></name></contrib>`, 150) +
		`<abstract><p>Decision-relevant abstract.</p></abstract></article-meta></front>` +
		`<body><sec><title>Clinical results</title><p>DECISION_RELEVANT_RESULT 42 percent.</p></sec></body></article>`
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"available": true, "recordAvailable": true, "status": "available", "doi": "10.1000/primary",
		"pmcid": "PMC123", "pmid": "123", "title": "Primary article", "publicationDate": "2026-05-18",
		"source": "Europe PMC", "sourceUrl": "https://europepmc.org/articles/PMC123/fullTextXML",
		"contentType": "application/xml", "body": body, "bytesRead": len(body), "complete": true,
		"evidenceState": "full-text-read", "recordDepth": "open_access_full_text",
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 12000),
		bytes.NewReader(raw), "article.json", "application/json", int64(len(raw)),
		map[string]any{"version_id": "article-source"})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(view)
	content := stringValue(result["content"])
	if result["view_format"] != "jats-readable-display-lines" || result["source_record_depth"] != "open_access_full_text" ||
		result["source_content_included"] != true || mapValue(result["raw_read_with"])["json_pointer"] != "/result/body" ||
		!strings.Contains(content, "Decision-relevant abstract") || !strings.Contains(content, "DECISION_RELEVANT_RESULT") ||
		strings.Contains(content, "FrontMatterNoise") {
		t.Fatalf("JATS source view=%#v", result)
	}
}

func TestResearchSourceRecordLargeResultPresentsAttestedJATSViewToModel(t *testing.T) {
	body := `<article><front><article-meta><title-group><article-title>Primary article</article-title></title-group>` +
		strings.Repeat(`<contrib><name><surname>FrontMatterNoise</surname></name></contrib>`, 350) +
		`<abstract><p>Decision-relevant abstract.</p></abstract></article-meta></front>` +
		`<body><sec><title>Results</title><p>MODEL_VISIBLE_RESULT 42 percent.</p></sec></body></article>`
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"available": true, "recordAvailable": true, "status": "available", "doi": "10.1000/primary",
		"pmcid": "PMC123", "title": "Primary article", "source": "Europe PMC",
		"sourceUrl": "https://europepmc.org/articles/PMC123/fullTextXML", "contentType": "application/xml",
		"body": body, "bytesRead": len(body), "complete": true,
		"evidenceState": "full-text-read", "recordDepth": "open_access_full_text",
	}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "article-source", Name: "fetch_article_fulltext"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: runnerLargeToolResultInlineLimitBytes,
	}
	record := workspace.RunnerLargeToolResult{
		ArtifactID: "large-tool-result-" + strings.Repeat("a", 32), VersionID: "article-version",
		ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
	}
	descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := (runnerLargeToolResultAuthority{}).ValidatePreview(context.Background(), input, descriptor); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if json.Unmarshal([]byte(descriptor.Preview), &view) != nil || view["view_format"] != "jats-readable-display-lines" ||
		!strings.Contains(stringValue(view["content"]), "MODEL_VISIBLE_RESULT") ||
		strings.Contains(stringValue(view["content"]), "FrontMatterNoise") ||
		mapValue(view["raw_read_with"])["version_id"] != record.VersionID ||
		descriptor.ReadWith != `read_file(version_id="article-version")` {
		t.Fatalf("model source feedback=%#v", view)
	}
}

func TestResearchSourceRecordViewPresentsCompleteAbstractWhenFullTextIsUnavailable(t *testing.T) {
	abstract := strings.Repeat("Complete abstract sentence. ", 50) + "ABSTRACT_END"
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"available": false, "recordAvailable": true, "sourceUnavailable": false,
		"status": "record_available", "doi": "10.1000/abstract", "pmid": "456",
		"title": "Abstract article", "abstractText": abstract, "authorString": "A Author et al.",
		"publicationDate": "2025", "journalTitle": "Evidence Journal", "source": "Europe PMC",
		"sourceUrl": "https://europepmc.org/article/MED/456", "recordDepth": "abstract_record",
		"evidenceState": "abstract-record-read",
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 12000),
		bytes.NewReader(raw), "article.json", "application/json", int64(len(raw)),
		map[string]any{"version_id": "abstract-source"})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(view)
	content := stringValue(result["content"])
	if result["view_format"] != "article-record-display-lines" || result["source_record_depth"] != "abstract_record" ||
		result["source_content_included"] != true || !strings.Contains(content, "ABSTRACT_END") ||
		!strings.Contains(content, "DOI: 10.1000/abstract") || !strings.Contains(content, "Published: 2025") ||
		!strings.Contains(content, "Citation handle: doi:10.1000/abstract") ||
		!strings.Contains(content, "Citation: A Author et al. Abstract article.") {
		t.Fatalf("abstract source view=%#v", result)
	}
}

func TestResearchSourceHTTPViewSeparatesResponseDatesFromPublisherMetadata(t *testing.T) {
	html := `<html><head><title>Publisher article</title>` +
		`<meta name="citation_publication_date" content="2025-03-27">` +
		`<meta name="citation_doi" content="10.1000/publisher">` +
		`<link rel="canonical" href="https://publisher.example/article"></head>` +
		`<body><article><h1>Publisher article</h1><p>Primary page result.</p></article></body></html>`
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"requestedUrl": "https://publisher.example/start", "url": "https://publisher.example/article",
		"statusCode": 200, "contentType": "text/html; charset=utf-8", "body": html,
		"bytesRead": len(html), "complete": true, "retrievedAt": "2026-09-11T01:02:03Z",
		"responseDate": "Thu, 11 Sep 2025 09:30:00 GMT", "lastModified": "Wed, 10 Sep 2025 08:00:00 GMT",
		"bodySha256": strings.Repeat("a", 64), "bodyHashScope": "returned_response_bytes",
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 12000),
		bytes.NewReader(raw), "page.json", "application/json", int64(len(raw)),
		map[string]any{"version_id": "publisher-page"})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(view)
	if result["document_title"] != "Publisher article" || result["http_requested_url"] != "https://publisher.example/start" ||
		result["http_retrieved_at"] != "2026-09-11T01:02:03Z" ||
		result["http_response_date"] != "Thu, 11 Sep 2025 09:30:00 GMT" ||
		result["http_last_modified"] != "Wed, 10 Sep 2025 08:00:00 GMT" ||
		result["source_content_included"] != true {
		t.Fatalf("HTTP source metadata=%#v", result)
	}
	metadata := anySliceValue(result["source_declared_metadata"])
	links := anySliceValue(result["source_declared_links"])
	if len(metadata) != 2 || len(links) != 1 ||
		strings.Contains(strings.ToLower(stringValue(result["published_at"])), "2025") {
		t.Fatalf("publisher/HTTP date boundary metadata=%#v links=%#v", metadata, links)
	}
	content := stringValue(result["content"])
	if !strings.Contains(content, "citation_publication_date") || !strings.Contains(content, "2025-03-27") ||
		!strings.Contains(content, "https://publisher.example/article") {
		t.Fatalf("publisher-declared metadata not readable:\n%s", content)
	}
}

func TestResearchSourceLargeSearchBeyondLegacyEightMiBRemainsNavigable(t *testing.T) {
	abstract := strings.Repeat("Complete abstract evidence sentence. ", 90000)
	sources := make([]any, 0, 3)
	for index := 0; index < 3; index++ {
		sources = append(sources, map[string]any{
			"kind": "search_result", "evidenceState": "discovered",
			"title": "Large source", "url": "https://example.org/source/" + string(rune('a'+index)),
			"record": map[string]any{
				"provider": "test", "record_depth": "abstract_record", "abstract_complete": true,
				"abstract": abstract, "citation_handle": "pmid:1234567",
			},
		})
	}
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"query": "large source", "sources": sources, "retrieval": map[string]any{"returned": 3},
	}})
	if err != nil || len(raw) <= 8<<20 || len(raw) > maxAgentWorkspaceStructuredJSONBytes {
		t.Fatalf("large search fixture bytes=%d err=%v", len(raw), err)
	}
	digest := sha256.Sum256(raw)
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "large-search", Name: "web_search"}, RawJSON: raw,
		Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: runnerLargeToolResultInlineLimitBytes,
	}
	record := workspace.RunnerLargeToolResult{
		ArtifactID: "large-tool-result-" + strings.Repeat("c", 32), VersionID: "large-search-version",
		ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
	}
	descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
	if err != nil {
		t.Fatal(err)
	}
	var preview map[string]any
	if json.Unmarshal([]byte(descriptor.Preview), &preview) != nil || preview["view_format"] != "search-results-display-lines" ||
		mapValue(preview["read_with"])["version_id"] != record.VersionID {
		t.Fatalf("large search first feedback=%#v", preview)
	}
	read, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 4000), bytes.NewReader(raw),
		"large-search.json", "application/json", int64(len(raw)), map[string]any{"version_id": record.VersionID})
	if err != nil || mapValue(read)["view_format"] != "search-results-display-lines" {
		t.Fatalf("large search read view=%#v err=%v", read, err)
	}
}
