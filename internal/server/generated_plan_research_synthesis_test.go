package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestResearchSynthesisContextCarriesAllCompletedModuleEvidence(t *testing.T) {
	document := generatedPlanDocument{
		Version: 3, TaskSummary: "Assess a target", DesiredOutputs: []string{"report", "evidence table"},
		Phases: []generatedPlanPhase{{
			ID: "research", Name: "Research", Delegations: []generatedPlanDelegation{{
				ID: "track", Name: "Evidence", Steps: []generatedPlanStep{
					{ID: "module-1", Title: "Mechanism", Kind: generatedPlanStepKindResearch, OutputModule: "mechanism", ResearchQuestion: "How does TARGET7 work?"},
					{ID: "module-2", Title: "Development", Kind: generatedPlanStepKindResearch, OutputModule: "development", ResearchQuestion: "What supports TARGET7 development?"},
					{ID: "delivery", Title: "Report", Kind: generatedPlanStepKindDelivery},
				},
			}},
		}},
	}
	receipt := func(event int, call string, cards ...any) map[string]any {
		return map[string]any{
			"event_id": event, "tool_call_id": call, "tool_name": "web_research",
			"material_role": "evidence", "material_state": "preview_with_read_handle",
			"result_reference": map[string]any{
				"version_id": "ltr-" + call, "read_with": "read_file(version_id=\"ltr-" + call + "\")",
			},
			"evidence_cards": cards,
		}
	}
	data := map[string]any{"_step_statuses": map[string]any{
		"module-1": map[string]any{
			"status": "completed", "observations": []any{"Mechanistic observation"},
			"source_receipts": []any{receipt(11, "mechanism",
				map[string]any{"title": "Shared source", "url": "https://example.test/shared", "research_focus": "TARGET7 mechanism", "excerpt": strings.Repeat("mechanism evidence ", 80)},
				map[string]any{"title": "Mechanism source", "url": "https://example.test/mechanism", "excerpt": strings.Repeat("direct mechanism result ", 80)},
			)},
		},
		"module-2": map[string]any{
			"status": "completed", "observations": []any{"Development observation"},
			"source_receipts": []any{receipt(22, "development",
				map[string]any{"title": "Shared source", "url": "https://example.test/shared", "excerpt": strings.Repeat("independent development evidence ", 80)},
				map[string]any{"title": "Development source", "url": "https://example.test/development", "excerpt": strings.Repeat("clinical development result ", 80)},
			)},
		},
		"delivery": map[string]any{"status": "in_progress"},
	}}

	navigation := generatedPlanResearchModelNavigation(document, data)
	synthesis := mapValue(navigation["synthesis_context"])
	if stringValue(synthesis["schema"]) != generatedPlanResearchSynthesisSchema {
		t.Fatalf("synthesis schema=%#v", synthesis)
	}
	if len(anySliceValue(synthesis["modules"])) != 2 || len(anySliceValue(synthesis["receipts"])) != 2 || len(anySliceValue(synthesis["sources"])) != 3 {
		t.Fatalf("cross-module synthesis context=%#v", synthesis)
	}
	encoded, err := json.Marshal(navigation)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Mechanistic observation", "Development observation", "mechanism evidence", "clinical development result", "ltr-mechanism", "ltr-development", "TARGET7 mechanism"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("synthesis context lost %q: %s", expected, encoded)
		}
	}
	if len(encoded) >= int(runnerLargeToolResultInlineLimitBytes) {
		t.Fatalf("model navigation exceeded inline boundary: %d", len(encoded))
	}
	for _, raw := range anySliceValue(navigation["current_investigations"]) {
		investigation := mapValue(raw)
		if _, leaked := investigation["source_receipts"]; leaked {
			t.Fatalf("model navigation repeated full source receipts: %#v", investigation)
		}
	}

	shared := mapValue(anySliceValue(synthesis["sources"])[0])
	if len(stringValueSlice(shared["module_ids"])) != 2 ||
		!slices.Contains(stringValueSlice(shared["research_focuses"]), "TARGET7 mechanism") {
		t.Fatalf("deduplicated source lost module ownership: %#v", shared)
	}
	transition := generatedPlanResearchTransition(document, data, generatedPlanStepIdentity{
		ID: "module-2", Title: "Development", Kind: generatedPlanStepKindResearch,
	}, nil)
	if stringValue(mapValue(transition["synthesis_context"])["schema"]) != generatedPlanResearchSynthesisSchema {
		t.Fatalf("same-unit transition lost synthesis context: %#v", transition)
	}
}

