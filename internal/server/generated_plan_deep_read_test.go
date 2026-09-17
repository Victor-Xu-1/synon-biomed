package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestDeepResearchStepKeepsDiscoverySeparateFromSourceReading(t *testing.T) {
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
				"version": 3, "task_summary": "Investigate a therapeutic mechanism",
				"phases": []any{map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
					"id": "track-1", "name": "Evidence", "steps": []any{map[string]any{
						"id": "step-1", "title": "Mechanism evidence", "description": "Read source evidence for the mechanism.",
						"kind": "research", "output_module": "Mechanism", "research_question": "What source evidence supports the mechanism?",
						"research_depth": "deep",
					}},
				}}}},
				"feasibility": map[string]any{"confidence": "high", "rationale": "Public sources exist"},
			},
			"_step_statuses": map[string]any{"step-1": map[string]any{
				"status": "in_progress", "title": "Mechanism evidence", "description": "Read source evidence for the mechanism.",
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
		"toolResult": map[string]any{"ok": true, "step": "step-1", "status": "in_progress"},
	})
	appendCheckpoint("discovery", map[string]any{
		"toolName": "web_search", "toolPhase": "completed", "toolCallId": "discovery-call",
		"toolInput": map[string]any{"query": "therapeutic mechanism primary evidence"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"sources": []any{map[string]any{
				"title": "Mechanism study", "url": "https://example.test/mechanism-study",
				"record": map[string]any{
					"record_depth": "abstract_record", "abstract_complete": true,
					"abstract":        strings.Repeat("Therapeutic mechanism primary evidence. ", 40),
					"citation_handle": "doi:10.1000/mechanism", "citation_text": "Mechanism study. DOI:10.1000/mechanism.",
				},
			}},
		}},
	})
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	discoveryOnly, err := fixture.server.executeAgentUpdateStepStatus(
		ctx, fixture.stream.FrameID, "complete-from-discovery", map[string]any{
			"step": "step-1", "status": "completed",
			"observations": []any{"The discovery record names a potentially relevant study."},
		},
	)
	continuation := mapValue(mapValue(discoveryOnly)["research_continuation"])
	targets := anySliceValue(continuation["available_read_targets"])
	if err != nil || stringValue(mapValue(discoveryOnly)["status"]) != "in_progress" ||
		stringValue(mapValue(discoveryOnly)["requested_status"]) != "completed" ||
		boolValue(mapValue(discoveryOnly)["applied"], true) ||
		stringValue(continuation["reason"]) != "research_source_read_required" ||
		!boolValue(continuation["required"], false) || !boolValue(continuation["blocking"], false) ||
		len(targets) != 1 || stringValue(mapValue(targets[0])["url"]) != "https://example.test/mechanism-study" {
		t.Fatalf("deep discovery-only completion=%#v err=%v", discoveryOnly, err)
	}

	metadata, found, err := fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	state := mapValue(mapValue(metadata.ContextData["_step_statuses"])["step-1"])
	if err != nil || !found || stringValue(state["status"]) != "in_progress" {
		t.Fatalf("discovery-only status was persisted as complete: state=%#v found=%t err=%v", state, found, err)
	}

	appendCheckpoint("source-read", map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "source-read-call",
		"toolInput": map[string]any{
			"url": "https://example.test/mechanism-study", "prompt": "What source evidence supports the mechanism?",
		},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"url": "https://example.test/mechanism-study", "contentType": "text/plain",
			"body": strings.Repeat("Therapeutic mechanism source methods results limitations evidence. ", 80),
		}},
	})
	completed, err := fixture.server.executeAgentUpdateStepStatus(
		ctx, fixture.stream.FrameID, "complete-after-source-read", map[string]any{
			"step": "step-1", "status": "completed",
			"observations": []any{"The source record was read and supports the bounded conclusion."},
		},
	)
	if err != nil || stringValue(mapValue(completed)["status"]) != "completed" ||
		mapValue(completed)["applied"] == false || len(anySliceValue(mapValue(completed)["source_receipts"])) != 1 {
		t.Fatalf("deep completion after source read=%#v err=%v", completed, err)
	}
}

func TestFocusedResearchMayKeepDiscoveryOnlyCompletionAdvisory(t *testing.T) {
	if generatedPlanResearchDepthRequiresSourceRead("focused") {
		t.Fatal("focused discovery was unexpectedly promoted to a mandatory deep-read contract")
	}
	for _, depth := range []string{"deep", "systematic", "DEEP"} {
		if !generatedPlanResearchDepthRequiresSourceRead(depth) {
			t.Fatalf("research depth %q did not require a source read", depth)
		}
	}
}

func TestArticleRecordMustMatchTheActiveInvestigationBeforeBecomingEvidence(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "article-read", Name: "fetch_article_fulltext",
		Arguments: json.RawMessage(`{"doi":"10.1000/unrelated"}`),
	}
	result := map[string]any{"ok": true, "result": map[string]any{
		"available": false, "recordAvailable": true, "status": "record_available",
		"title": "Cancer prevalence across vertebrates", "doi": "10.1000/unrelated",
		"abstractText": strings.Repeat("Comparative oncology survey of cancer prevalence across animal species. ", 30),
	}}
	focuses := []string{"MTAP PRMT5 inhibitor clinical pipeline and trial stage"}
	if researchSourceCallUsable(call, result, focuses) {
		t.Fatal("a real but off-topic article record became evidence for the active investigation")
	}

	relevant := map[string]any{"ok": true, "result": map[string]any{
		"available": false, "recordAvailable": true, "status": "record_available",
		"title": "MTA-cooperative PRMT5 inhibitors in MTAP-deleted tumors", "doi": "10.1000/relevant",
		"abstractText": strings.Repeat("MTAP-deleted cancer and MTA-cooperative PRMT5 inhibitor clinical pipeline evidence. ", 30),
	}}
	if !researchSourceCallUsable(call, relevant, focuses) {
		t.Fatal("a substantive on-topic article record was rejected")
	}
}
