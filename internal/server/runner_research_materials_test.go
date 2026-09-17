package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestResearchQualifiedEvidenceMessagesExcludeDiscoveryAndUnusableSourceAttempts(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{
			{ID: "qualified", Name: "web_fetch"},
			{ID: "discovery", Name: "web_search"},
			{ID: "off-topic", Name: "web_fetch"},
			{ID: "calculation", Name: "python"},
		}},
		{Role: "tool", ToolCallID: "qualified", Content: "qualified source"},
		{Role: "tool", ToolCallID: "discovery", Content: "search snippets"},
		{Role: "tool", ToolCallID: "off-topic", Content: "unrelated article"},
		{Role: "tool", ToolCallID: "calculation", Content: "computed result"},
	}
	materials := sessionRunnerResearchMaterialSet{
		Attempts: []sessionRunnerResearchSourceAttempt{
			{ToolCallID: "qualified"}, {ToolCallID: "discovery"}, {ToolCallID: "off-topic"},
		},
		Receipts: []sessionRunnerResearchSourceReceipt{
			{ToolCallID: "qualified", MaterialRole: "evidence"},
			{ToolCallID: "discovery", MaterialRole: "discovery"},
		},
	}
	filtered := researchQualifiedEvidenceMessages(messages, materials)
	if len(filtered) != 3 || len(filtered[0].ToolCalls) != 2 ||
		filtered[0].ToolCalls[0].ID != "qualified" || filtered[0].ToolCalls[1].ID != "calculation" ||
		filtered[1].ToolCallID != "qualified" || filtered[2].ToolCallID != "calculation" {
		t.Fatalf("qualified evidence projection retained a lower-tier source: %#v", filtered)
	}
}

func TestResearchQualifiedEvidenceMessagesKeepOnlyEligibleWebResearchDocuments(t *testing.T) {
	content, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"sources": []any{
			map[string]any{"url": "https://example.test/qualified"},
			map[string]any{"url": "https://example.test/off-topic"},
		},
		"documents": []any{
			map[string]any{
				"url": "https://example.test/qualified", "content": "target mechanism evidence",
				"readReceipt": map[string]any{"queryRelevant": true},
			},
			map[string]any{
				"url": "https://example.test/off-topic", "content": "unrelated subject",
				"readReceipt": map[string]any{"queryRelevant": false},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "research-call", Name: "web_research"}}},
		{Role: "tool", ToolCallID: "research-call", Content: string(content)},
	}
	materials := sessionRunnerResearchMaterialSet{
		Attempts: []sessionRunnerResearchSourceAttempt{{ToolCallID: "research-call"}},
		Receipts: []sessionRunnerResearchSourceReceipt{{
			ToolCallID: "research-call", ToolName: "web_research", MaterialRole: "evidence",
		}},
	}
	filtered := researchQualifiedEvidenceMessages(messages, materials)
	if len(filtered) != 2 || !strings.Contains(filtered[1].Content, "target mechanism evidence") ||
		strings.Contains(filtered[1].Content, "unrelated subject") || strings.Contains(filtered[1].Content, "off-topic") {
		t.Fatalf("web research projection leaked a lower-tier document: %#v", filtered)
	}
}

func TestResearchSourceReceiptsFollowActiveInvestigationAndReadState(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: tool + "-call",
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	events := []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName, map[string]any{"step": "investigation-a", "status": "in_progress"}, map[string]any{"ok": true}),
		checkpoint(2, "web_search", map[string]any{"query": "primary evidence"}, map[string]any{
			"artifact_id": "source-a", "version_id": "version-a", "truncated": true,
			"read_with": "read_file(version_id=\"version-a\")",
			"sources":   []any{map[string]any{"url": "https://example.test/discovery"}},
		}),
		checkpoint(3, "read_file", map[string]any{"version_id": "version-a"}, map[string]any{"ok": true}),
		checkpoint(4, updateStepStatusToolName, map[string]any{"step": "investigation-a", "status": "completed"}, map[string]any{"ok": true}),
		checkpoint(5, updateStepStatusToolName, map[string]any{"step": "investigation-b", "status": "in_progress"}, map[string]any{"ok": true}),
		checkpoint(6, "web_fetch", map[string]any{"url": "https://example.test/record"}, map[string]any{
			"ok": true, "result": map[string]any{"body": strings.Repeat("substantive source evidence ", 80)},
		}),
	}
	receipts := researchMaterialsFromCheckpoints(server, events, map[string][]string{
		"investigation-b": {"substantive source evidence"},
	}).Receipts
	if len(receipts) != 2 {
		t.Fatalf("source receipts=%#v", receipts)
	}
	if receipts[0].EventID != 2 || receipts[0].MaterialState != "read_requested" ||
		receipts[0].MaterialRole != "discovery" ||
		len(receipts[0].InvestigationIDs) != 1 || receipts[0].InvestigationIDs[0] != "investigation-a" ||
		stringValue(receipts[0].Request["query"]) != "primary evidence" {
		t.Fatalf("first source was not bound to its investigation/read state: %#v", receipts[0])
	}
	if receipts[1].EventID != 6 || receipts[1].MaterialState != "inline_result" ||
		receipts[1].MaterialRole != "evidence" ||
		len(receipts[1].InvestigationIDs) != 1 || receipts[1].InvestigationIDs[0] != "investigation-b" ||
		len(receipts[1].EvidenceCards) != 1 ||
		stringValue(receipts[1].EvidenceCards[0]["research_focus"]) != "substantive source evidence" {
		t.Fatalf("second source crossed investigation boundaries: %#v", receipts[1])
	}
}

func TestResearchSourceReceiptsExcludeUnavailableAndNonSubstantiveResults(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: tool + "-call",
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	receipts := researchMaterialsFromCheckpoints(server, []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName, map[string]any{"step": "module", "status": "in_progress"}, map[string]any{"ok": true}),
		checkpoint(2, "web_fetch", map[string]any{"url": "https://example.test/maintenance"}, map[string]any{
			"ok": true, "result": map[string]any{"statusCode": 200, "body": "website under maintenance"},
		}),
		checkpoint(3, "fetch_article_fulltext", map[string]any{"doi": "10.1000/unavailable"}, map[string]any{
			"ok": true, "result": map[string]any{"available": false, "recordAvailable": false, "status": "not_available"},
		}),
	}).Receipts
	if len(receipts) != 0 {
		t.Fatalf("unavailable pages became research evidence: %#v", receipts)
	}
}

func TestResearchSourceReceiptsRejectSubstantiveButOffTopicWebFetch(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: fmt.Sprintf("%s-%d", tool, eventID),
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	events := []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName,
			map[string]any{"step": "module", "status": "in_progress"},
			map[string]any{"ok": true, "step": "module", "status": "in_progress"}),
		checkpoint(2, "web_fetch", map[string]any{
			"url": "https://example.test/off-topic", "prompt": "Fetch the POLQ inhibitor study",
		}, map[string]any{"ok": true, "result": map[string]any{
			"body": strings.Repeat("Paediatric dosage-form manufacturing and clinical use. ", 40),
		}}),
		checkpoint(3, "web_fetch", map[string]any{
			"url": "https://example.test/polq", "prompt": "Fetch the POLQ inhibitor study",
		}, map[string]any{"ok": true, "result": map[string]any{
			"body": strings.Repeat("POLQ inhibitor evidence in homologous-recombination-deficient cancer. ", 30),
		}}),
	}
	materials := researchMaterialsFromCheckpoints(
		New(Options{FileRoot: t.TempDir()}), events,
		map[string][]string{"module": {"POLQ inhibitor development"}},
	)
	if len(materials.Attempts) != 2 || materials.Attempts[0].MaterialUsable || !materials.Attempts[1].MaterialUsable {
		t.Fatalf("off-topic source qualification=%#v", materials.Attempts)
	}
	if len(materials.Receipts) != 1 || materials.Receipts[0].EventID != 3 {
		t.Fatalf("off-topic body entered research evidence: %#v", materials.Receipts)
	}
}

