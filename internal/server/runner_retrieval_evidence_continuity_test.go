package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestCandidateReferencesKeepCSVURLInsideItsField(t *testing.T) {
	positive := map[string]struct{}{}
	negative := map[string]struct{}{}
	addSessionRunnerCandidateReferences(positive, negative,
		"title,url,source_type,relevance\nPaper,https://example.org/paper,publication,primary\n")
	want := "url:https://example.org/paper"
	if _, ok := positive[want]; !ok {
		t.Fatalf("references = %#v, missing %q", positive, want)
	}
	if _, bad := positive["url:https://example.org/paper,publication,primary"]; bad {
		t.Fatalf("CSV suffix leaked into URL reference: %#v", positive)
	}
}

func TestLargeWebResearchResultPersistsDeepReadEvidenceAcrossDescriptorCheckpoint(t *testing.T) {
	server := &Server{}
	run := &sessionRunnerChatRun{}
	fullResult := map[string]any{
		"ok": true,
		"result": map[string]any{
			"sources": []any{map[string]any{
				"status": "fetched",
				"url":    "https://example.org/full-paper",
				"readReceipt": map[string]any{
					"deepRead":              true,
					"responseComplete":      true,
					"truncated":             false,
					"partial":               false,
					"extractableCharacters": float64(webResearchMinimumSubstantiveCharacters + 100),
				},
			}},
		},
	}
	raw, err := json.Marshal(fullResult)
	if err != nil {
		t.Fatal(err)
	}
	server.bindTrustedScientificLargeToolResult(run, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "call-1", Name: "web_research", Arguments: json.RawMessage(`{}`)},
		RawJSON:  raw,
		Outcome:  agentruntime.ToolResultSucceeded,
	})
	if got := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(got, []string{
		trustedScientificValidatedWebResearchReadSignal, "source-host:example.org",
	}) {
		t.Fatalf("large-result signals = %#v", got)
	}

	details := map[string]any{
		"toolName":  "web_research",
		"toolInput": map[string]any{},
		"toolResult": map[string]any{
			"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
			"outcome":     "succeeded",
			"preview":     `{}`,
		},
	}
	server.bindTrustedScientificCompletion(run, details, nil)
	if got := stringArrayValue(details[trustedScientificReviewSignalsField]); !reflect.DeepEqual(got, []string{
		trustedScientificValidatedWebResearchReadSignal, "source-host:example.org",
	}) {
		t.Fatalf("checkpoint signals = %#v", got)
	}
}

func TestLargePartialWebResearchKeepsOnlyVerifiedDeepReadEvidence(t *testing.T) {
	server := &Server{}
	run := &sessionRunnerChatRun{}
	deepURL := "https://company.example/disclosures/phase-3"
	fullResult := map[string]any{
		"ok": true,
		"result": map[string]any{
			"failure": map[string]any{"kind": "research_partial", "recoverable": true},
			"sources": []any{
				map[string]any{
					"status": "fetched", "url": deepURL,
					"readReceipt": map[string]any{
						"deepRead": true, "responseComplete": true, "truncated": false,
						"partial": false, "extractableCharacters": float64(1800),
					},
				},
				map[string]any{"status": "sourceUnavailable", "url": "https://blocked.example/source"},
			},
			"documents": []any{map[string]any{
				"url": deepURL, "content": "substantive source",
				"readReceipt": map[string]any{"deepRead": true, "responseComplete": true},
			}},
		},
	}
	raw, err := json.Marshal(fullResult)
	if err != nil {
		t.Fatal(err)
	}
	if outcome := agentruntime.ClassifyToolResult(fullResult); outcome == agentruntime.ToolResultSucceeded {
		t.Fatalf("fixture must remain a partial result, got %s", outcome)
	}
	server.bindTrustedScientificLargeToolResult(run, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "partial-web", Name: "web_research", Arguments: json.RawMessage(`{}`)},
		RawJSON:  raw, Outcome: agentruntime.ClassifyToolResult(fullResult),
	})
	want := []string{
		"evidence-record:web:" + runnerEvidenceCanonicalWeb(deepURL),
	}
	if got := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("partial large-result signals=%#v want=%#v", got, want)
	}
	if !sessionRunnerHasAuthoritativeSourceEvidence(want) {
		t.Fatalf("exact deep-read web record was not authoritative source evidence: %#v", want)
	}
	details := map[string]any{
		"toolName": "web_research", "toolInput": map[string]any{},
		"toolResult": map[string]any{
			"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
			"outcome":     "partial", "preview": `{}`,
		},
	}
	server.bindTrustedScientificCompletion(run, details, nil)
	if got := stringArrayValue(details[trustedScientificReviewSignalsField]); !reflect.DeepEqual(got, want) {
		t.Fatalf("partial descriptor checkpoint signals=%#v want=%#v", got, want)
	}
}

func TestLargePatentResultPersistsExactRecordDepthAcrossDescriptorCheckpoint(t *testing.T) {
	server := &Server{}
	run := &sessionRunnerChatRun{}
	fullResult := map[string]any{"ok": true, "result": map[string]any{
		"evidenceDepth": "full_record",
		"records": []any{map[string]any{
			"publicationNumber": "CN117362283B", "recordDepth": "full_record", "focusRelevant": true,
			"abstract": "A substantive record", "claims": []any{"A compound claim"},
		}},
	}}
	raw, err := json.Marshal(fullResult)
	if err != nil {
		t.Fatal(err)
	}
	server.bindTrustedScientificLargeToolResult(run, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{
			ID: "patent-call", Name: "patent_search",
			Arguments: json.RawMessage(`{"operation":"lookup","publication_number":"CN117362283B"}`),
		},
		RawJSON: raw, Outcome: agentruntime.ToolResultSucceeded,
	})
	want := []string{"evidence-record:patent:cn117362283b"}
	if got := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("large patent signals=%#v want=%#v", got, want)
	}
	details := map[string]any{
		"toolName": "patent_search", "toolInput": map[string]any{},
		"toolResult": map[string]any{
			"artifact_id": "large-tool-result-0123456789abcdef0123456789abcdef",
			"outcome":     "succeeded", "preview": `{}`,
		},
	}
	server.bindTrustedScientificCompletion(run, details, nil)
	if got := stringArrayValue(details[trustedScientificReviewSignalsField]); !reflect.DeepEqual(got, want) {
		t.Fatalf("large patent checkpoint signals=%#v want=%#v", got, want)
	}
}

func TestEvidenceDepthIndexesPatentLookupSchemaFieldNames(t *testing.T) {
	messages := []agentruntime.Message{
		{
			Role: "assistant",
			ToolCalls: []agentruntime.ToolCall{{
				ID:               "patent-1",
				Name:             "patent_search",
				VerifiedEvidence: true,
				Arguments: json.RawMessage(`{
					"operation":"lookup",
					"publication_number":"CN117362283B",
					"focus_terms":["GLP-1","oral"]
				}`),
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "patent-1",
			Content: `{
				"ok":true,
				"result":{
					"evidenceDepth":"full_record",
					"records":[{
						"recordDepth":"full_record",
						"focusRelevant":true,
						"abstract":"A substantive patent abstract",
						"claims":["A compound claim"]
					}]
				}
			}`,
		},
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !index.contains("patent", "CN117362283B") {
		t.Fatalf("snake_case patent lookup was not indexed: %#v", index.patents)
	}
}
