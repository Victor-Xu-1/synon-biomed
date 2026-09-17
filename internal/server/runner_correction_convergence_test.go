package server

import (
	"encoding/json"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestRecordClassCorrectionRevalidatesAfterOneChangedArtifactPublication(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed " +
			"(unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=0 " +
			"invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=1 " +
			"invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): " +
			"cross-artifact consistency failures evidence_record_class_missing:source_type=patent " +
			"reason=requested_source_class_not_represented",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "edit-1", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"evidence.csv"}`),
	}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true,"changed":true}`}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-1", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["evidence.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{
		"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-2","unchanged":false}]}`}

	if !sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, editCall, editResult}) {
		t.Fatal("record-class correction released before the changed artifact was published")
	}
	if sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{
		boundary, editCall, editResult, saveCall, saveResult,
	}) {
		t.Fatal("changed publication did not return control to the immutable completion validator")
	}

	unchangedResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{
		"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-1","unchanged":true}]}`}
	if !sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{
		boundary, editCall, editResult, saveCall, unchangedResult,
	}) {
		t.Fatal("unchanged publication incorrectly discharged the correction")
	}
}

func TestSourceLocatorCorrectionReusesDurableTaskEvidenceThenRevalidates(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "cross-artifact consistency failures " +
			"evidence_source_locator_missing:evidence.csv row=2 source_type=evidence",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "edit-locator", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"evidence.csv"}`),
	}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-locator", Content: `{"ok":true,"changed":true}`}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-locator", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["evidence.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-locator", Content: `{
		"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-2","unchanged":false}]}`}
	messages := []agentruntime.Message{boundary, editCall, editResult, saveCall, saveResult}

	if sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("changed locator repair was forced to reread evidence already validated for the logical task")
	}
	if !sessionRunnerCorrectionReadyForRevalidation(run, messages) {
		t.Fatal("changed locator repair did not return to the immutable validator")
	}

	unchanged := append(append([]agentruntime.Message(nil), messages[:3]...),
		saveCall,
		agentruntime.Message{Role: "tool", ToolCallID: "save-locator", Content: `{
			"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-1","unchanged":true}]}`},
	)
	if !sessionRunnerCorrectionStillRequiresAction(run, unchanged) ||
		sessionRunnerCorrectionReadyForRevalidation(run, unchanged) {
		t.Fatal("unchanged locator publication incorrectly advanced correction state")
	}
}

func TestCorrectionGatewayDisablesToolsForImmediateRevalidation(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_source_locator_missing:evidence.csv row=2 source_type=evidence",
	}
	messages := []agentruntime.Message{
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "edit", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"evidence.csv"}`),
		}}},
		{Role: "tool", ToolCallID: "edit", Content: `{"ok":true,"changed":true}`},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "save", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["evidence.csv"]}`),
		}}},
		{Role: "tool", ToolCallID: "save", Content: `{
			"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-2","unchanged":false}]}`},
	}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	tools := []agentruntime.ToolSchema{{Name: "edit_file"}, {Name: "save_artifacts"}, {Name: "web_fetch"}}
	if choice := gateway.RequiredToolChoice(messages, tools); choice != "none" {
		t.Fatalf("converged correction tool choice=%#v, want none for revalidation", choice)
	}
}

func TestPriorGenericWebReadDoesNotExhaustANewSourceClassRepair(t *testing.T) {
	priorBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	webCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "paper-fetch", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://www.nature.com/articles/example"}`),
	}}}
	webResult := agentruntime.Message{Role: "tool", ToolCallID: "paper-fetch", Content: `{
		"ok":true,"result":{"statusCode":200,"body":"A substantive publication record."}}`}
	currentBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	messages := []agentruntime.Message{priorBoundary, webCall, webResult, currentBoundary}

	if runnerCorrectionPreviouslyExhaustedSourceTools(messages, "patent")["webfetch"] {
		t.Fatal("an unrelated prior web read exhausted the patent fallback in a new repair scope")
	}
	if runnerCorrectionPreviouslyExhaustedSourceTools(messages, "trial")["webfetch"] {
		t.Fatal("an unrelated prior web read exhausted the trial fallback in a new repair scope")
	}
}

func TestPatentCorrectionRetainsWebFallbackAfterUnrelatedEarlierReads(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=patent reason=requested_source_class_not_represented",
	}
	run.addExecutedSkillNames("deep-literature-investigation")
	priorBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	priorFetch := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "paper-fetch", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/article"}`),
	}}}
	priorFetchResult := agentruntime.Message{Role: "tool", ToolCallID: "paper-fetch", Content: `{
		"ok":true,"result":{"statusCode":200,"body":"An unrelated article."}}`}
	currentBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	lookup := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-lookup", Name: "patent_search", Arguments: json.RawMessage(`{
			"operation":"lookup","publication_number":"CA3160518C"}`),
	}}}
	lookupResult := agentruntime.Message{Role: "tool", ToolCallID: "patent-lookup", Content: `{
		"ok":true,"result":{"evidenceDepth":"locator_only","records":[{
			"publicationNumber":"CA3160518C","recordDepth":"locator",
			"url":"https://patents.google.com/patent/CA3160518C/en"}]}}`}
	tools := []agentruntime.ToolSchema{
		{Name: "patent_search"}, {Name: "web_fetch"}, {Name: "web_research"},
		{Name: "web_search"}, {Name: "edit_file"}, {Name: "save_artifacts"},
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{
		priorBoundary, priorFetch, priorFetchResult, currentBoundary, lookup, lookupResult,
	}, tools).(map[string]any)
	if choice["name"] != "web_fetch" {
		t.Fatalf("patent correction lost its exact-record web fallback: %#v", choice)
	}
}