func TestResearchWebFetchModuleFocusCannotBeOverriddenByGenericRequestOverlap(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"url": "https://example.test/candidate", "prompt": "inhibitor tumor development evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := agentruntime.ToolCall{ID: "fetch-call", Name: "web_fetch", Arguments: arguments}
	offTopic := map[string]any{"ok": true, "result": map[string]any{
		"body": strings.Repeat("unrelated inhibitor tumor development evidence ", 40),
	}}
	if researchWebFetchRelevantToInvestigation(call, offTopic, []string{"ABC1 inhibitor evidence"}) {
		t.Fatal("generic request overlap overrode the structured module entity")
	}
	onTopic := map[string]any{"ok": true, "result": map[string]any{
		"body": strings.Repeat("ABC1 inhibitor tumor development evidence ", 40),
	}}
	if !researchWebFetchRelevantToInvestigation(call, onTopic, []string{"ABC1 inhibitor evidence"}) {
		t.Fatal("matching structured module evidence was rejected")
	}
}

func TestResearchSourceReceiptsRetainSubstantivePartialWebResearch(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: tool + "-call",
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	receipts := researchMaterialsFromCheckpoints(server, []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName,
			map[string]any{"step": "module", "status": "in_progress"},
			map[string]any{"ok": true, "step": "module", "status": "in_progress"}),
		checkpoint(2, "web_research", map[string]any{
			"operation": "search_and_fetch", "query": "模块证据", "query_variants": []any{"module evidence"},
		}, map[string]any{"ok": true, "result": map[string]any{
			"documents": []any{map[string]any{
				"url": "https://example.test/primary", "content": strings.Repeat("substantive evidence ", 100),
				"readReceipt": map[string]any{"deepRead": true},
			}},
			"sources": []any{
				map[string]any{"status": "fetched", "url": "https://example.test/primary"},
				map[string]any{"status": "unavailable", "url": "https://blocked.test/source"},
			},
			"quality": map[string]any{"deepReadSources": 1, "meetsTarget": false},
		}}),
	}).Receipts
	if len(receipts) != 1 || receipts[0].MaterialRole != "evidence" ||
		len(receipts[0].InvestigationIDs) != 1 || receipts[0].InvestigationIDs[0] != "module" {
		t.Fatalf("usable documents were discarded with an unavailable sibling source: %#v", receipts)
	}
	if len(receipts[0].EvidenceCards) != 1 ||
		!strings.Contains(stringValue(receipts[0].EvidenceCards[0]["excerpt"]), "substantive evidence") ||
		stringValue(receipts[0].EvidenceCards[0]["url"]) != "https://example.test/primary" {
		t.Fatalf("source content was reduced to a handle before synthesis: %#v", receipts[0])
	}
	value := researchSourceReceiptValue(receipts[0])
	if len(anySliceValue(value["evidence_cards"])) != 1 {
		t.Fatalf("durable research navigation omitted source evidence cards: %#v", value)
	}
}

func TestResearchEvidenceCardsPreferRelevantPassagesOverPageChrome(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{"query": "TARGET7 inhibitor response evidence"})
	if err != nil {
		t.Fatal(err)
	}
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "research-call", Name: "web_research", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{
		"documents": []any{map[string]any{
			"title": "Primary study", "url": "https://example.test/study",
			"discovery": map[string]any{"matchedQueryVariant": "TARGET7 inhibitor response evidence"},
			"content": strings.Join([]string{
				"Account navigation journal login and subscription controls for the publication website.",
				"TARGET7 inhibitor treatment produced a durable response in the measured experimental endpoint and the independent validation cohort.",
				"Publisher footer advertising related journals and account services to website visitors.",
			}, "\n"),
			"readReceipt": map[string]any{"deepRead": true, "queryRelevant": true},
		}},
	}})
	if len(cards) != 1 {
		t.Fatalf("evidence cards=%#v", cards)
	}
	excerpt := stringValue(cards[0]["excerpt"])
	if !strings.Contains(excerpt, "durable response") || strings.Contains(excerpt, "Account navigation") || strings.Contains(excerpt, "Publisher footer") ||
		stringValue(cards[0]["research_focus"]) != "TARGET7 inhibitor response evidence" {
		t.Fatalf("query-focused excerpt=%q", excerpt)
	}
}

func TestResearchEvidenceCardKeepsTableHeaderWithSelectedRow(t *testing.T) {
	content := strings.Join([]string{
		"## Efficacy results",
		"Regimen\tN\tConfirmed ORR\tData cutoff",
		"TARGET7 combination\t12\t92%\t2026-05-01",
		"Unrelated control\t80\t5%\t2025-01-01",
	}, "\n")
	excerpt := researchEvidenceCardExcerpt("TARGET7 combination 92%", content)
	if !strings.Contains(excerpt, "Regimen\tN\tConfirmed ORR\tData cutoff") ||
		!strings.Contains(excerpt, "TARGET7 combination\t12\t92%") {
		t.Fatalf("selected table row lost its header context: %q", excerpt)
	}
}

func TestResearchEvidenceCardKeepsSeveralRelevantSections(t *testing.T) {
	content := strings.Join([]string{
		"## Trial status\nTARGET7 program status is recruiting in the registry record with a stated cutoff.",
		"## Company update\nTARGET7 program status changed after the company portfolio review.",
		"## Efficacy\nTARGET7 program status includes an early measured response in a defined cohort.",
		"## Limitations\nTARGET7 program status remains uncertain because follow-up and sample size are limited.",
	}, "\n")
	excerpt := researchEvidenceCardExcerpt("TARGET7 program status", content)
	for _, expected := range []string{"recruiting", "portfolio review", "measured response", "sample size"} {
		if !strings.Contains(excerpt, expected) {
			t.Fatalf("multi-section evidence excerpt lost %q: %q", expected, excerpt)
		}
	}
}

func TestResearchEvidenceCardsUseActiveInvestigationWhenWebFetchHasNoQuery(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{"url": "https://example.test/study"})
	if err != nil {
		t.Fatal(err)
	}
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "fetch-call", Name: "web_fetch", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{
		"url": "https://example.test/study", "contentType": "text/plain",
		"body": strings.Join([]string{
			"Account navigation journal login and subscription controls for the publication website.",
			"TARGET7 inhibitor treatment produced a durable response in the measured experimental endpoint and the independent validation cohort.",
			"Publisher footer advertising related journals and account services to website visitors.",
		}, "\n"),
	}}, "TARGET7 inhibitor response evidence")
	if len(cards) != 1 {
		t.Fatalf("evidence cards=%#v", cards)
	}
	excerpt := stringValue(cards[0]["excerpt"])
	if !strings.Contains(excerpt, "durable response") || strings.Contains(excerpt, "Account navigation") ||
		strings.Contains(excerpt, "Publisher footer") ||
		stringValue(cards[0]["research_focus"]) != "TARGET7 inhibitor response evidence" {
		t.Fatalf("active-investigation excerpt=%q card=%#v", excerpt, cards[0])
	}
}

