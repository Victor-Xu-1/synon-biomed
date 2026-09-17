package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactWebConversationMessagePreservesAuthoritativeResultCount(t *testing.T) {
	output := map[string]any{
		"ok": true,
		"result": map[string]any{
			"diagnostics": map[string]any{
				"httpBackends": []any{
					map[string]any{"name": "provider-a", "returnedResults": 0},
					map[string]any{"name": "provider-b", "returnedResults": 10},
				},
				"returnedResults": 10,
			},
			"results": []any{strings.Repeat("large-result-record", 300)},
		},
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{
		"type": "tool_call",
		"content": map[string]any{
			"call_id": "search-1",
			"name":    "web_search",
			"status":  "completed",
			"output":  string(rawOutput),
		},
	}

	compacted := compactWebConversationMessage(message)
	content, _ := compacted["content"].(map[string]any)
	compact, _ := content["_compact"].(map[string]any)
	if compact["truncated"] != true {
		t.Fatalf("compact metadata = %#v", compact)
	}
	if got, ok := compact["result_count"].(int); !ok || got != 10 {
		t.Fatalf("result_count = %d, want 10; metadata=%#v", got, compact)
	}
}

func TestCompactWebConversationMessagePrefersActualSourcesOverProviderCounter(t *testing.T) {
	output := map[string]any{
		"ok": true,
		"result": map[string]any{
			"diagnostics": map[string]any{"returnedResults": 1},
			"sources": []any{
				map[string]any{"url": "https://example.org/one"},
				map[string]any{"url": "https://example.org/two"},
				map[string]any{"url": "https://example.org/three"},
			},
		},
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{
		"type": "tool_call",
		"content": map[string]any{
			"call_id": "search-sources",
			"name":    "web_search",
			"status":  "completed",
			"output":  string(rawOutput) + strings.Repeat(" ", compactWebToolOutputBytes),
		},
	}

	compacted := compactWebConversationMessage(message)
	content := compacted["content"].(map[string]any)
	compact := content["_compact"].(map[string]any)
	if got := compact["result_count"]; got != 3 {
		t.Fatalf("result_count=%#v want 3; metadata=%#v", got, compact)
	}
}

func TestCompactWebConversationMessagePreservesUnifiedRetrievalCount(t *testing.T) {
	output := map[string]any{
		"result": map[string]any{
			"retrieval": map[string]any{
				"returned": 387, "provider_total": 1421,
				"complete": false, "truncated": true,
			},
			"page_receipts": []any{strings.Repeat("large-page-receipt", 500)},
		},
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{
		"type": "tool_call",
		"content": map[string]any{
			"call_id": "mcp-search-coverage", "name": "mcp_search",
			"status": "completed", "output": string(rawOutput),
		},
	}

	compacted := compactWebConversationMessage(message)
	content := compacted["content"].(map[string]any)
	compact := content["_compact"].(map[string]any)
	if got := compact["result_count"]; got != 387 {
		t.Fatalf("result_count=%#v want 387; metadata=%#v", got, compact)
	}
}

func TestCompactWebConversationMessageCountsScientificDomainRecords(t *testing.T) {
	output := map[string]any{
		"count": 2,
		"targets": []any{
			map[string]any{
				"target_chembl_id": "CHEMBL3553", "pref_name": "Human TYK2",
				"components": []any{map[string]any{"target_component_xrefs": []any{
					map[string]any{"xref_id": "8TB6"}, map[string]any{"xref_id": "8S9A"},
				}}},
			},
			map[string]any{"target_chembl_id": "CHEMBL2363062", "pref_name": "Janus kinases"},
		},
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{
		"type": "tool_call",
		"content": map[string]any{
			"call_id": "chembl-target-search", "name": "mcp__chembl__target_search",
			"status": "completed", "output": string(rawOutput) + strings.Repeat(" ", compactWebToolOutputBytes),
		},
	}

	compacted := compactWebConversationMessage(message)
	content := compacted["content"].(map[string]any)
	compact := content["_compact"].(map[string]any)
	if got := compact["result_count"]; got != 2 {
		t.Fatalf("result_count=%#v want 2; metadata=%#v", got, compact)
	}
}