func TestResearchSourceCitationMetadataSurvivesPlanTransitionCompaction(t *testing.T) {
	compacted := compactGeneratedPlanSourceReceipts([]any{map[string]any{
		"event_id": 1, "tool_call_id": "source-call", "tool_name": "web_search", "material_role": "evidence",
		"evidence_cards": []any{map[string]any{
			"title": "Primary article", "url": "https://doi.org/10.1000/primary",
			"citation_handle": "doi:10.1000/primary", "citation_text": "Primary article. DOI:10.1000/primary.",
			"record_depth": "abstract_record", "published_at": "2026-05-18", "excerpt": "source excerpt",
			"source_locator": "/result/sources/0/record/abstract", "excerpt_scope": "tool-result-json-pointer",
			"excerpt_sha256": strings.Repeat("a", 64),
		}},
	}})
	cards := anySliceValue(mapValue(compacted[0])["evidence_cards"])
	if len(cards) != 1 {
		t.Fatalf("compacted cards=%#v", compacted)
	}
	card := mapValue(cards[0])
	for key, expected := range map[string]string{
		"citation_handle": "doi:10.1000/primary", "citation_text": "Primary article. DOI:10.1000/primary.",
		"record_depth": "abstract_record", "published_at": "2026-05-18",
		"source_locator": "/result/sources/0/record/abstract", "excerpt_scope": "tool-result-json-pointer",
		"excerpt_sha256": strings.Repeat("a", 64),
	} {
		if stringValue(card[key]) != expected {
			t.Fatalf("compacted source card lost %s: %#v", key, card)
		}
	}
	if _, leaked := card["excerpt"]; leaked {
		t.Fatalf("ordinary compaction copied excerpt outside the bounded retention step: %#v", card)
	}
}