func TestResearchSourceEvidenceCardsRetainCompleteSearchAbstractAndCitationIdentity(t *testing.T) {
	arguments, _ := json.Marshal(map[string]any{"query": "TARGET7 clinical response"})
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "search-call", Name: "web_search", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{"sources": []any{map[string]any{
		"title": "TARGET7 clinical study", "url": "https://doi.org/10.1000/target7",
		"record": map[string]any{
			"record_depth": "abstract_record", "abstract_complete": true,
			"abstract":        "TARGET7 treatment produced a clinical response in the measured cohort.",
			"citation_handle": "doi:10.1000/target7", "citation_text": "TARGET7 clinical study. DOI:10.1000/target7.",
		},
	}}}}, "TARGET7 clinical response")
	if len(cards) != 1 || !strings.Contains(stringValue(cards[0]["excerpt"]), "clinical response") ||
		cards[0]["citation_handle"] != "doi:10.1000/target7" || cards[0]["record_depth"] != "abstract_record" ||
		cards[0]["source_locator"] != "/result/sources/0/record/abstract" ||
		cards[0]["excerpt_scope"] != "tool-result-json-pointer" || len(stringValue(cards[0]["excerpt_sha256"])) != 64 {
		t.Fatalf("search abstract evidence cards=%#v", cards)
	}
}

func TestResearchSearchCardsKeepSnippetOnlyCandidatesAsDiscoveryNavigation(t *testing.T) {
	arguments, _ := json.Marshal(map[string]any{"query": "TARGET7 current program status"})
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "search-call", Name: "web_search", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{"sources": []any{
		map[string]any{
			"title": "Complete abstract", "url": "https://doi.org/10.1000/abstract",
			"snippet": "bounded presentation",
			"record": map[string]any{
				"record_depth": "abstract_record", "abstract_complete": true,
				"abstract": "TARGET7 complete abstract with measured current program evidence.",
			},
		},
		map[string]any{
			"title": "Primary company update", "url": "https://company.example.test/program-update",
			"snippet": "TARGET7 program status changed according to the primary company update.",
		},
	}}})
	if len(cards) != 2 {
		t.Fatalf("search discovery cards=%#v", cards)
	}
	second := cards[1]
	if second["record_depth"] != "search_snippet" || second["source_locator"] != "/result/sources/1/snippet" ||
		second["excerpt_scope"] != "tool-result-json-pointer" ||
		!strings.Contains(stringValue(second["excerpt"]), "program status changed") {
		t.Fatalf("snippet-only discovery candidate was lost or upgraded: %#v", second)
	}
}

func TestResearchSourceEvidenceCardsSelectRelevantJATSPassage(t *testing.T) {
	arguments, _ := json.Marshal(map[string]any{"doi": "10.1000/target7"})
	body := `<article><front><article-meta>` + strings.Repeat(`<contrib><name><surname>HeaderNoise</surname></name></contrib>`, 100) +
		`<abstract><p>Background context.</p></abstract></article-meta></front><body>` +
		`<sec><title>Clinical results</title><p>TARGET7 produced a durable clinical response in the measured cohort.</p></sec>` +
		`</body></article>`
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "article-call", Name: "fetch_article_fulltext", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{
		"title": "TARGET7 clinical study", "sourceUrl": "https://europepmc.org/article/PMC1",
		"contentType": "application/xml", "body": body, "recordDepth": "open_access_full_text",
		"doi": "10.1000/target7", "citation_handle": "doi:10.1000/target7",
	}}, "TARGET7 clinical response")
	if len(cards) != 1 || !strings.Contains(stringValue(cards[0]["excerpt"]), "durable clinical response") ||
		strings.Contains(stringValue(cards[0]["excerpt"]), "HeaderNoise") || cards[0]["record_depth"] != "open_access_full_text" ||
		cards[0]["source_locator"] != "/result/body" || cards[0]["excerpt_scope"] != "jats-readable-projection" ||
		len(stringValue(cards[0]["excerpt_sha256"])) != 64 {
		t.Fatalf("JATS evidence cards=%#v", cards)
	}
}

func TestResearchReceiptBindsPrePlanEvidenceByQualifiedCardReference(t *testing.T) {
	receipt := sessionRunnerResearchSourceReceipt{
		EventID: 17, ToolCallID: "pre-plan-research", ToolName: "web_research", MaterialRole: "evidence",
		EvidenceCards: []map[string]any{
			{"title": "Mechanism", "url": "https://example.test/mechanism", "excerpt": "measured mechanism evidence"},
			{"title": "Development", "url": "https://example.test/development", "excerpt": "observed development evidence"},
		},
	}
	bound := researchEvidenceReceiptsForStep(
		[]sessionRunnerResearchSourceReceipt{receipt}, "module-after-plan",
		[]string{"https://unsupported.test/discovery, https://example.test/mechanism"},
	)
	if len(bound) != 1 || bound[0].ToolCallID != receipt.ToolCallID {
		t.Fatalf("qualified pre-plan evidence was not rebound by its source card: %#v", bound)
	}
	refs := researchEvidenceReceiptReferences(bound)
	if len(refs) != 2 || refs[0] != "https://example.test/mechanism" || refs[1] != "https://example.test/development" {
		t.Fatalf("qualified source projection leaked discovery references: %#v", refs)
	}
}

func TestResearchEvidenceCardsRetainEveryQualifiedDocumentIdentity(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{"query": "TARGET7 inhibitor evidence"})
	if err != nil {
		t.Fatal(err)
	}
	documents := make([]any, 0, 4)
	for index := 0; index < 4; index++ {
		documents = append(documents, map[string]any{
			"title": fmt.Sprintf("Source %d", index+1),
			"url":   fmt.Sprintf("https://source-%d.example/record", index+1),
			"content": strings.Repeat(
				fmt.Sprintf("TARGET7 inhibitor source %d measured evidence. ", index+1), 30,
			),
			"readReceipt": map[string]any{"deepRead": true, "queryRelevant": true},
		})
	}
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "research-call", Name: "web_research", Arguments: arguments,
	}, map[string]any{"ok": true, "result": map[string]any{"documents": documents}})
	if len(cards) != 4 {
		t.Fatalf("qualified document identities=%d cards=%#v", len(cards), cards)
	}
	for index, card := range cards {
		want := fmt.Sprintf("https://source-%d.example/record", index+1)
		if stringValue(card["url"]) != want || strings.TrimSpace(stringValue(card["excerpt"])) == "" {
			t.Fatalf("card %d=%#v want url=%s", index, card, want)
		}
	}
}

func TestResearchEvidenceCardsProjectGenericStructuredSourceRecord(t *testing.T) {
	arguments, _ := json.Marshal(map[string]any{"accessions": []any{"GSE295600"}})
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "geo-details", Name: "mcp__omics-archives__geo_get_series", Arguments: arguments,
	}, map[string]any{"records": []any{map[string]any{
		"accession": "GSE295600",
		"title":     "Combinatorial delivery of low-dose irradiation and immunotherapy",
		"status":    "Public on Apr 07 2026",
	}}}, "GSE295600 irradiation immunotherapy")
	if len(cards) != 1 {
		t.Fatalf("generic source record cards=%#v", cards)
	}
	card := cards[0]
	if card["source_identifier"] != "GSE295600" || card["source_status"] != "Public on Apr 07 2026" ||
		card["source_locator"] != "/records/0" || card["excerpt_scope"] != "normalized-result-json-pointer" ||
		!strings.Contains(stringValue(card["excerpt"]), "Combinatorial delivery") {
		t.Fatalf("generic structured source identity was not preserved: %#v", card)
	}
}

