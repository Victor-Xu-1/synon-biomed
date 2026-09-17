package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

func TestCompactAgentRuntimeListToolResponseBoundsLargeValues(t *testing.T) {
	smallValue := map[string]any{"status": "ok", "count": 2}
	largeValue := strings.Repeat("x", 10000)
	entries := []runtimekv.Entry{
		{Namespace: "agent", Key: "small", Value: smallValue},
		{Namespace: "agent", Key: "large", Value: largeValue},
	}
	response := map[string]any{"entries": entries}
	before, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}

	compacted := compactAgentRuntimeToolResponse("runtime_list", response)
	after, err := json.Marshal(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) >= len(before)/2 {
		t.Fatalf("runtime_list response was not materially bounded: before=%d after=%d", len(before), len(after))
	}

	payload, ok := compacted.(map[string]any)
	if !ok {
		t.Fatalf("compacted response type = %T", compacted)
	}
	compactedEntries, ok := payload["entries"].([]runtimekv.Entry)
	if !ok || len(compactedEntries) != 2 {
		t.Fatalf("compacted entries = %#v", payload["entries"])
	}
	if !reflect.DeepEqual(compactedEntries[0].Value, smallValue) {
		t.Fatalf("small value changed: %#v", compactedEntries[0].Value)
	}
	largeMetadata, ok := compactedEntries[1].Value.(map[string]any)
	if !ok {
		t.Fatalf("large value was not converted to metadata: %T", compactedEntries[1].Value)
	}
	if largeMetadata["truncated"] != true {
		t.Fatalf("large value metadata = %#v", largeMetadata)
	}
	encodedLarge, err := json.Marshal(largeValue)
	if err != nil {
		t.Fatal(err)
	}
	if largeMetadata["valueBytes"] != len(encodedLarge) {
		t.Fatalf("valueBytes = %#v, want %d", largeMetadata["valueBytes"], len(encodedLarge))
	}
	preview, ok := largeMetadata["preview"].(string)
	if !ok || len([]byte(preview)) > agentRuntimeListValuePreviewBytes {
		t.Fatalf("preview length = %d", len([]byte(preview)))
	}
	readWith, ok := largeMetadata["readWith"].(map[string]any)
	if !ok || readWith["tool"] != "runtime_get" || readWith["namespace"] != "agent" || readWith["key"] != "large" {
		t.Fatalf("readWith = %#v", largeMetadata["readWith"])
	}
	if entries[1].Value != largeValue {
		t.Fatal("compaction mutated the exact source value")
	}
	if got := compactAgentRuntimeToolResponse("runtime_get", response); !reflect.DeepEqual(got, response) {
		t.Fatalf("non-list response was changed: %#v", got)
	}
}

func TestCompactAgentRuntimeListToolResponseLeavesSmallListingUnchanged(t *testing.T) {
	response := map[string]any{"entries": []runtimekv.Entry{
		{Namespace: "agent", Key: "small", Value: map[string]any{"ok": true}},
	}}
	if got := compactAgentRuntimeToolResponse("runtime_list", response); !reflect.DeepEqual(got, response) {
		t.Fatalf("small listing changed: %#v", got)
	}
}

func TestCompactUpdateStepStatusResponseKeepsControlStateInline(t *testing.T) {
	receipts := make([]any, 0, 8)
	for index := 0; index < 8; index++ {
		receipts = append(receipts, map[string]any{
			"event_id": index + 1, "tool_call_id": "source-call", "tool_name": "web_research",
			"material_role": "evidence", "material_state": "preview_with_read_handle",
			"request": map[string]any{"operation": "search_and_fetch", "query": "module evidence"},
			"result_reference": map[string]any{
				"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
				"version_id":  "ltr-01234567-89ab-cdef-0123-456789abcdef",
				"read_with":   "read_file(version_id=\"ltr-01234567-89ab-cdef-0123-456789abcdef\")",
			},
			"evidence_cards": []any{
				map[string]any{"title": "Primary source", "url": "https://example.test/source", "excerpt": strings.Repeat("substantive evidence ", 100)},
				map[string]any{"title": "Independent source", "url": "https://example.test/independent", "excerpt": strings.Repeat("independent evidence ", 100)},
			},
		})
	}
	response := map[string]any{
		"ok": true, "status": "in_progress", "step": "module-1", "applied": false,
		"requested_status": "completed", "source_receipts": receipts,
		"research_continuation": map[string]any{
			"reason":       "research_follow_up_required",
			"next_actions": []any{map[string]any{"action": "search_more", "query": "follow-up evidence"}},
		},
		"research_transition": map[string]any{
			"schema": "synon.research_transition.v1", "new_source_receipts": receipts,
			"updated_investigation": map[string]any{"id": "module-1", "status": "in_progress", "source_receipts": receipts},
			"next_investigations":   []any{map[string]any{"id": "module-1", "status": "in_progress", "source_receipts": receipts}},
		},
	}
	before, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	compacted := compactAgentRuntimeToolResponse(updateStepStatusToolName, response)
	after, err := json.Marshal(compacted)
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(compacted)
	if result["status"] != "in_progress" || result["applied"] != false ||
		stringValue(mapValue(result["research_continuation"])["reason"]) != "research_follow_up_required" {
		t.Fatalf("progress control state changed during compaction: %#v", result)
	}
	if len(after) >= int(runnerLargeToolResultInlineLimitBytes) || len(after) >= len(before) {
		t.Fatalf("progress result exceeded its inline projection boundary: before=%d after=%d", len(before), len(after))
	}
	if !strings.Contains(string(after), "substantive evidence") {
		t.Fatal("new source excerpts were removed before same-unit synthesis")
	}
	if len(anySliceValue(mapValue(anySliceValue(result["source_receipts"])[0])["evidence_cards"])) != 0 {
		t.Fatal("cumulative source identities duplicated excerpt payloads")
	}
	if !strings.Contains(string(before), "substantive evidence") {
		t.Fatal("progress compaction mutated the stored source state")
	}
}

func TestCompactUpdateStepStatusKeepsCrossModuleSynthesisContext(t *testing.T) {
	synthesis := map[string]any{
		"schema": generatedPlanResearchSynthesisSchema,
		"modules": []any{
			map[string]any{"id": "module-1", "research_question": "Mechanism"},
			map[string]any{"id": "module-2", "research_question": "Development"},
		},
		"sources": []any{
			map[string]any{"id": "source-1", "url": "https://example.test/one"},
			map[string]any{"id": "source-2", "url": "https://example.test/two"},
		},
		"passages": []any{
			map[string]any{"source_id": "source-1", "module_id": "module-1", "evidence_excerpt": "mechanism result"},
			map[string]any{"source_id": "source-2", "module_id": "module-2", "evidence_excerpt": "development result"},
		},
	}
	response := map[string]any{
		"ok": true, "status": "completed", "step": "module-2",
		"research_transition": map[string]any{
			"schema": "synon.research_transition.v1", "navigation_state_only": true,
			"synthesis_context":   synthesis,
			"next_investigations": []any{map[string]any{"id": "delivery", "status": "pending", "kind": "delivery"}},
		},
	}
	compacted := mapValue(compactAgentRuntimeToolResponse(updateStepStatusToolName, response))
	transition := mapValue(compacted["research_transition"])
	retained := mapValue(transition["synthesis_context"])
	encoded, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{generatedPlanResearchSynthesisSchema, "mechanism result", "development result"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("compaction lost %q from synthesis context: %s", expected, encoded)
		}
	}
}