func TestResearchSynthesisContextDistributesBoundedExcerptsAcrossSources(t *testing.T) {
	document := generatedPlanDocument{Version: 3, TaskSummary: "Research", Phases: []generatedPlanPhase{{
		ID: "phase", Name: "Research", Delegations: []generatedPlanDelegation{{
			ID: "track", Name: "Track", Steps: []generatedPlanStep{{
				ID: "module", Title: "Evidence", Kind: generatedPlanStepKindResearch, OutputModule: "evidence", ResearchQuestion: "What supports the result?",
			}},
		}},
	}}}
	cards := make([]any, 0, 12)
	for index := 0; index < 12; index++ {
		cards = append(cards, map[string]any{
			"title": "Source", "url": "https://example.test/source-" + string(rune('a'+index)),
			"excerpt": strings.Repeat("source-specific measured evidence ", 120),
		})
	}
	data := map[string]any{"_step_statuses": map[string]any{"module": map[string]any{
		"status": "completed", "source_receipts": []any{map[string]any{
			"event_id": 1, "tool_call_id": "source-call", "tool_name": "web_research",
			"material_role": "evidence", "evidence_cards": cards,
		}},
	}}}
	synthesis := generatedPlanResearchSynthesisContext(document, data)
	encoded, err := json.Marshal(synthesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= maxGeneratedPlanResearchSynthesisContextBytes {
		t.Fatalf("synthesis context exceeded bound: %d", len(encoded))
	}
	if len(anySliceValue(synthesis["sources"])) != len(cards) {
		t.Fatalf("bounded projection dropped source identities: %#v", synthesis)
	}
	passages := anySliceValue(synthesis["passages"])
	if len(passages) != len(cards) {
		t.Fatalf("bounded projection dropped source passages: %#v", synthesis)
	}
	for _, raw := range passages {
		if strings.TrimSpace(stringValue(mapValue(raw)["evidence_excerpt"])) == "" {
			t.Fatalf("fair excerpt allocation omitted a source passage: %#v", raw)
		}
	}
}

func TestResearchSynthesisContextPreservesEvidenceFromEveryModuleWithRealisticHandles(t *testing.T) {
	steps := make([]generatedPlanStep, 0, 3)
	statuses := make(map[string]any)
	for moduleIndex := 0; moduleIndex < 3; moduleIndex++ {
		moduleID := fmt.Sprintf("module-%d-with-a-realistic-generated-plan-identity", moduleIndex+1)
		steps = append(steps, generatedPlanStep{
			ID: moduleID, Title: fmt.Sprintf("Module %d", moduleIndex+1), Kind: generatedPlanStepKindResearch,
			OutputModule: fmt.Sprintf("output-%d", moduleIndex+1), ResearchQuestion: fmt.Sprintf("Question %d", moduleIndex+1),
		})
		cards := make([]any, 0, 3)
		for sourceIndex := 0; sourceIndex < 3; sourceIndex++ {
			cards = append(cards, map[string]any{
				"title": fmt.Sprintf("Module %d source %d", moduleIndex+1, sourceIndex+1),
				"url":   fmt.Sprintf("https://example.test/module-%d/source-%d", moduleIndex+1, sourceIndex+1),
				"excerpt": strings.Repeat(
					fmt.Sprintf("measured evidence for module %d source %d ", moduleIndex+1, sourceIndex+1), 80,
				),
			})
		}
		callID := fmt.Sprintf("call-with-realistic-provider-identity-%d", moduleIndex+1)
		statuses[moduleID] = map[string]any{
			"status": "completed", "source_receipts": []any{map[string]any{
				"event_id": moduleIndex + 10, "tool_call_id": callID, "tool_name": "web_research",
				"material_role": "evidence", "material_state": "preview_with_read_handle", "evidence_cards": cards,
				"result_reference": map[string]any{
					"artifact_id": "large-tool-result-with-long-identity-" + callID,
					"version_id":  "ltr-version-with-long-identity-" + callID,
					"read_with":   "read_file(version_id=\"ltr-version-with-long-identity-" + callID + "\")",
					"content_url": "/api/artifacts/long/path/that/is/not-needed/by/the/writer/" + callID,
					"sha256":      strings.Repeat("a", 64), "size_bytes": 500000,
				},
			}},
		}
	}
	document := generatedPlanDocument{Version: 3, TaskSummary: "Research", Phases: []generatedPlanPhase{{
		ID: "phase", Name: "Research", Delegations: []generatedPlanDelegation{{ID: "track", Name: "Track", Steps: steps}},
	}}}
	synthesis := generatedPlanResearchSynthesisContext(document, map[string]any{"_step_statuses": statuses})
	encoded, err := json.Marshal(synthesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= maxGeneratedPlanResearchSynthesisContextBytes {
		t.Fatalf("realistic synthesis context exceeded bound: %d", len(encoded))
	}
	passages := anySliceValue(synthesis["passages"])
	if len(anySliceValue(synthesis["sources"])) != 9 || len(passages) != 9 {
		t.Fatalf("realistic projection dropped source identities or passages: %#v", synthesis)
	}
	coveredModules := make(map[string]bool)
	for _, raw := range passages {
		passage := mapValue(raw)
		if strings.TrimSpace(stringValue(passage["evidence_excerpt"])) == "" {
			t.Fatalf("realistic projection omitted passage text: %#v", passage)
		}
		coveredModules[stringValue(passage["module_id"])] = true
	}
	if len(coveredModules) != 3 {
		t.Fatalf("realistic projection did not retain every module: %#v", coveredModules)
	}
}