func TestResearchMaterialsRetainUnavailableWebResearchDiscoveryWithoutEvidence(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"operation": "search_and_fetch", "query": "模块证据", "query_variants": []any{"module evidence"},
	})
	result, _ := json.Marshal(map[string]any{
		"ok": true, "sourceUnavailable": true,
		"result": map[string]any{
			"candidateSources": []any{map[string]any{"url": "https://candidate.example/source"}},
			"documents":        []any{},
			"retrievalDecision": map[string]any{
				"continueRecommended": true, "stopReason": "no_relevant_sources",
			},
			"nextActions": []any{map[string]any{
				"action": "fetch", "url": "https://candidate.example/source",
			}},
		},
	})
	stepInput, _ := json.Marshal(map[string]any{"step": "module", "status": "in_progress"})
	stepResult, _ := json.Marshal(map[string]any{"ok": true, "step": "module", "status": "in_progress"})
	materials := researchMaterialsFromCheckpoints(New(Options{FileRoot: t.TempDir()}), []sessionRunnerResearchCheckpoint{
		{EventID: 1, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: updateStepStatusToolName, ToolPhase: "completed", ToolCallID: "start-module",
			ToolInput: stepInput, ToolResult: stepResult,
		}},
		{EventID: 2, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: "web_research", ToolPhase: "completed", ToolCallID: "discovery-without-evidence",
			ToolInput: input, ToolResult: result,
		}},
	})
	if len(materials.Attempts) != 1 || materials.Attempts[0].MaterialUsable ||
		len(materials.Receipts) != 1 || materials.Receipts[0].MaterialRole != "discovery" ||
		len(researchEvidenceReceiptsForStep(materials.Receipts, "module", nil)) != 0 {
		t.Fatalf("unavailable discovery crossed the process/evidence boundary: %#v", materials)
	}
	if pending := researchPendingSourceAttemptContinuation(materials.Attempts); len(pending) == 0 || stringValue(researchContinuationFirstAction(pending)["url"]) != "https://candidate.example/source" {
		t.Fatalf("unavailable discovery lost its source-owned route: %#v", pending)
	}
}

func TestResearchSourceContinuationClosesOnlyAfterItsFollowUpRuns(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: fmt.Sprintf("%s-%d", tool, eventID),
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	baseResult := func(continueRecommended bool) map[string]any {
		result := map[string]any{
			"documents": []any{map[string]any{
				"url": "https://example.test/source", "content": strings.Repeat("source evidence ", 100),
				"readReceipt": map[string]any{"deepRead": true},
			}},
			"quality": map[string]any{"deepReadSources": 1},
			"retrievalDecision": map[string]any{
				"continueRecommended": continueRecommended, "stopReason": "max_rounds_reached",
			},
			"research_session": map[string]any{"id": "wr-session", "mode": "start"},
		}
		if continueRecommended {
			result["nextActions"] = []any{map[string]any{
				"action": "search_more", "query": "follow-up source frontier",
			}}
		}
		return map[string]any{"ok": true, "result": result}
	}
	events := []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName,
			map[string]any{"step": "module", "status": "in_progress"},
			map[string]any{"ok": true, "step": "module", "status": "in_progress"}),
		checkpoint(2, "web_research", map[string]any{
			"operation": "search_and_fetch", "query": "initial source frontier",
			"research_session": map[string]any{"mode": "start"},
		}, baseResult(true)),
	}
	materials := researchMaterialsFromCheckpoints(server, events)
	receipts := materials.Receipts
	pending := researchPendingSourceAttemptContinuation(materials.Attempts)
	if len(pending) == 0 || stringValue(mapValue(pending["research_session"])["id"]) != "wr-session" ||
		stringValue(researchContinuationFirstAction(pending)["query"]) != "follow-up source frontier" {
		t.Fatalf("source continuation was not retained: receipts=%#v pending=%#v", receipts, pending)
	}

	followUp := checkpoint(3, "web_search", map[string]any{
		"query": "model-authored next module",
	}, map[string]any{"ok": true, "results": []any{map[string]any{
		"url": "https://example.test/follow-up", "snippet": strings.Repeat("follow-up source evidence ", 20),
	}}})
	followUp.Checkpoint.ExecutedToolInput, _ = json.Marshal(map[string]any{
		"query": "follow-up source frontier",
	})
	events = append(events, followUp)
	if pending = researchPendingSourceAttemptContinuation(researchMaterialsFromCheckpoints(server, events).Attempts); len(pending) != 0 {
		t.Fatalf("executed follow-up did not close the prior source frontier: %#v", pending)
	}
}

func TestResearchAttemptLedgerSeparatesTerminalFailuresFromEvidence(t *testing.T) {
	checkpoint := func(eventID int64, tool, phase string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: phase, ToolCallID: fmt.Sprintf("%s-%d", tool, eventID),
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	openResult := map[string]any{"ok": true, "result": map[string]any{
		"documents": []any{map[string]any{
			"url": "https://example.test/source", "content": strings.Repeat("source evidence ", 100),
			"readReceipt": map[string]any{"deepRead": true},
		}},
		"quality":           map[string]any{"deepReadSources": 1},
		"retrievalDecision": map[string]any{"continueRecommended": true, "stopReason": "frontier_open"},
		"nextActions":       []any{map[string]any{"action": "search_more", "query": "exact next action"}},
		"research_session":  map[string]any{"id": "session-a", "mode": "continue"},
	}}
	events := []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName, "completed",
			map[string]any{"step": "module", "status": "in_progress"},
			map[string]any{"ok": true, "step": "module", "status": "in_progress"}),
		checkpoint(2, "web_research", "completed", map[string]any{
			"operation": "search_and_fetch", "query": "initial action",
		}, openResult),
		checkpoint(3, "web_search", "failed", map[string]any{
			"query": "exact next action",
		}, map[string]any{"ok": false, "error": map[string]any{"code": "upstream_reset"}}),
	}
	preflight := checkpoint(4, "web_research", prestartToolFailurePhase, map[string]any{
		"operation": "search_and_fetch", "query": "must not count",
	}, map[string]any{
		"ok": false, "executed": false, "status": "network_preflight_required", "message": "approval required",
	})
	preflight.Checkpoint.RejectedBeforeExecution = true
	events = append(events, preflight)

	materials := researchMaterialsFromCheckpoints(server, events)
	if len(materials.Receipts) != 1 || len(materials.Attempts) != 2 {
		t.Fatalf("evidence/attempt ledgers were conflated: %#v", materials)
	}
	if pending := researchPendingSourceAttemptContinuation(materials.Attempts); len(pending) != 0 {
		t.Fatalf("terminal failed follow-up did not consume its exact action: %#v", pending)
	}
}

func TestResearchContinuationIsNotReplacedByUnrelatedAttempt(t *testing.T) {
	attempts := []sessionRunnerResearchSourceAttempt{
		{
			EventID: 1, ToolCallID: "source-1", ToolName: "web_research",
			Request: map[string]any{"query": "initial"}, ResultSHA256: "first",
			ResearchContinuation: map[string]any{
				"next_actions": []any{map[string]any{"action": "search_more", "query": "required follow-up"}},
			},
		},
		{
			EventID: 2, ToolCallID: "source-2", ToolName: "web_research",
			Request: map[string]any{"query": "unrelated route"}, ResultSHA256: "second",
			ResearchContinuation: map[string]any{
				"next_actions": []any{map[string]any{"action": "search_more", "query": "replacement route"}},
			},
		},
	}
	pending := researchPendingSourceAttemptContinuation(attempts)
	if got := stringValue(researchContinuationFirstAction(pending)["query"]); got != "required follow-up" {
		t.Fatalf("unrelated attempt replaced active source frontier: %#v", pending)
	}
}

