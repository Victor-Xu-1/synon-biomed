package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWorkspaceSearchViewKeepsAllRecordsAndOriginalPointers(t *testing.T) {
	sources := []map[string]any{}
	for i := 0; i < 60; i++ {
		sources = append(sources, map[string]any{"kind": "search_result", "evidenceState": "discovered", "title": fmt.Sprintf("Unique paper %03d", i), "url": fmt.Sprintf("https://example.org/%d", i), "snippet": "A source-specific result, not a complete article.", "metadata": map[string]any{"trace": strings.Repeat("trace", 100)}})
	}
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"query": "A natural question", "sources": sources, "retrieval": map[string]any{"returned": 60, "exhaustive": false}, "diagnostics": map[string]any{"status": "partial", "backend": "test"}, "results": sources}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), 3000)
	input := map[string]any{"version_id": "ltr-search"}
	var all strings.Builder
	for page := 0; page < 100; page++ {
		value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "search.json", "application/json", int64(len(raw)), input)
		if err != nil {
			t.Fatal(err)
		}
		view := mapValue(value)
		if view["view_format"] != "search-results-display-lines" {
			t.Fatalf("search still hidden behind repeated JSON metadata: %v", view["view_format"])
		}
		if view["source_state"] != "discovered" {
			t.Fatal("discovery promoted to full source evidence")
		}
		if !agentWorkspaceReadResultFits(ctx, view) {
			t.Fatal("search page exceeds transport")
		}
		all.WriteString(stringValue(view["content"]))
		all.WriteByte('\n')
		if view["truncated"] != true {
			break
		}
		next := int(numberValue(view["next_offset"]))
		if next <= int(numberValue(input["offset"])) {
			t.Fatal("non-progressing search page")
		}
		input["offset"] = next
		if page == 99 {
			t.Fatal("pagination did not finish")
		}
	}
	for i := 0; i < 60; i++ {
		if strings.Count(all.String(), fmt.Sprintf("Unique paper %03d", i)) != 1 {
			t.Fatalf("lost/duplicated result %d", i)
		}
	}
	if !strings.Contains(all.String(), "/result/sources/59") || !strings.Contains(all.String(), "partial") {
		t.Fatal("source locator or diagnostics lost")
	}
}

func TestResearchSourceSearchViewPresentsRetainedRecordMetadataAndCompleteAbstract(t *testing.T) {
	abstract := strings.Repeat("Complete provider abstract sentence. ", 55) + "FINAL_ABSTRACT_SENTENCE"
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"query": "current evidence", "sources": []any{map[string]any{
			"kind": "search_result", "evidenceState": "discovered", "title": "Primary article",
			"url": "https://doi.org/10.1000/primary", "snippet": "A bounded discovery snippet.",
			"record": map[string]any{
				"provider": "crossref", "record_depth": "abstract_record", "abstract_complete": true,
				"abstract": abstract, "publisher": "Evidence Publisher", "journal": "Evidence Journal",
				"citation_handle": "doi:10.1000/primary", "citation_text": "Ada Lovelace. Primary article. Evidence Journal. 2026-05-18. DOI:10.1000/primary.",
				"authors": "Ada Lovelace", "identifiers": []any{map[string]any{"namespace": "doi", "value": "10.1000/primary"}},
				"published": map[string]any{"value": "2026-05-18", "precision": "day", "raw": "2026-5-18", "source_field": "published.date-parts"},
			},
		}}, "retrieval": map[string]any{"returned": 1, "provider_total_known": false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := readAgentWorkspaceFile(
		withAgentWorkspaceReadBudget(context.Background(), 12000), bytes.NewReader(raw),
		"search.json", "application/json", int64(len(raw)), map[string]any{"version_id": "search-metadata"},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(view)
	content := stringValue(result["content"])
	for _, expected := range []string{
		"Provider: crossref", "Record depth: abstract_record", "Published: 2026-05-18 (day; published.date-parts)",
		"Identifier: doi:10.1000/primary", "Publisher: Evidence Publisher", "Journal: Evidence Journal",
		"Authors: Ada Lovelace", "Citation handle: doi:10.1000/primary", "Citation: Ada Lovelace. Primary article.",
		"FINAL_ABSTRACT_SENTENCE", "json_pointer: /result/sources/0",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("search reading view lost %q:\n%s", expected, content)
		}
	}
	if result["source_records_with_abstract"] != 1 || result["source_content_included"] != true {
		t.Fatalf("search view depth metadata=%#v", result)
	}
}