func TestResearchContinuationAdvancesAcrossSourceOwnedAlternatives(t *testing.T) {
	attempts := []sessionRunnerResearchSourceAttempt{
		{
			EventID: 1, ToolCallID: "discovery", ToolName: "web_research", MaterialUsable: true,
			ResultSHA256: "discovery-result",
			ResearchContinuation: map[string]any{
				"research_session": map[string]any{"id": "discovery-session", "mode": "continue"},
				"next_actions": []any{
					map[string]any{"action": "fetch", "url": "https://blocked.example/first"},
					map[string]any{"action": "fetch", "url": "https://available.example/second"},
				},
			},
		},
		{
			EventID: 2, ToolCallID: "first-fetch", ToolName: "web_fetch", MaterialUsable: false,
			Request: map[string]any{"url": "https://blocked.example/first"},
		},
	}
	pending := researchPendingSourceAttemptContinuation(attempts)
	actions := anySliceValue(pending["next_actions"])
	if len(actions) != 1 || stringValue(mapValue(actions[0])["url"]) != "https://available.example/second" {
		t.Fatalf("failed route did not advance to the next discovered alternative: %#v", pending)
	}
	attempts = append(attempts, sessionRunnerResearchSourceAttempt{
		EventID: 3, ToolCallID: "second-fetch", ToolName: "web_fetch", MaterialUsable: true,
		Request: map[string]any{"url": "https://available.example/second"},
	})
	if pending = researchPendingSourceAttemptContinuation(attempts); len(pending) != 0 {
		t.Fatalf("usable alternative did not settle the discovered frontier: %#v", pending)
	}
}

func TestResearchContinuationRetiresCompoundModelRouteAtCompatibilityBoundary(t *testing.T) {
	actions := researchContinuationActions([]any{
		map[string]any{"action": "search_more", "query": "next evidence", "tool": "web_research"},
		map[string]any{"action": "fetch", "url": "https://example.test/source"},
	}, "web_research")
	if len(actions) != 2 {
		t.Fatalf("normalized continuation actions=%#v", actions)
	}
	if got := researchContinuationExpectedTool(mapValue(actions[0])); got != "web_search" {
		t.Fatalf("search continuation tool=%q want web_search", got)
	}
	if got := researchContinuationExpectedTool(mapValue(actions[1])); got != "web_fetch" {
		t.Fatalf("fetch continuation tool=%q want web_fetch", got)
	}
	legacy := map[string]any{"action": "search_more", "query": "persisted", "tool": "web_research"}
	if got := researchContinuationExpectedTool(legacy); got != "web_search" {
		t.Fatalf("persisted compound continuation tool=%q want web_search", got)
	}
}

func TestResearchModuleConsumesUnavailableFollowUpWithoutTreatingItAsEvidence(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Investigate a source frontier",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Evidence", "steps": []any{map[string]any{
						"id": "step-1", "title": "Inspect source", "description": "Inspect source evidence.",
						"kind": "research", "output_module": "Evidence", "research_question": "What does the source establish?",
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Sources available"},
			},
			"_step_statuses": map[string]any{"step-1": map[string]any{
				"status": "in_progress", "title": "Inspect source", "description": "Inspect source evidence.",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpoint("step-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-start-call",
		"toolInput":  map[string]any{"step": "step-1", "status": "in_progress"},
		"toolResult": map[string]any{"ok": true, "step": "step-1", "status": "in_progress"},
	})
	appendCheckpoint("source-opens-frontier", map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "source-open",
		"toolInput": map[string]any{"operation": "search_and_fetch", "query": "initial evidence"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"documents": []any{map[string]any{
				"url": "https://example.test/primary", "content": strings.Repeat("substantive primary evidence ", 80),
				"readReceipt": map[string]any{"deepRead": true},
			}},
			"quality":           map[string]any{"deepReadSources": 1},
			"retrievalDecision": map[string]any{"continueRecommended": true, "stopReason": "frontier_open"},
			"nextActions":       []any{map[string]any{"action": "search_more", "query": "exact follow-up"}},
			"research_session":  map[string]any{"id": "research-session", "mode": "continue"},
		}},
	})
	appendCheckpoint("source-follow-up-unavailable", map[string]any{
		"toolName": "web_search", "toolPhase": "completed", "toolCallId": "source-unavailable",
		"toolInput":         map[string]any{"query": "model moved elsewhere"},
		"executedToolInput": map[string]any{"query": "exact follow-up"},
		"toolResult":        map[string]any{"ok": true, "status": "unavailable", "sourceUnavailable": true},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	completed, err := fixture.server.executeAgentUpdateStepStatus(
		withTranscriptRunnerChatRun(context.Background(), run), fixture.stream.FrameID, "finish-after-attempt", map[string]any{
			"step": "step-1", "status": "completed", "observations": []any{"Primary evidence was retained."},
		},
	)
	if err != nil || stringValue(mapValue(completed)["status"]) != "completed" ||
		len(anySliceValue(mapValue(completed)["source_receipts"])) != 1 ||
		len(mapValue(mapValue(completed)["research_continuation"])) != 0 {
		t.Fatalf("executed unavailable follow-up did not close navigation independently of evidence: result=%#v err=%v", completed, err)
	}
}

func TestResearchSourceRoutingUsesAppliedStepStatus(t *testing.T) {
	checkpoint := func(eventID int64, tool string, input, result any) sessionRunnerResearchCheckpoint {
		inputJSON, _ := json.Marshal(input)
		resultJSON, _ := json.Marshal(result)
		return sessionRunnerResearchCheckpoint{EventID: eventID, Checkpoint: sessionRunnerDurableToolCheckpoint{
			ToolName: tool, ToolPhase: "completed", ToolCallID: tool + "-call",
			ToolInput: inputJSON, ToolResult: resultJSON,
		}}
	}
	server := New(Options{FileRoot: t.TempDir()})
	receipts := researchMaterialsFromCheckpoints(server, []sessionRunnerResearchCheckpoint{
		checkpoint(1, updateStepStatusToolName,
			map[string]any{"step": "module-1", "status": "in_progress"},
			map[string]any{"ok": true, "step": "module-1", "status": "in_progress"}),
		checkpoint(2, updateStepStatusToolName,
			map[string]any{"step": "module-1", "status": "completed"},
			map[string]any{"ok": true, "step": "module-1", "status": "in_progress", "applied": false}),
		checkpoint(3, "web_fetch", map[string]any{"url": "https://example.test/recovery"}, map[string]any{
			"ok": true, "result": map[string]any{"body": strings.Repeat("recovery evidence ", 100)},
		}),
	}).Receipts
	if len(receipts) != 1 || len(receipts[0].InvestigationIDs) != 1 || receipts[0].InvestigationIDs[0] != "module-1" {
		t.Fatalf("recovery source was orphaned by a rejected completion request: %#v", receipts)
	}
}

func TestResearchSourceReceiptsRestoreExternalizedWebResearch(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpoint("module-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "module-start-call",
		"toolInput": map[string]any{"step": "module-1", "status": "in_progress"}, "toolResult": map[string]any{"ok": true},
	})
	rawResult, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"documents": []any{map[string]any{
			"url": "https://example.test/primary", "content": strings.Repeat("substantive evidence ", 100),
			"readReceipt": map[string]any{"deepRead": true},
		}},
		"sources": []any{
			map[string]any{"status": "fetched", "url": "https://example.test/primary"},
			map[string]any{"status": "unavailable", "url": "https://blocked.test/source"},
		},
		"quality": map[string]any{"deepReadSources": 1, "meetsTarget": false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-0123456789abcdef0123456789abcdef",
		ProjectID:  fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
		StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID,
		ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt, SourceEventID: 1,
		ToolName: "web_research", ToolCallID: "web-research-large", Content: rawResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"artifact_id": record.ArtifactID, "version_id": record.VersionID,
		"sha256": record.ContentSHA256, "size_bytes": record.SizeBytes,
		"content_type": record.ContentType, "outcome": "partial", "truncated": true,
		"content_url": "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
		"preview":     "externalized web research result",
	}
	appendCheckpoint("web-research-completed", map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "web-research-large",
		"toolInput": map[string]any{
			"operation": "search_and_fetch", "query": "模块证据", "query_variants": []any{"module evidence"},
		},
		"toolResult": descriptor,
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	materials, err := fixture.server.sessionRunnerResearchMaterials(context.Background(), run)
	receipts := materials.Receipts
	if err != nil || len(receipts) != 1 || receipts[0].ToolCallID != "web-research-large" ||
		receipts[0].MaterialRole != "evidence" || len(receipts[0].InvestigationIDs) != 1 ||
		receipts[0].InvestigationIDs[0] != "module-1" ||
		stringValue(receipts[0].ResultReference["artifact_id"]) != record.ArtifactID ||
		!researchSourceReceiptMatchesReference(receipts[0], "artifact:"+record.ArtifactID) {
		t.Fatalf("externalized research material was not restored and routed: receipts=%#v err=%v", receipts, err)
	}
}

func TestResearchSourceRoutingRestoresExternalizedProgressResult(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpoint("module-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "module-start-call",
		"toolInput":  map[string]any{"step": "module-1", "status": "in_progress"},
		"toolResult": map[string]any{"ok": true, "step": "module-1", "status": "in_progress"},
	})
	progressResult, err := json.Marshal(map[string]any{
		"ok": true, "step": "module-1", "status": "in_progress", "requested_status": "completed", "applied": false,
		"research_continuation": map[string]any{"reason": "research_follow_up_required"},
		"retained_state":        strings.Repeat("durable navigation state ", 1800),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-fedcba98765432100123456789abcdef",
		ProjectID:  fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
		StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID,
		ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt, SourceEventID: 1,
		ToolName: updateStepStatusToolName, ToolCallID: "redirected-completion", Content: progressResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"artifact_id": record.ArtifactID, "version_id": record.VersionID,
		"sha256": record.ContentSHA256, "size_bytes": record.SizeBytes,
		"content_type": record.ContentType, "outcome": "succeeded", "truncated": true,
		"content_url": "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
	}
	appendCheckpoint("redirected-completion", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "redirected-completion",
		"toolInput": map[string]any{"step": "module-1", "status": "completed"}, "toolResult": descriptor,
	})
	appendCheckpoint("source-after-redirect", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "follow-up-source",
		"toolInput": map[string]any{"url": "https://example.test/follow-up"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"body": strings.Repeat("substantive follow-up evidence ", 80),
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	materials, err := fixture.server.sessionRunnerResearchMaterials(context.Background(), run)
	receipts := materials.Receipts
	if err != nil || len(receipts) != 1 || receipts[0].ToolCallID != "follow-up-source" ||
		len(receipts[0].InvestigationIDs) != 1 || receipts[0].InvestigationIDs[0] != "module-1" {
		t.Fatalf("externalized applied progress result lost active routing: receipts=%#v err=%v", receipts, err)
	}
}

func TestResearchQueryCoverageIsSharedAcrossWebAndMCPRoutes(t *testing.T) {
	step := generatedPlanStepIdentity{
		ID: "module-mechanism",
		DiscoveryQueries: []generatedPlanQuery{
			{Language: "zh", Query: "聚合酶 theta 作用机制"},
			{Language: "en", Query: "DNA polymerase theta mechanism"},
		},
	}
	receipts := []sessionRunnerResearchSourceReceipt{
		{
			ToolCallID: "web-discovery", ToolName: "web_search", MaterialRole: "discovery",
			InvestigationIDs: []string{step.ID}, Request: map[string]any{"query": "聚合酶 theta 作用机制"},
		},
		{
			ToolCallID: "mcp-discovery", ToolName: "mcp__literature__search", MaterialRole: "discovery",
			InvestigationIDs: []string{step.ID}, Request: map[string]any{
				"parameters": map[string]any{"search_term": "DNA polymerase theta mechanism"},
			},
		},
	}
	covered := researchQueryLanguagesForStep(receipts, step, nil)
	if strings.Join(covered, ",") != "zh,en" {
		t.Fatalf("module query coverage split across retrieval routes: %#v", covered)
	}
	if missing := researchMissingQueryLanguages(step, covered[:1]); len(missing) != 1 || missing[0] != "en" {
		t.Fatalf("missing language was not preserved across route handoff: %#v", missing)
	}
}

func TestResearchQueryCoverageUsesNativeExecutedModuleState(t *testing.T) {
	step := generatedPlanStepIdentity{
		ID: "module-pipeline",
		DiscoveryQueries: []generatedPlanQuery{
			{Language: "zh", Query: "POLQ 抑制剂 研发进展"},
			{Language: "en", Query: "POLQ inhibitor development status"},
		},
	}
	receipts := []sessionRunnerResearchSourceReceipt{{
		ToolCallID: "native-research", ToolName: "web_research", MaterialRole: "evidence",
		InvestigationIDs: []string{step.ID},
		// Transcript audit retains the provider's original request. The native
		// gateway deterministically executes the active module query set instead.
		Request: map[string]any{"operation": "search_and_fetch", "query": "provider ad-hoc wording"},
	}}
	covered := researchQueryLanguagesForStep(receipts, step, nil)
	if strings.Join(covered, ",") != "zh,en" {
		t.Fatalf("native executed module query state was lost behind provider audit input: %#v", covered)
	}
	beforePlan := receipts
	beforePlan[0].InvestigationIDs = nil
	if covered := researchQueryLanguagesForStep(beforePlan, step, []string{"native-research"}); len(covered) != 0 {
		t.Fatalf("unbound native discovery invented module query coverage: %#v", covered)
	}
}

func TestUpdateStepStatusHandsOffNewTranscriptSourcesOnce(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Investigate evidence",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Evidence", "steps": []any{map[string]any{
						"id": "step-1", "title": "Inspect source", "description": "Inspect source evidence.", "kind": "research",
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Source available"},
			},
			"_step_statuses": map[string]any{"step-1": map[string]any{
				"status": "in_progress", "title": "Inspect source", "description": "Inspect source evidence.", "kind": "research",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpoint("step-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-start-call",
		"toolInput": map[string]any{"step": "step-1", "status": "in_progress"}, "toolResult": map[string]any{"ok": true},
	})
	appendCheckpoint("source-result", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "source-call",
		"toolInput": map[string]any{"url": "https://example.test/primary"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"body": strings.Repeat("substantive primary evidence ", 80),
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	first, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "complete-step", map[string]any{
		"step": "step-1", "status": "completed", "notes": "Source inspected",
		"observations": []any{"The primary source supports the module."}, "follow_ups": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	transition := mapValue(mapValue(first)["research_transition"])
	receipts := anySliceValue(transition["new_source_receipts"])
	if len(receipts) != 1 || stringValue(mapValue(receipts[0])["tool_call_id"]) != "source-call" {
		t.Fatalf("new source was not handed off: %#v", transition)
	}
	bound := anySliceValue(mapValue(first)["source_receipts"])
	if len(bound) != 1 || stringValue(mapValue(bound[0])["tool_call_id"]) != "source-call" {
		t.Fatalf("successful source was not bound to its research module: %#v", first)
	}
	replayed, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "complete-step-again", map[string]any{
		"step": "step-1", "status": "completed", "notes": "Source inspected",
		"observations": []any{"The primary source supports the module."}, "follow_ups": []any{},
	})
	if err != nil || mapValue(replayed)["idempotent"] != true ||
		len(anySliceValue(mapValue(mapValue(replayed)["research_transition"])["new_source_receipts"])) != 0 {
		t.Fatalf("source handoff repeated after its durable watermark: %#v err=%v", replayed, err)
	}
}

func TestUpdateStepStatusRetiresCachedReceiptsRejectedByCurrentEvidencePolicy(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Investigate POLQ inhibitors",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Evidence", "steps": []any{map[string]any{
						"id": "step-1", "title": "POLQ evidence", "description": "Inspect POLQ inhibitor evidence.",
						"kind": "research", "research_question": "What supports POLQ inhibitor development?",
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Sources available"},
			},
			"_step_statuses": map[string]any{"step-1": map[string]any{
				"status": "in_progress", "title": "POLQ evidence", "description": "Inspect POLQ inhibitor evidence.",
				"source_receipts": []any{map[string]any{"event_id": 1, "tool_call_id": "stale-source", "material_role": "evidence"}},
				"query_languages": []any{"zh", "en"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, _, _, appendErr := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendCheckpoint("step-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-start-call",
		"toolInput":  map[string]any{"step": "step-1", "status": "in_progress"},
		"toolResult": map[string]any{"step": "step-1", "status": "in_progress"},
	})
	appendCheckpoint("off-topic-source", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "off-topic-call",
		"toolInput": map[string]any{"url": "https://example.test/unrelated", "prompt": "Fetch the POLQ inhibitor study"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"contentType": "text/plain", "body": strings.Repeat("paediatric oral dosage formulation and caregiver adherence ", 80),
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	result, err := fixture.server.executeAgentUpdateStepStatus(
		withTranscriptRunnerChatRun(context.Background(), run), fixture.stream.FrameID, "complete-step", map[string]any{
			"step": "step-1", "status": "completed", "notes": "Source inspected",
		},
	)
	continuation := mapValue(mapValue(result)["research_continuation"])
	if err != nil || mapValue(result)["status"] != "completed" || mapValue(result)["applied"] == false ||
		len(anySliceValue(mapValue(result)["source_receipts"])) != 0 ||
		len(stringValueSlice(mapValue(result)["query_languages"])) != 0 ||
		stringValue(continuation["reason"]) != "research_material_required" ||
		boolValue(continuation["required"], true) || boolValue(continuation["blocking"], true) ||
		!boolValue(continuation["quality_advisory"], false) {
		t.Fatalf("cached evidence survived authoritative requalification: result=%#v err=%v", result, err)
	}
}

func TestUpdateStepStatusRequalifiesEarlierCompletedResearchModule(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Investigate POLQ inhibitors",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Evidence", "steps": []any{
						map[string]any{
							"id": "step-1", "title": "POLQ evidence", "description": "Inspect POLQ inhibitor evidence.",
							"kind": "research", "research_question": "What supports POLQ inhibitor development?",
						},
						map[string]any{
							"id": "step-2", "title": "Synthesis", "description": "Synthesize qualified evidence.", "kind": "work",
						},
					},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Sources available"},
			},
			"_step_statuses": map[string]any{
				"step-1": map[string]any{
					"status": "completed", "title": "POLQ evidence", "description": "Inspect POLQ inhibitor evidence.",
					"source_receipts": []any{
						map[string]any{"event_id": 2, "tool_call_id": "qualified-call", "material_role": "evidence"},
						map[string]any{"event_id": 3, "tool_call_id": "off-topic-call", "material_role": "evidence"},
					},
				},
				"step-2": map[string]any{
					"status": "in_progress", "title": "Synthesis", "description": "Synthesize qualified evidence.",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, _, _, appendErr := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendCheckpoint("step-one-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-one-start-call",
		"toolInput":  map[string]any{"step": "step-1", "status": "in_progress"},
		"toolResult": map[string]any{"step": "step-1", "status": "in_progress"},
	})
	appendCheckpoint("qualified-source", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "qualified-call",
		"toolInput": map[string]any{"url": "https://example.test/polq", "prompt": "Fetch the POLQ inhibitor study"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"contentType": "text/plain", "body": strings.Repeat("POLQ inhibitor synthetic lethality tumor evidence ", 80),
		}},
	})
	appendCheckpoint("off-topic-source", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "off-topic-call",
		"toolInput": map[string]any{"url": "https://example.test/unrelated", "prompt": "Fetch the POLQ inhibitor study"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"contentType": "text/plain", "body": strings.Repeat("paediatric oral dosage formulation and caregiver adherence ", 80),
		}},
	})
	appendCheckpoint("step-one-complete", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-one-complete-call",
		"toolInput":  map[string]any{"step": "step-1", "status": "completed"},
		"toolResult": map[string]any{"step": "step-1", "status": "completed"},
	})
	appendCheckpoint("step-two-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "step-two-start-call",
		"toolInput":  map[string]any{"step": "step-2", "status": "in_progress"},
		"toolResult": map[string]any{"step": "step-2", "status": "in_progress"},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	if _, err := fixture.server.executeAgentUpdateStepStatus(
		withTranscriptRunnerChatRun(context.Background(), run), fixture.stream.FrameID, "continue-step-two", map[string]any{
			"step": "step-2", "status": "in_progress",
		},
	); err != nil {
		t.Fatal(err)
	}
	metadata, found, err := fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	stepOne := mapValue(mapValue(metadata.ContextData["_step_statuses"])["step-1"])
	receipts := anySliceValue(stepOne["source_receipts"])
	if stepOne["status"] != "completed" || len(receipts) != 1 ||
		stringValue(mapValue(receipts[0])["tool_call_id"]) != "qualified-call" {
		t.Fatalf("earlier completed module retained stale evidence: %#v", stepOne)
	}
}

func TestReconcileGeneratedPlanResearchEvidenceKeepsCompletedModuleAndAddsAdvisory(t *testing.T) {
	statuses := map[string]any{"step-1": map[string]any{
		"status": "completed", "source_receipts": []any{map[string]any{"tool_call_id": "stale-call"}},
	}}
	steps := []generatedPlanStepIdentity{{
		ID: "step-1", Kind: generatedPlanStepKindResearch, ResearchQuestion: "What supports POLQ inhibitor development?",
	}}
	if !reconcileGeneratedPlanResearchEvidence(statuses, steps, nil) {
		t.Fatal("authoritative empty ledger did not change stale research state")
	}
	state := mapValue(statuses["step-1"])
	continuation := mapValue(state["research_continuation"])
	if state["status"] != "completed" || len(anySliceValue(state["source_receipts"])) != 0 ||
		stringValue(continuation["reason"]) != "research_material_required" ||
		boolValue(continuation["required"], true) || boolValue(continuation["blocking"], true) ||
		!boolValue(continuation["quality_advisory"], false) {
		t.Fatalf("unsupported completed module was not preserved with an advisory: %#v", state)
	}
}

func TestReconcileGeneratedPlanResearchEvidencePreservesCompletedSourceAdvice(t *testing.T) {
	statuses := map[string]any{"step-1": map[string]any{
		"status": "completed", "source_refs": []any{"source-call"},
		"research_continuation": map[string]any{
			"reason": "research_follow_up_required", "required": true,
			"next_actions": []any{map[string]any{"action": "fetch", "url": "https://example.test/follow-up"}},
		},
	}}
	steps := []generatedPlanStepIdentity{{
		ID: "step-1", Kind: generatedPlanStepKindResearch, ResearchQuestion: "What supports the conclusion?",
	}}
	receipts := []sessionRunnerResearchSourceReceipt{{
		EventID: 3, ToolCallID: "source-call", ToolName: "web_fetch", MaterialRole: "evidence",
		InvestigationIDs: []string{"step-1"},
	}}
	if !reconcileGeneratedPlanResearchEvidence(statuses, steps, receipts) {
		t.Fatal("completed source advice was not reconciled")
	}
	state := mapValue(statuses["step-1"])
	continuation := mapValue(state["research_continuation"])
	if state["status"] != "completed" || stringValue(continuation["reason"]) != "research_follow_up_required" ||
		boolValue(continuation["required"], true) || boolValue(continuation["blocking"], true) ||
		!boolValue(continuation["quality_advisory"], false) {
		t.Fatalf("completed source advice was not preserved as advisory: %#v", state)
	}
}

func TestDeepResearchModuleCompletionRequiresBindingAvailableSource(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Research each module",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Modules", "steps": []any{map[string]any{
						"id": "step-1", "title": "Mechanism module", "description": "Research mechanism evidence.",
						"kind": "research", "output_module": "Mechanism", "research_question": "What supports the mechanism?",
						"research_depth": "deep", "discovery_queries": []any{
							map[string]any{"language": "zh", "query": "作用机制 证据"},
							map[string]any{"language": "en", "query": "mechanism evidence"},
						},
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Sources available"},
			},
			"_step_statuses": map[string]any{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpoint("source-before-plan", map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "verified-source",
		"toolInput": map[string]any{
			"operation": "search_and_fetch", "query": "作用机制 证据", "query_variants": []any{"mechanism evidence"},
		},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"quality":   map[string]any{"meetsTarget": true, "deepReadSources": 1},
			"documents": []any{map[string]any{"url": "https://example.test/record", "content": strings.Repeat("source evidence ", 100)}},
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if _, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "start-module", map[string]any{
		"step": "step-1", "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	continued, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "unsupported-module", map[string]any{
		"step": "step-1", "status": "completed",
		"observations": []any{"A claim that must be sourced."}, "follow_ups": []any{},
	})
	continuation := mapValue(mapValue(continued)["research_continuation"])
	if err != nil || mapValue(continued)["applied"] != false || mapValue(continued)["status"] != "in_progress" ||
		stringValue(mapValue(continued)["requested_status"]) != "completed" ||
		!boolValue(continuation["required"], false) || !boolValue(continuation["blocking"], false) ||
		boolValue(continuation["quality_advisory"], true) ||
		!strings.Contains(strings.Join(stringValueSlice(continuation["available_source_refs"]), " "), "verified-source") {
		t.Fatalf("deep module completion did not require the available source binding: result=%#v err=%v", continued, err)
	}
	completed, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "supported-module", map[string]any{
		"step": "step-1", "status": "completed",
		"source_refs": []any{"verified-source"}, "follow_ups": []any{},
	})
	if err != nil || mapValue(completed)["status"] != "completed" || len(anySliceValue(mapValue(completed)["source_receipts"])) != 1 ||
		len(stringValueSlice(mapValue(completed)["query_languages"])) != 2 ||
		!slices.Equal(stringValueSlice(mapValue(completed)["source_refs"]), []string{"https://example.test/record"}) {
		t.Fatalf("verified research module without optional observations did not complete: result=%#v err=%v", completed, err)
	}
}

func TestResearchModulePersistsAndExecutesSourceOwnedFollowUp(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, workspace.FrameRuntimeMetadata{
		FrameID: fixture.stream.FrameID,
		ContextData: map[string]any{
			"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
			"_plan_artifact_id": "plan", "_plan_version_id": "version",
			"_plan_json": map[string]any{
				"version": 3, "task_summary": "Research one module",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Module", "steps": []any{map[string]any{
						"id": "step-1", "title": "Evidence module", "description": "Research source evidence.",
						"kind": "research", "output_module": "Evidence", "research_question": "What supports the conclusion?",
						"research_depth": "deep",
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Sources available"},
			},
			"_step_statuses": map[string]any{"step-1": map[string]any{
				"status": "in_progress", "title": "Evidence module", "description": "Research source evidence.",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	appendCheckpoint := func(id string, payload map[string]any) {
		t.Helper()
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, _, _, appendErr := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendCheckpoint("module-start", map[string]any{
		"toolName": updateStepStatusToolName, "toolPhase": "completed", "toolCallId": "module-start",
		"toolInput":  map[string]any{"step": "step-1", "status": "in_progress"},
		"toolResult": map[string]any{"ok": true, "step": "step-1", "status": "in_progress"},
	})
	appendCheckpoint("initial-research", map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "initial-research",
		"toolInput": map[string]any{
			"operation": "search_and_fetch", "query": "initial evidence",
			"research_session": map[string]any{"mode": "start"},
		},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"documents": []any{map[string]any{
				"url": "https://example.test/initial", "content": strings.Repeat("initial evidence ", 100),
			}},
			"quality":           map[string]any{"deepReadSources": 1},
			"research_session":  map[string]any{"id": "wr-session", "mode": "start"},
			"retrievalDecision": map[string]any{"continueRecommended": true, "stopReason": "max_rounds_reached"},
			"nextActions":       []any{map[string]any{"action": "search_more", "query": "follow-up evidence"}},
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	continued, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "complete-before-follow-up", map[string]any{
		"step": "step-1", "status": "completed",
	})
	continuation := mapValue(mapValue(continued)["research_continuation"])
	if err != nil || stringValue(mapValue(continued)["status"]) != "completed" ||
		stringValue(continuation["reason"]) != "research_follow_up_required" ||
		boolValue(continuation["required"], true) || boolValue(continuation["blocking"], true) ||
		!boolValue(continuation["quality_advisory"], false) ||
		stringValue(mapValue(continuation["research_session"])["id"]) != "wr-session" {
		t.Fatalf("source-owned continuation was not retained as advice: result=%#v err=%v", continued, err)
	}

	appendCheckpoint("follow-up-research", map[string]any{
		"toolName": "web_search", "toolPhase": "completed", "toolCallId": "follow-up-research",
		"toolInput": map[string]any{
			"query": "follow-up evidence",
		},
		"toolResult": map[string]any{"ok": true, "results": []any{map[string]any{
			"url": "https://example.test/follow-up", "snippet": strings.Repeat("follow-up evidence ", 20),
		}}},
	})
	completed, err := fixture.server.executeAgentUpdateStepStatus(ctx, fixture.stream.FrameID, "complete-after-follow-up", map[string]any{
		"step": "step-1", "status": "completed",
	})
	if err != nil || stringValue(mapValue(completed)["status"]) != "completed" ||
		len(mapValue(mapValue(completed)["research_continuation"])) != 0 {
		t.Fatalf("executed source follow-up did not close the module: result=%#v err=%v", completed, err)
	}
}
