package server

import (
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestCorrectionToolChoicePersistsUntilSuccessfulExecutedAction(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "research and calculate",
		CorrectionReason: sessionRunnerRealScientificEvidenceRequiredReasonCode,
		CorrectionDetail: "the task has successful governed runtime execution evidence but no durable authoritative source evidence",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker + ". Obtain source authority."}
	preflight := agentruntime.Message{Role: "tool", ToolCallID: "ask-1", Content: `{"ok":false,"executed":false,"status":"agent_owned_decision"}`}
	decision := agentruntime.Message{Role: "tool", ToolCallID: "choice-1", Content: `{"ok":true,"executed":false,"decision_required":true,"status":"implementation_selection_required"}`}
	failed := agentruntime.Message{Role: "tool", ToolCallID: "fetch-1", Content: `{"ok":false,"error":"network failure"}`}
	succeeded := agentruntime.Message{Role: "tool", ToolCallID: "fetch-2", Content: `{"ok":true,"status":"completed","body":"authoritative content"}`}

	for name, messages := range map[string][]agentruntime.Message{
		"no action":        {boundary},
		"preflight only":   {boundary, preflight},
		"decision only":    {boundary, decision},
		"failed action":    {boundary, failed},
		"unrelated before": {{Role: "tool", Content: `{"ok":true}`}, boundary},
	} {
		t.Run(name, func(t *testing.T) {
			if !sessionRunnerCorrectionStillRequiresAction(run, messages) {
				t.Fatalf("correction action requirement was released by %#v", messages)
			}
		})
	}
	if sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, preflight, succeeded}) {
		t.Fatal("successful executed action did not release the correction tool requirement")
	}
}

func TestCorrectionGatewayRequiresAnyToolWithoutNamingOne(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "research and calculate",
		CorrectionReason: sessionRunnerRealScientificEvidenceRequiredReasonCode,
		CorrectionDetail: "no durable authoritative source evidence",
	}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	choice := gateway.RequiredToolChoice(
		[]agentruntime.Message{{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}},
		[]agentruntime.ToolSchema{{Name: "web_fetch"}, {Name: "download_public_scientific_file"}},
	)
	if choice != "required" {
		t.Fatalf("correction tool choice=%#v, want provider-neutral required", choice)
	}
}

func TestAgentOwnedArtifactCorrectionDoesNotAdvertiseAskUser(t *testing.T) {
	tools := []agentruntime.ToolSchema{
		{Name: "ask_user"}, {Name: "read_file"}, {Name: "edit_file"}, {Name: "save_artifacts"},
	}
	detail := "runner completion reference integrity failed (cross_artifact_failures=1): " +
		"machine_validation_missing_passing_check:artifact=validation.json"
	filtered := sessionRunnerCorrectionToolSchemas(tools, "artifact_reference_correction_required", detail)
	if agentRuntimeToolSchemaNamed(filtered, "ask_user") {
		t.Fatal("agent-owned artifact repair still advertised ask_user")
	}
	for _, required := range []string{"read_file", "edit_file", "save_artifacts"} {
		if !agentRuntimeToolSchemaNamed(filtered, required) {
			t.Fatalf("agent-owned artifact repair removed %q", required)
		}
	}

	userOwned := sessionRunnerCorrectionToolSchemas(
		tools, "artifact_reference_correction_required",
		"runner completion reference integrity failed (missing_required_deliverables=1)",
	)
	if !agentRuntimeToolSchemaNamed(userOwned, "ask_user") {
		t.Fatal("unclassified correction removed ask_user")
	}
}

func TestAgentOwnedArtifactCorrectionRequiresSuccessfulSaveReceipt(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a report",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=1): " +
			"machine_validation_missing_passing_check:artifact=validation.json",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	readCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "read-1", Name: "read_file"}}}
	readResult := agentruntime.Message{Role: "tool", ToolCallID: "read-1", Content: `{"ok":true,"content":"draft"}`}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "edit-1", Name: "edit_file"}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true,"status":"completed"}`}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "save-1", Name: "save_artifacts"}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{"ok":true,"artifacts":[{"filename":"report.md"}]}`}

	if !sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, readCall, readResult, editCall, editResult}) {
		t.Fatal("artifact correction was released before the changed artifact was saved")
	}
	if sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, readCall, readResult, editCall, editResult, saveCall, saveResult}) {
		t.Fatal("successful save receipt did not release artifact correction")
	}
}

func TestAgentOwnedArtifactCorrectionUsesBoundedRepairTransitions(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a report",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=1): " +
			"machine_validation_missing_passing_check:artifact=validation.json",
	}
	tools := []agentruntime.ToolSchema{{Name: "read_file"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	readCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "read-1", Name: "read_file"}}}
	readResult := agentruntime.Message{Role: "tool", ToolCallID: "read-1", Content: `{"ok":true}`}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "edit-1", Name: "edit_file"}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true}`}

	assertNamed := func(messages []agentruntime.Message, want string) {
		t.Helper()
		got, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
		if got["type"] != "tool" || got["name"] != want {
			t.Fatalf("choice=%#v, want named %s", got, want)
		}
	}
	assertNamed([]agentruntime.Message{boundary}, "read_file")
	assertNamed([]agentruntime.Message{boundary, readCall, readResult}, "edit_file")
	assertNamed([]agentruntime.Message{boundary, readCall, readResult, editCall, editResult}, "save_artifacts")
	noChange := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true,"changed":false}`}
	assertNamed([]agentruntime.Message{boundary, readCall, readResult, editCall, noChange}, "save_artifacts")
}

func TestSourceLocatorCorrectionRequiresEvidenceBeforeAChangedSave(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=2): " +
			"evidence_source_locator_missing:evidence.csv row=4 source_type=patent",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := func(id, name string) agentruntime.Message {
		return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: id, Name: name}}}
	}
	result := func(id string) agentruntime.Message {
		return agentruntime.Message{Role: "tool", ToolCallID: id, Content: `{"ok":true,"records":[{"id":"source-1"}]}`}
	}
	deepResult := func(id string) agentruntime.Message {
		return agentruntime.Message{Role: "tool", ToolCallID: id, Content: `{
			"ok":true,"result":{"quality":{"meetsTarget":true,"deepReadSources":1},
			"documents":[{"url":"https://example.test/record","content":"substantive primary record"}]}}`}
	}
	read := []agentruntime.Message{boundary, call("read-1", "read_file"), result("read-1"), call("save-1", "save_artifacts"), result("save-1")}
	if !sessionRunnerCorrectionStillRequiresAction(run, read) {
		t.Fatal("read/save loop satisfied a missing source-locator correction")
	}
	discoveryOnly := append(read, call("search-1", "web_search"), result("search-1"), call("save-2", "save_artifacts"), result("save-2"))
	if !sessionRunnerCorrectionStillRequiresAction(run, discoveryOnly) {
		t.Fatal("discovery-only search satisfied an authoritative source-locator correction")
	}
	sourceThenSave := []agentruntime.Message{boundary, call("source-1", "web_research"), deepResult("source-1"), call("save-3", "save_artifacts"), result("save-3")}
	if !sessionRunnerCorrectionStillRequiresAction(run, sourceThenSave) {
		t.Fatal("source read followed by an unchanged workspace publish bypassed the required edit")
	}
	saveThenSource := []agentruntime.Message{boundary, call("save-4", "save_artifacts"), result("save-4"), call("source-2", "repl"), result("source-2")}
	if !sessionRunnerCorrectionStillRequiresAction(run, saveThenSource) {
		t.Fatal("save before source incorrectly satisfied the correction action gate")
	}
	tools := []agentruntime.ToolSchema{{Name: "ask_user"}, {Name: "repl"}, {Name: "web_research"}, {Name: "save_artifacts"}}
	filtered := sessionRunnerCorrectionToolSchemas(tools, run.CorrectionReason, run.CorrectionDetail)
	if agentRuntimeToolSchemaNamed(filtered, "ask_user") || !agentRuntimeToolSchemaNamed(filtered, "repl") || !agentRuntimeToolSchemaNamed(filtered, "web_research") {
		t.Fatalf("source-locator correction tools=%#v", filtered)
	}
	if choice := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, filtered); choice != nil {
		t.Fatalf("source correction prescribed a competing fixed tool route=%#v", choice)
	}
	postSourceTools := append(filtered, agentruntime.ToolSchema{Name: "edit_file"})
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, sourceThenSave[:3], postSourceTools).(map[string]any); choice["name"] != "edit_file" {
		t.Fatalf("post-source correction did not advance to the artifact edit transition: %#v", choice)
	}
	postSourceEdit := []agentruntime.Message{
		boundary,
		call("source-4", "web_research"), deepResult("source-4"),
		call("edit-4", "edit_file"), {Role: "tool", ToolCallID: "edit-4", Content: `{"ok":true,"changed":true}`},
	}
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, postSourceEdit, postSourceTools).(map[string]any); choice["name"] != "save_artifacts" {
		t.Fatalf("post-edit correction did not advance to publication: %#v", choice)
	}
	postSourceSaved := append(postSourceEdit, call("save-5", "save_artifacts"), result("save-5"))
	if choice := sessionRunnerCorrectionRequiredToolChoice(run, postSourceSaved, postSourceTools); choice != nil {
		t.Fatalf("source-backed saved correction retained forced choice=%#v", choice)
	}

	plainREPL := []agentruntime.Message{
		boundary,
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "repl-plain", Name: "repl", Arguments: json.RawMessage(`{"code":"print('format only')"}`)}}},
		result("repl-plain"),
	}
	if runnerCorrectionIsSuccessfulMCPRepl(plainREPL[1].ToolCalls[0], map[string]any{"ok": true}) {
		t.Fatal("ordinary REPL execution was mistaken for source evidence")
	}
	discoveryMCP := []agentruntime.Message{
		boundary,
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "repl-mcp", Name: "repl", Arguments: json.RawMessage(`{"code":"records = host.mcp.search(query='oral peptide')"}`)}}},
		result("repl-mcp"),
	}
	if runnerCorrectionIsSuccessfulMCPRepl(discoveryMCP[1].ToolCalls[0], map[string]any{"ok": true}) {
		t.Fatal("MCP discovery candidates were mistaken for a record read")
	}
	recordMCP := agentruntime.ToolCall{ID: "repl-record", Name: "repl", Arguments: json.RawMessage(
		`{"code":"record = host.mcp('clinical-trials','get_trial_details',{'nct_id':'NCT00000001'}); print(record.evidence)"}`,
	)}
	if !runnerCorrectionIsSuccessfulMCPRepl(recordMCP, map[string]any{"ok": true}) {
		t.Fatal("MCP record read with inspected evidence depth was not recognized")
	}
}

func TestSourceLocatorCorrectionContextPreventsFormattingOnlyLoop(t *testing.T) {
	detail := "runner completion reference integrity failed (cross_artifact_failures=1): " +
		"evidence_source_locator_missing:evidence.csv row=4 source_type=patent"
	context := recoveredRunnerCorrectionContext(runnerCorrectionEntriesForClassification(
		"artifact_reference_correction_required", detail,
	))
	for _, required := range []string{
		"synon.runner_recovery.v1",
		`"required_transition":"repair_current_candidate"`,
		`"choose_from_advertised_capabilities":true`,
		"evidence_source_locator_missing",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("source-locator correction context missing %q: %s", required, context)
		}
	}
}

func TestSourceCorrectionSelectsExactReaderFromCapabilitiesNotToolIdentity(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{
		{Name: "arbitrary_broad_route", Capabilities: []string{"source-investigation", "evidence-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "arbitrary_exact_reader", Capabilities: []string{"source-class:publication", "source-record-read", "evidence-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "arbitrary_locator_reader", Capabilities: []string{"source-locator-read", "evidence-read"}, Exposure: agentruntime.ToolExposureDirect},
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary}, tools,
	).(map[string]any)
	if choice["name"] != "arbitrary_exact_reader" {
		t.Fatalf("source correction ignored the exact capability contract: %#v", choice)
	}
}

func TestSourceCorrectionContinuesReturnedURLByCapability(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=dataset",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "source-route", Name: "arbitrary_source_route",
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "source-route", Content: `{
		"ok":true,"record":{"source_url":"https://example.test/dataset/record"}}`}
	tools := []agentruntime.ToolSchema{
		{Name: "arbitrary_broad_route", Capabilities: []string{"source-investigation", "evidence-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "arbitrary_locator_reader", Capabilities: []string{"source-locator-read", "evidence-read"}, Exposure: agentruntime.ToolExposureDirect},
		{Name: "arbitrary_connector", Capabilities: []string{"mcp-bridge"}, Exposure: agentruntime.ToolExposureDirect},
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, call, result}, tools,
	).(map[string]any)
	if choice["name"] != "arbitrary_locator_reader" {
		t.Fatalf("returned URL did not continue through its declared reader capability: %#v", choice)
	}
}

func TestRecordDepthCorrectionSelectsOnlyTheMissingSourceClassThenMutation(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=3): " +
			"evidence_record_class_missing:source_type=publication, " +
			"evidence_record_class_missing:source_type=trial, " +
			"evidence_record_depth_missing:sources.csv row=3 source_type=patent identifier=CN117362283B",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	patentCall := runnerEvidenceDepthCall("patent-1", "patent_search", map[string]any{
		"operation": "lookup", "publication_number": "CN117362283B",
	})
	patentResult := runnerEvidenceDepthResult("patent-1", map[string]any{
		"evidenceDepth": "full_record", "records": []any{map[string]any{
			"publicationNumber": "CN117362283B", "recordDepth": "full_record", "focusRelevant": true,
		}},
	})
	publicationCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/example",
	})
	publicationResult := runnerEvidenceDepthResult("paper-1", map[string]any{
		"available": true, "fulltext": "substantive methods and results",
	})
	tools := []agentruntime.ToolSchema{
		{Name: "patent_search"}, {Name: "fetch_article_fulltext"}, {Name: "repl"},
		{Name: "edit_file"}, {Name: "save_artifacts"},
	}
	messages := []agentruntime.Message{patentCall, patentResult, publicationCall, publicationResult, boundary}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("missing trial choice=%#v", choice)
	}

	trialCall := runnerEvidenceDepthCall("trial-1", "mcp__clinical-trials__get_trial_details", map[string]any{
		"nct_id": "NCT04616014",
	})
	trialResult := runnerEvidenceDepthResult("trial-1", map[string]any{
		"found": true, "trial": map[string]any{"primary_outcomes": []any{"Exposure"}},
	})
	messages = append(messages, trialCall, trialResult)
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("post-evidence choice=%#v", choice)
	}
	if !sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("record-depth repair released before the evidence table edit")
	}

	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "edit-1", Name: "edit_file"}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true,"changed":true}`}
	messages = append(messages, editCall, editResult)
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "save_artifacts" {
		t.Fatalf("post-edit choice=%#v", choice)
	}

	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "save-1", Name: "save_artifacts"}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{"ok":true,"artifacts":[{"version_id":"v2"}]}`}
	messages = append(messages, saveCall, saveResult)
	if sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("record-depth repair remained forced after complete source/edit/save transition")
	}
}

func TestRecordDepthCorrectionLoadsConnectorSkillAfterMCPSchemaPreflight(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	replCall := runnerEvidenceDepthCall("paper-mcp", "repl", map[string]any{
		"code": `record = host.mcp("literature", "openalex_get_work", {"doi":"10.1000/example"})`,
	})
	preflight := runnerEvidenceDepthResult("paper-mcp", map[string]any{
		"ok": false, "status": "mcp_schema_preflight_required", "executed": false,
		"recovery": "Use only the exact live input keys.",
	})
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, replCall, preflight}, tools,
	).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("post-schema-preflight choice=%#v", choice)
	}

	skillCall := runnerEvidenceDepthCall("skill-1", "skill", map[string]any{"name": "mcp-literature", "filter": "openalex_get_work"})
	skillResult := runnerEvidenceDepthResult("skill-1", map[string]any{"ok": true, "name": "mcp-literature"})
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, replCall, preflight, skillCall, skillResult}, tools,
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("post-skill choice=%#v", choice)
	}
}

func TestRecordDepthCorrectionLoadsConnectorSkillBeforeFirstMCPRepairCall(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=trial",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("missing record did not load its connector schema before REPL: %#v", choice)
	}

	skillCall := runnerEvidenceDepthCall("skill-1", "skill", map[string]any{
		"name": "mcp-clinical-trials", "filter": "get_trial_details",
	})
	skillResult := runnerEvidenceDepthResult("skill-1", map[string]any{"ok": true, "name": "mcp-clinical-trials"})
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, skillCall, skillResult}, tools,
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("successful connector Skill did not advance to REPL: %#v", choice)
	}
}

func TestRecordDepthCorrectionAdvancesPastMissingSkillAcrossCorrectionBoundary(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_required:trial source_type=trial identifier=NCT04991480 reason=at_least_one_substantive_record_read",
	}
	firstBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	skillCall := runnerEvidenceDepthCall("skill-missing", "skill", map[string]any{
		"skill": "clinical-trials",
	})
	skillMissing := runnerEvidenceDepthResult("skill-missing", map[string]any{
		"ok": false, "success": false, "executed": false, "status": "skill_not_found",
		"recovery": "Use search_skills, or continue through an already advertised live connector.",
	})
	secondBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{firstBoundary, skillCall, skillMissing, secondBoundary}, tools,
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("missing connector Skill was retried instead of advancing to the live MCP transport: %#v", choice)
	}
}

func TestRecordDepthCorrectionDoesNotReloadDurableSkillAfterCompaction(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	run.addExecutedSkillNames("literature-review", "mcp-literature")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("durably loaded Skill was repeated after compaction instead of advancing to source retrieval: %#v", choice)
	}
}

func TestRecordDepthCorrectionDoesNotMistakeUnrelatedSkillForSourceConnector(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	run.addExecutedSkillNames("unrelated-analysis-workflow")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run,
		[]agentruntime.Message{boundary},
		[]agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}, {Name: "web_fetch"}},
	).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("unrelated Skill suppressed source-connector activation: %#v", choice)
	}
}

func TestRecordDepthCorrectionFallsBackFromUnavailableFullTextToStructuredMCPRecord(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fulltextCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/unavailable",
	})
	fulltextUnavailable := runnerEvidenceDepthResult("paper-1", map[string]any{
		"available": false, "status": "not_available", "doi": "10.1000/unavailable",
	})
	tools := []agentruntime.ToolSchema{
		{Name: "fetch_article_fulltext"}, {Name: "repl"}, {Name: "web_fetch"}, {Name: "web_research"},
	}

	initial, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if initial["name"] != "fetch_article_fulltext" {
		t.Fatalf("initial publication route=%#v", initial)
	}
	fallback, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, fulltextCall, fulltextUnavailable}, tools,
	).(map[string]any)
	if fallback["name"] != "repl" {
		t.Fatalf("unavailable full text did not advance to the structured MCP record route: %#v", fallback)
	}

	mcpCall := runnerEvidenceDepthCall("paper-mcp", "repl", map[string]any{
		"code": `record = host.mcp("literature", "openalex_get_work", {"work_id":"https://doi.org/10.1000/unavailable"})`,
	})
	mcpResult := runnerEvidenceDepthResult("paper-mcp", map[string]any{
		"ok": true, "exit_status": "ok", "stdout": "structured metadata without an abstract",
	})
	webFallback, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{boundary, fulltextCall, fulltextUnavailable, mcpCall, mcpResult}, tools,
	).(map[string]any)
	if webFallback["name"] != "web_fetch" {
		t.Fatalf("metadata-only MCP record did not advance to the publication landing page: %#v", webFallback)
	}

	persisted := &sessionRunnerChatRun{
		CorrectionReason: run.CorrectionReason, CorrectionDetail: run.CorrectionDetail,
	}
	persisted.addTrustedScientificReviewSignals(
		trustedScientificEvidenceRouteExhaustedSignalPrefix + "publication:repl",
	)
	persistedFallback, _ := sessionRunnerCorrectionRequiredToolChoice(
		persisted, []agentruntime.Message{boundary, fulltextCall, fulltextUnavailable}, tools,
	).(map[string]any)
	if persistedFallback["name"] != "web_fetch" {
		t.Fatalf("persisted MCP exhaustion was lost across execution units: %#v", persistedFallback)
	}
}

func TestRecordDepthCorrectionLeavesRejectedREPLForWebRoute(t *testing.T) {
	for _, status := range []string{
		"mcp_schema_preflight_required", "execution_path_exhausted", "inspection_required",
	} {
		t.Run(status, func(t *testing.T) {
			run := &sessionRunnerChatRun{
				CorrectionReason: "artifact_reference_correction_required",
				CorrectionDetail: "evidence_record_class_missing:source_type=publication",
			}
			run.addExecutedSkillNames("synon-research", "mcp-synon-research")
			boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
			fulltextCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{
				"pmcid": "PMC10102842",
			})
			fulltextUnavailable := runnerEvidenceDepthResult("paper-1", map[string]any{
				"available": false, "status": "not_available", "pmcid": "PMC10102842",
			})
			replCall := runnerEvidenceDepthCall("repl-1", "repl", map[string]any{
				"code": `record = host.mcp("wrong", "method", {})`,
			})
			replRejected := runnerEvidenceDepthResult("repl-1", map[string]any{
				"ok": false, "executed": false, "preflight": true, "status": status,
				"message": "the current contract rejected this source call before execution",
			})
			tools := []agentruntime.ToolSchema{
				{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"},
				{Name: "web_fetch"}, {Name: "web_research"},
			}
			choice, _ := sessionRunnerCorrectionRequiredToolChoice(
				run,
				[]agentruntime.Message{boundary, fulltextCall, fulltextUnavailable, replCall, replRejected},
				tools,
			).(map[string]any)
			if choice["name"] != "web_fetch" {
				t.Fatalf("preflight-rejected REPL route was repeated instead of changing transport: %#v", choice)
			}
		})
	}
}

func TestRecordDepthCorrectionReopensREPLOnlyAfterSkillContractRevision(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fulltextCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{"pmcid": "PMC1"})
	fulltextUnavailable := runnerEvidenceDepthResult("paper-1", map[string]any{"available": false, "status": "not_available"})
	replCall := runnerEvidenceDepthCall("repl-1", "repl", map[string]any{"code": `print("invalid route")`})
	replRejected := runnerEvidenceDepthResult("repl-1", map[string]any{
		"ok": false, "executed": false, "status": "mcp_schema_preflight_required",
		"message": "load the connector contract",
	})
	skillCall := runnerEvidenceDepthCall("skill-1", "skill", map[string]any{"skill": "mcp-publication"})
	skillLoaded := runnerEvidenceDepthResult("skill-1", map[string]any{"ok": true, "status": "completed"})
	tools := []agentruntime.ToolSchema{
		{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}, {Name: "web_fetch"},
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run,
		[]agentruntime.Message{
			boundary, fulltextCall, fulltextUnavailable, replCall, replRejected, skillCall, skillLoaded,
		},
		tools,
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("a materialized connector contract did not reopen one REPL route: %#v", choice)
	}
}

func TestRecordDepthCorrectionLeavesFailedREPLForWebRoute(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	run.addExecutedSkillNames("mcp-source-broker")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fulltextCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{"pmcid": "PMC1"})
	fulltextUnavailable := runnerEvidenceDepthResult("paper-1", map[string]any{"available": false, "status": "not_available"})
	replCall := runnerEvidenceDepthCall("repl-1", "repl", map[string]any{
		"code": `record = host.mcp("source-broker", "read_source", {"source_id":"missing","operation":"lookup","input":{}})`,
	})
	replFailed := agentruntime.Message{
		Role: "tool", ToolCallID: "repl-1",
		Content: `{"ok":false,"code":"source_not_found","error":"the selected source is unavailable"}`,
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run,
		[]agentruntime.Message{boundary, fulltextCall, fulltextUnavailable, replCall, replFailed},
		[]agentruntime.ToolSchema{
			{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}, {Name: "web_fetch"},
		},
	).(map[string]any)
	if choice["name"] != "web_fetch" {
		t.Fatalf("failed connector transport was repeated instead of changing route: %#v", choice)
	}
}

func TestRecordDepthCorrectionConsumesExactPublicationFetchAcrossRecoveryBoundary(t *testing.T) {
	const sourceURL = "https://cjournal.hep.com.cn/CN/10.20053/j.issn1001-5094.20250057"
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_missing:sources.csv row=2 source_type=publication " +
			"declared_source_type=identifier identifier=doi:10.20053/j.issn1001-5094.20250057",
	}
	firstBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fetchCall := runnerEvidenceDepthCall("localized-publication", "web_fetch", map[string]any{"url": sourceURL})
	fetchResult := runnerEvidenceDepthResult("localized-publication", map[string]any{
		"url": sourceURL, "statusCode": 200,
		"body": strings.Repeat("本研究系统考察了实验方法、观察结果与主要结论，并报告了可复核的数据。", 80),
	})
	secondBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{firstBoundary, fetchCall, fetchResult, secondBoundary},
		[]agentruntime.ToolSchema{{Name: "web_fetch"}, {Name: "edit_file"}, {Name: "save_artifacts"}},
	).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("completed exact publication fetch was repeated after recovery: %#v", choice)
	}
}

func TestRecordDepthCorrectionKeepsMCPTransportAvailableForPostDiscoveryDetail(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=publication",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fulltextCall := runnerEvidenceDepthCall("paper-1", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/unavailable",
	})
	fulltextUnavailable := runnerEvidenceDepthResult("paper-1", map[string]any{
		"available": false, "status": "not_available", "doi": "10.1000/unavailable",
	})
	mcpDiscovery := runnerEvidenceDepthCall("paper-mcp", "repl", map[string]any{
		"code": `result = host.mcp("literature", "openalex_search_works", {"query":"oral small molecule", "include_abstracts":true})`,
	})
	mcpResult := runnerEvidenceDepthResult("paper-mcp", map[string]any{
		"ok": true, "exit_status": "ok", "stdout": "Top-level keys: records",
	})
	tools := []agentruntime.ToolSchema{
		{Name: "fetch_article_fulltext"}, {Name: "repl"}, {Name: "web_fetch"}, {Name: "web_research"},
	}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run,
		[]agentruntime.Message{boundary, fulltextCall, fulltextUnavailable, mcpDiscovery, mcpResult},
		tools,
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("MCP discovery incorrectly exhausted the transport before an exact detail read: %#v", choice)
	}
}

func TestFailedArtifactSaveForcesMissingEvidenceSourceBeforeAnotherEditOrSave(t *testing.T) {
	run := &sessionRunnerChatRun{}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-1", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{
		"ok":false,
		"code":"artifact_save_requires_correction",
		"errors":[{"code":"unsupported_evidence_references","unsupported_evidence_references":["nct:NCT04616014"]}]
	}`}
	tools := []agentruntime.ToolSchema{{Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{saveCall, saveResult}, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("unsupported trial reference did not force a source read: %#v", choice)
	}
	trialCall := runnerEvidenceDepthCall("trial-after-warning", "repl", map[string]any{
		"code": `trial = host.mcp("clinical-trials", "get_trial_details", {"nct_id":"NCT04616014"})\nprint(trial.evidence)`,
	})
	trialResult := runnerEvidenceDepthResult("trial-after-warning", map[string]any{
		"ok": true, "exit_status": "ok", "stdout": "structured_record_read",
	})
	if resolved := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{saveCall, saveResult, trialCall, trialResult}, tools,
	); resolved != nil {
		t.Fatalf("resolved trial reference still forced a source tool: %#v", resolved)
	}
}

func TestDeferredDraftWarningForUnsupportedWebURLForcesWebFetchBeforeResave(t *testing.T) {
	const sourceURL = "https://www.fda.gov/drugs/example"
	run := &sessionRunnerChatRun{}
	// A prior web receipt from the same logical task was already considered by
	// save_artifacts and must not cancel this exact post-save warning.
	run.addTrustedScientificReviewSignals("evidence-record:web:" + runnerEvidenceCanonicalWeb(sourceURL))
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-web", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["sources.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-web", Content: `{
		"artifacts":[{"filename":"sources.csv"}],
		"completion_pending":true,
		"warnings":[{"code":"unsupported_evidence_references",
		"unsupported_evidence_references":["url:` + sourceURL + `"]}]
	}`}
	tools := []agentruntime.ToolSchema{{Name: "web_fetch"}, {Name: "edit_file"}, {Name: "save_artifacts"}}

	classes := runnerPendingArtifactSaveEvidenceClasses(run, []agentruntime.Message{saveCall, saveResult})
	if len(classes) != 1 || classes[0] != "web" {
		t.Fatalf("deferred draft warning did not preserve the missing web class: %#v", classes)
	}
	class, identifier := runnerEvidenceReferenceRequirement("url:" + sourceURL)
	if class != "web" || identifier != runnerEvidenceCanonicalWeb(sourceURL) {
		t.Fatalf("web evidence reference was not canonicalized: class=%q identifier=%q", class, identifier)
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{saveCall, saveResult}, tools,
	).(map[string]any)
	if choice["name"] != "web_fetch" {
		t.Fatalf("unsupported web draft reference did not require a deep fetch before resave: %#v", choice)
	}
}

func TestDeferredDraftWarningAdvancesAfterFullTextSourceIsUnavailable(t *testing.T) {
	run := &sessionRunnerChatRun{}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-publication", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["sources.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-publication", Content: `{
		"completion_pending":true,
		"warnings":[{"code":"unsupported_evidence_references",
		"unsupported_evidence_references":["doi:10.1161/circ.152.suppl_3.4364312"]}]
	}`}
	fulltextCall := runnerEvidenceDepthCall("fulltext-unavailable", "fetch_article_fulltext", map[string]any{
		"doi": "10.1161/circ.152.suppl_3.4364312",
	})
	fulltextResult := runnerEvidenceDepthResult("fulltext-unavailable", map[string]any{
		"ok": true,
		"result": map[string]any{
			"available": false, "recordAvailable": false, "sourceUnavailable": true,
			"status": "source_unavailable", "statusCode": 503,
		},
	})
	tools := []agentruntime.ToolSchema{{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(
		run, []agentruntime.Message{saveCall, saveResult, fulltextCall, fulltextResult}, tools,
	).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("unavailable full-text route was repeated instead of advancing to the connector Skill: %#v", choice)
	}
}

func TestDeferredDraftWarningRejectsUnrelatedREPLWorkAsSourceRepair(t *testing.T) {
	run := &sessionRunnerChatRun{}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-publication", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["sources.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-publication", Content: `{
		"completion_pending":true,
		"warnings":[{"code":"unsupported_evidence_references",
		"unsupported_evidence_references":["doi:10.1000/example"]}]
	}`}
	fulltextCall := runnerEvidenceDepthCall("fulltext-unavailable", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/example",
	})
	fulltextResult := runnerEvidenceDepthResult("fulltext-unavailable", map[string]any{
		"ok": true, "result": map[string]any{"available": false, "recordAvailable": false},
	})
	skillCall := runnerEvidenceDepthCall("literature-skill", "skill", map[string]any{"skill": "literature-review"})
	skillResult := runnerEvidenceDepthResult("literature-skill", map[string]any{"ok": true, "loaded": true})
	unrelatedREPLCall := runnerEvidenceDepthCall("rewrite-csv", "repl", map[string]any{
		"code": `print("rewrote sources.csv")`,
	})
	unrelatedREPLResult := runnerEvidenceDepthResult("rewrite-csv", map[string]any{
		"ok": true, "exit_status": "ok", "stdout": "rewrote sources.csv",
	})
	tools := []agentruntime.ToolSchema{
		{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}, {Name: "web_fetch"},
	}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{
		saveCall, saveResult, fulltextCall, fulltextResult, skillCall, skillResult, unrelatedREPLCall, unrelatedREPLResult,
	}, tools).(map[string]any)
	if choice["name"] != "web_fetch" {
		t.Fatalf("unrelated REPL work kept the connector transport selected: %#v", choice)
	}
}

func TestGatewayBindsRequiredMCPSourceClassWhenRepairSelectsREPL(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "prepare a source-backed report"}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-publication", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["sources.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-publication", Content: `{
		"completion_pending":true,
		"warnings":[{"code":"unsupported_evidence_references","path":"sources.csv",
		"unsupported_evidence_references":["doi:10.1000/unavailable"]}]
	}`}
	fulltextCall := runnerEvidenceDepthCall("fulltext", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/unavailable",
	})
	fulltextResult := runnerEvidenceDepthResult("fulltext", map[string]any{
		"ok": true, "result": map[string]any{"available": false, "recordAvailable": false},
	})
	skillCall := runnerEvidenceDepthCall("skill", "skill", map[string]any{"skill": "literature-review"})
	skillResult := runnerEvidenceDepthResult("skill", map[string]any{"ok": true, "loaded": true})
	tools := []agentruntime.ToolSchema{{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}

	choice, _ := gateway.RequiredToolChoice(
		[]agentruntime.Message{saveCall, saveResult, fulltextCall, fulltextResult, skillCall, skillResult}, tools,
	).(map[string]any)
	if choice["name"] != "repl" || run.requiredMCPSourceClassSnapshot() != "publication" {
		t.Fatalf("REPL source authority was not bound to the pending publication repair: choice=%#v class=%q",
			choice, run.requiredMCPSourceClassSnapshot())
	}
}

func TestDeferredDraftWarningClosesRepairSetAfterEverySourceRouteIsExhausted(t *testing.T) {
	run := &sessionRunnerChatRun{}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-publication", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["sources.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-publication", Content: `{
		"completion_pending":true,
		"warnings":[{"code":"unsupported_evidence_references","path":"sources.csv",
		"unsupported_evidence_references":["doi:10.1000/unavailable"]}]
	}`}
	fulltextCall := runnerEvidenceDepthCall("fulltext", "fetch_article_fulltext", map[string]any{
		"doi": "10.1000/unavailable",
	})
	fulltextResult := runnerEvidenceDepthResult("fulltext", map[string]any{
		"ok": true, "result": map[string]any{"available": false, "recordAvailable": false},
	})
	skillCall := runnerEvidenceDepthCall("skill", "skill", map[string]any{"skill": "literature-review"})
	skillResult := runnerEvidenceDepthResult("skill", map[string]any{"ok": true, "loaded": true})
	replCall := runnerEvidenceDepthCall("repl", "repl", map[string]any{"code": `print("not a source read")`})
	replResult := runnerEvidenceDepthResult("repl", map[string]any{
		"ok": true, "exit_status": "ok", "stdout": "not a source read",
	})
	webFetchCall := runnerEvidenceDepthCall("web-fetch", "web_fetch", map[string]any{
		"url": "https://doi.org/10.1000/unavailable",
	})
	webFetchResult := runnerEvidenceDepthResult("web-fetch", map[string]any{
		"ok": true, "result": map[string]any{"sourceUnavailable": true, "statusCode": 404},
	})
	webResearchCall := runnerEvidenceDepthCall("web-research", "web_research", map[string]any{
		"query": "10.1000/unavailable",
	})
	webResearchResult := runnerEvidenceDepthResult("web-research", map[string]any{
		"ok": true, "outcome": "partial", "candidateSources": []any{},
	})
	messages := []agentruntime.Message{
		saveCall, saveResult, fulltextCall, fulltextResult, skillCall, skillResult, replCall, replResult,
		webFetchCall, webFetchResult, webResearchCall, webResearchResult,
	}
	tools := []agentruntime.ToolSchema{
		{Name: "fetch_article_fulltext"}, {Name: "skill"}, {Name: "repl"}, {Name: "web_fetch"},
		{Name: "web_research"}, {Name: "edit_file"}, {Name: "save_artifacts"},
	}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("exhausted source set did not close on the validator-owned file: %#v", choice)
	}
	editCall := runnerEvidenceDepthCall("edit-sources", "edit_file", map[string]any{
		"file_path": "sources.csv", "old_string": "10.1000/unavailable", "new_string": "",
	})
	editResult := runnerEvidenceDepthResult("edit-sources", map[string]any{"success": true, "changed": true})
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(
		run, append(messages, editCall, editResult), tools,
	).(map[string]any)
	if choice["name"] != "save_artifacts" {
		t.Fatalf("closed evidence repair did not publish the changed artifact: %#v", choice)
	}
}

func TestGatewayAppliesImmediateArtifactEvidenceRepairOutsideCompletionCorrection(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "prepare a source-backed report"}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-1", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{
		"ok":false,"code":"artifact_save_requires_correction",
		"errors":[{"code":"unsupported_evidence_references","unsupported_evidence_references":["nct:NCT05564421"]}]
	}`}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	choice, _ := gateway.RequiredToolChoice(
		[]agentruntime.Message{saveCall, saveResult},
		[]agentruntime.ToolSchema{{Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}},
	).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("immediate artifact evidence repair did not reach the gateway: %#v", choice)
	}
}

func TestFailedDelimitedArtifactSaveUsesWriterBeforeRetry(t *testing.T) {
	run := &sessionRunnerChatRun{}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-1", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["source_ledger.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{
		"ok":false,"code":"artifact_save_requires_correction",
		"errors":[{"code":"invalid_delimited_artifact","path":"source_ledger.csv",
		"validation_detail":"record 8 has the wrong number of fields"}]
	}`}
	tools := []agentruntime.ToolSchema{{Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	messages := []agentruntime.Message{saveCall, saveResult}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("malformed delimited artifact did not use the declared artifact editor: %#v", choice)
	}

	writeCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "write-1", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"source_ledger.csv","old_string":"bad,row","new_string":"fixed,row"}`),
	}}}
	writeResult := agentruntime.Message{Role: "tool", ToolCallID: "write-1", Content: `{
		"ok":true,"changed":true,"path":"source_ledger.csv"
	}`}
	messages = append(messages, writeCall, writeResult)
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "save_artifacts" {
		t.Fatalf("writer-corrected delimited artifact did not advance to save: %#v", choice)
	}

	saveCall2 := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-2", Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["source_ledger.csv"]}`),
	}}}
	saveResult2 := agentruntime.Message{Role: "tool", ToolCallID: "save-2", Content: `{"ok":true,"artifacts":[{"filename":"source_ledger.csv"}]}`}
	messages = append(messages, saveCall2, saveResult2)
	if choice := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools); choice != nil {
		t.Fatalf("successful corrected save left a pending file repair: %#v", choice)
	}
}

func TestFailedScientificArtifactSaveUsesWriterBeforeRetry(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "Repair and save the molecule set"}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-scientific", Name: "save_artifacts",
		Arguments: json.RawMessage(`{"files":["ligands.smi"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-scientific", Content: `{
		"ok":false,"code":"artifact_save_requires_correction",
		"errors":[{"code":"invalid_scientific_artifact","path":"ligands.smi",
		"validation_code":"invalid_smiles_records","validation_records":4,
		"validation_parsed_records":3,"validation_invalid_records":1}]
	}`}
	tools := []agentruntime.ToolSchema{
		{Name: "edit_file", Capabilities: []string{"artifact-write", "artifact-edit"}},
		{Name: "manage_environments", Capabilities: []string{"environment-management"}},
		{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}},
	}
	messages := []agentruntime.Message{saveCall, saveResult}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("invalid scientific artifact did not require its writer: %#v", choice)
	}

	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "edit-scientific", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"ligands.smi","old_string":"invalid","new_string":"CC"}`),
	}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-scientific", Content: `{"ok":true,"changed":true}`}
	messages = append(messages, editCall, editResult)
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "save_artifacts" {
		t.Fatalf("scientific repair did not advance to publication: %#v", choice)
	}
}

func TestPartialArtifactSaveRequiresAnotherToolBeforeCompletion(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "Save the requested report and structure"}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-partial", Name: "save_artifacts",
		Arguments: json.RawMessage(`{"files":["report.md","structure.pdb"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-partial", Content: `{
		"ok":false,"partial":true,"code":"artifact_save_requires_correction",
		"artifacts":[{"filename":"report.md"}],
		"errors":[{"path":"structure.pdb","code":"file_not_found","retryable":false}]
	}`}
	tools := []agentruntime.ToolSchema{
		{Name: "edit_file", Capabilities: []string{"artifact-write", "artifact-edit"}},
		{Name: "download_public_scientific_file", Capabilities: []string{"source-download", "artifact-write"}},
		{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}},
	}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	if choice := gateway.RequiredToolChoice([]agentruntime.Message{saveCall, saveResult}, tools); choice != "required" {
		t.Fatalf("partial save allowed immediate completion: %#v", choice)
	}

	repairedCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-repaired", Name: "save_artifacts",
		Arguments: json.RawMessage(`{"files":["structure.pdb"]}`),
	}}}
	repairedResult := agentruntime.Message{Role: "tool", ToolCallID: "save-repaired", Content: `{
		"ok":true,"artifacts":[{"filename":"structure.pdb"}]
	}`}
	if choice := gateway.RequiredToolChoice(
		[]agentruntime.Message{saveCall, saveResult, repairedCall, repairedResult}, tools,
	); choice != nil && choice != "none" {
		t.Fatalf("successful repair left tool choice pending: %#v", choice)
	}
}

func TestRecordDepthCorrectionDoesNotRepeatCompletedWebResearch(t *testing.T) {
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "research-1", Name: "web_research", Arguments: json.RawMessage(`{"operation":"search_and_fetch","query":"company disclosure"}`),
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "research-1", Content: `{
		"artifact_id":"large-tool-result-1","outcome":"partial","content_type":"application/json"
	}`}
	exhausted := runnerCorrectionExhaustedSourceToolsSinceBoundary(
		[]agentruntime.Message{boundary, call, result}, "web",
	)
	if !exhausted["webresearch"] {
		t.Fatalf("completed bounded web research remained selectable in the same correction: %#v", exhausted)
	}

	preflight := agentruntime.Message{Role: "tool", ToolCallID: "research-1", Content: `{
		"executed":false,"status":"source_record_class_preflight_required","message":"use the unresolved source class"
	}`}
	exhausted = runnerCorrectionExhaustedSourceToolsSinceBoundary(
		[]agentruntime.Message{boundary, call, preflight}, "web",
	)
	if exhausted["webresearch"] {
		t.Fatalf("non-executing preflight exhausted web research: %#v", exhausted)
	}
}

func TestRecordDepthCorrectionUsesExistingWebResearchPathForGenericSourceRows(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_missing:sources.csv row=5 source_type=web " +
			"declared_source_type=corporate_disclosure identifier=" + strings.Repeat("a", 64),
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{{Name: "web_fetch"}, {Name: "web_research"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if choice["name"] != "web_fetch" {
		t.Fatalf("generic source row did not use the canonical web read path: %#v", choice)
	}
}

func TestRecordDepthCorrectionDoesNotReleaseAnEditWithoutSourceRecovery(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=1): " +
			"evidence_record_depth_required:web source_type=web reason=at_least_one_substantive_record_read",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "edit-1", Name: "edit_file"}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-1", Content: `{"ok":true,"changed":true}`}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "save-1", Name: "save_artifacts"}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-1", Content: `{"ok":true,"artifacts":[{"filename":"evidence.csv","unchanged":false}]}`}
	withoutSource := []agentruntime.Message{boundary, editCall, editResult, saveCall, saveResult}
	if !sessionRunnerCorrectionStillRequiresAction(run, withoutSource) {
		t.Fatal("artifact edit/save without a source attempt discharged record-depth recovery")
	}

	webCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "web-1", Name: "web_fetch", VerifiedEvidence: true,
		Arguments: json.RawMessage(`{"url":"https://example.test/primary-record"}`),
	}}}
	webResult := agentruntime.Message{Role: "tool", ToolCallID: "web-1", Content: `{
		"ok":true,"result":{"url":"https://example.test/primary-record","statusCode":200,
		"body":"` + strings.Repeat("Substantive methods, results, limitations, and evidence for screening. ", 12) + `"}}`}
	withSource := []agentruntime.Message{boundary, webCall, webResult, editCall, editResult, saveCall, saveResult}
	if sessionRunnerCorrectionStillRequiresAction(run, withSource) {
		t.Fatal("source read followed by changed edit/save did not return to completion validation")
	}
}

func TestSourceLocatorCorrectionFallsThroughAnUnavailableSource(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a source-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=1): " +
			"evidence_source_locator_missing:evidence.csv row=4 source_type=paper",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "fulltext-1", Name: "fetch_article_fulltext", Arguments: json.RawMessage(`{"doi":"10.1000/missing"}`),
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "fulltext-1", Content: `{
		"ok":true,"result":{"available":false,"status":"not_available","reason":"full_text_not_found",
		"doi":"10.1000/missing","sourceUrl":"https://example.test/missing"}}`}
	messages := []agentruntime.Message{boundary, call, result}
	var unavailable any
	_ = json.Unmarshal([]byte(result.Content), &unavailable)
	if runnerCorrectionSourceCallUsable(call.ToolCalls[0], unavailable) {
		t.Fatal("unavailable full text was mistaken for usable evidence")
	}
	if !sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("unavailable full text satisfied the source-locator correction")
	}
	tools := []agentruntime.ToolSchema{{Name: "fetch_article_fulltext"}, {Name: "repl"}}
	if choice := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools); choice != nil {
		t.Fatalf("unavailable source prescribed a fixed fallback route=%#v", choice)
	}
	tools = []agentruntime.ToolSchema{{Name: "fetch_article_fulltext"}, {Name: "web_search"}, {Name: "web_fetch"}, {Name: "repl"}}
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any); choice["name"] != "web_search" {
		t.Fatalf("unavailable full text did not advance to atomic source discovery: %#v", choice)
	}
	searchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "search-1", Name: "web_search"}}}
	searchResult := agentruntime.Message{Role: "tool", ToolCallID: "search-1", Content: `{"ok":true,"results":[{"url":"https://pubmed.ncbi.nlm.nih.gov/1/"}]}`}
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, append(messages, searchCall, searchResult), tools).(map[string]any); choice["name"] != "web_fetch" {
		t.Fatalf("discovery result did not advance to its returned source locator: %#v", choice)
	}
}

func TestPatentDepthCorrectionAdvancesToArtifactRepairAfterFullRecord(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_missing:evidence.csv row=2 source_type=patent identifier=US20230241233A1",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	lookup := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-lookup", Name: "patent_search", VerifiedEvidence: true,
		Arguments: json.RawMessage(`{"operation":"lookup","publication_number":"US20230241233A1","focus_terms":["oral peptide","delivery"]}`),
	}}}
	fullRecord := agentruntime.Message{Role: "tool", ToolCallID: "patent-lookup", Content: `{
		"ok":true,"result":{"operation":"lookup","evidenceDepth":"full_record",
		"records":[{"publicationNumber":"US20230241233A1","recordDepth":"full_record","focusRelevant":true,"sectionsRead":["abstract","claims","description"]}]}}`}
	messages := []agentruntime.Message{boundary, lookup, fullRecord}
	if !runnerCorrectionSourceCallUsable(lookup.ToolCalls[0], map[string]any{"result": map[string]any{
		"operation": "lookup", "evidenceDepth": "full_record", "records": []any{map[string]any{
			"publicationNumber": "US20230241233A1", "recordDepth": "full_record", "focusRelevant": true,
		}},
	}}) {
		t.Fatal("a successful full patent record was not recognized as source evidence")
	}
	tools := []agentruntime.ToolSchema{{Name: "patent_search"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any); choice["name"] != "edit_file" {
		t.Fatalf("post-patent-depth correction did not advance to artifact repair: %#v", choice)
	}

	partial := agentruntime.Message{Role: "tool", ToolCallID: "patent-lookup", Content: `{
		"ok":true,"result":{"operation":"lookup","evidenceDepth":"locator_only",
		"records":[{"publicationNumber":"US20230241233A1","recordDepth":"partial_record","sectionsRead":["abstract"]}]}}`}
	var partialValue any
	_ = json.Unmarshal([]byte(partial.Content), &partialValue)
	if runnerCorrectionSourceCallUsable(lookup.ToolCalls[0], partialValue) {
		t.Fatal("an abstract-only patent lookup was mistaken for a full record")
	}
}

func TestPatentDepthCorrectionKeepsCanonicalPatentRouteForFullCrossLanguageRecord(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "patent-lookup", Name: "patent_search",
		Arguments: json.RawMessage(`{"operation":"lookup","publication_number":"CN115698003B","focus_terms":["口服小分子","受体激动剂"]}`),
	}
	result := map[string]any{"result": map[string]any{
		"operation": "lookup", "evidenceDepth": "full_record",
		"records": []any{map[string]any{
			"publicationNumber": "CN115698003B", "recordDepth": "full_record",
			"focusRelevant": false, "sectionsRead": []any{"abstract", "claims", "description"},
		}},
	}}
	if !runnerCorrectionSourceCallUsable(call, result) {
		t.Fatal("a fully read patent was incorrectly treated as an exhausted transport route")
	}
}

func TestPatentDepthCorrectionDoesNotRepeatAnIneffectiveLookup(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_missing:evidence.csv row=2 source_type=patent identifier=US20230040805A1",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := func(id, name string, arguments string) agentruntime.Message {
		return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(arguments)}}}
	}
	result := func(id, content string) agentruntime.Message {
		return agentruntime.Message{Role: "tool", ToolCallID: id, Content: content}
	}
	messages := []agentruntime.Message{
		boundary,
		call("patent-1", "patent_search", `{"operation":"lookup","publication_number":"US20230040805A1"}`),
		result("patent-1", `{"ok":true,"result":{"evidenceDepth":"locator_only","records":[{"publicationNumber":"US20230040805A1","recordDepth":"partial_record"}]}}`),
	}
	tools := []agentruntime.ToolSchema{{Name: "patent_search"}, {Name: "read_file"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	if choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any); choice["name"] != "edit_file" {
		t.Fatalf("exhausted patent source did not advance to bounded unsupported-row repair: %#v", choice)
	}
	messages = append(messages,
		call("read-1", "read_file", `{}`), result("read-1", `{"ok":true,"content":"current ledger"}`),
		call("edit-1", "edit_file", `{}`), result("edit-1", `{"ok":true,"changed":true}`),
		call("save-1", "save_artifacts", `{}`), result("save-1", `{"ok":true,"artifacts":[{"id":"ledger"}]}`),
	)
	if sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("a changed save after exhausted source lookup did not satisfy the bounded unsupported-row removal")
	}
}

func TestRecoveredCorrectionContextIsSingleAndTerminal(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "system", Content: "base"},
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker + ": repair report.md"},
		{Role: "system", Content: "later skill and language policy"},
		{Role: "user", Content: "task"},
	}
	got := moveRecoveredRunnerCorrectionContextToEnd(messages)
	count := 0
	for _, message := range got {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			count++
		}
	}
	if count != 1 || !strings.Contains(got[len(got)-1].Content, "repair report.md") {
		t.Fatalf("terminal correction projection=%#v", got)
	}
}

func TestValidatedPublicDownloadCorrectionRequiresDownloadTool(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "retrieve public scientific guidance",
		CorrectionReason: sessionRunnerRealScientificEvidenceRequiredReasonCode,
		CorrectionDetail: "the task explicitly requires downloaded public scientific source material but has no validated public scientific download receipt",
	}
	tools := []agentruntime.ToolSchema{
		{Name: "ask_user"}, {Name: "web_search"}, {Name: "web_fetch"}, {Name: "fetch_article_fulltext"}, {Name: "download_public_scientific_file"},
	}
	filtered := sessionRunnerCorrectionToolSchemas(tools, run.CorrectionReason, run.CorrectionDetail)
	if agentRuntimeToolSchemaNamed(filtered, "ask_user") {
		t.Fatal("download recovery still advertised ask_user")
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, nil, filtered).(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "web_fetch" {
		t.Fatalf("download recovery choice=%#v", choice)
	}

	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	fetchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "fetch-1", Name: "web_fetch"}}}
	fetchResult := agentruntime.Message{Role: "tool", ToolCallID: "fetch-1", Content: `{"ok":true,"url":"https://example.test/guidance.pdf"}`}
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary, fetchCall, fetchResult}, filtered).(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "download_public_scientific_file" {
		t.Fatalf("post-fetch download recovery choice=%#v", choice)
	}
	searchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "search-1", Name: "web_search"}}}
	searchResult := agentruntime.Message{Role: "tool", ToolCallID: "search-1", Content: `{"ok":true,"sources":[{"url":"https://example.test/guidance.pdf"}]}`}
	if !sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, fetchCall, fetchResult, searchCall, searchResult}) {
		t.Fatal("search result incorrectly satisfied required download receipt")
	}
	downloadCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "download-1", Name: "download_public_scientific_file"}}}
	downloadResult := agentruntime.Message{Role: "tool", ToolCallID: "download-1", Content: `{"ok":true,"status":"completed","path":"sources/guidance.pdf"}`}
	if sessionRunnerCorrectionStillRequiresAction(run, []agentruntime.Message{boundary, fetchCall, fetchResult, searchCall, searchResult, downloadCall, downloadResult}) {
		t.Fatal("successful validated download did not satisfy correction")
	}
}

func TestValidatedPublicDownloadGatewayAdvancesFromBinaryFetchToDownload(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "retrieve public scientific guidance",
		CorrectionReason: sessionRunnerRealScientificEvidenceRequiredReasonCode,
		CorrectionDetail: "the task explicitly requires downloaded public scientific source material but has no validated public scientific download receipt",
	}
	tools := []agentruntime.ToolSchema{
		{Name: "web_fetch"}, {Name: "download_public_scientific_file"},
	}
	messages := []agentruntime.Message{
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "fetch-1", Name: "web_fetch"}}},
		{Role: "tool", ToolCallID: "fetch-1", Content: `{"ok":true,"result":{"binary":true,"body":"","bytesRead":524288,"contentLength":718888,"contentType":"application/pdf","partial":true,"recovery":"use_dedicated_download_or_fulltext_tool","statusCode":200,"truncated":true,"url":"https://www.fda.gov/media/72309/download"}}`},
	}
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	choice, _ := gateway.RequiredToolChoice(messages, tools).(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "download_public_scientific_file" {
		t.Fatalf("post-binary-fetch gateway choice=%#v", choice)
	}
}

func TestSourceDepthCorrectionUsesConnectorForIdentifierOnlyFallback(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_depth_missing:evidence.csv row=2 source_type=patent identifier=US20230040805A1",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-lookup", Name: "patent_search", Arguments: json.RawMessage(`{"operation":"lookup","publication_number":"US20230040805A1"}`),
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "patent-lookup", Content: `{"ok":true,"result":{"evidenceDepth":"locator_only","records":[{"publicationNumber":"US20230040805A1","recordDepth":"locator"}]}}`}
	tools := []agentruntime.ToolSchema{{Name: "patent_search"}, {Name: "web_fetch"}, {Name: "web_research"}, {Name: "repl"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary, call, result}, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("identifier-only source correction did not use the untried connector bridge: %#v", choice)
	}
}

func TestPatentDiscoveryCorrectionAdvancesAfterCompletedEmptySearch(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=patent reason=requested_source_class_not_represented",
	}
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-search", Name: "patent_search", Arguments: json.RawMessage(`{"operation":"search","query":"oral small molecule PCSK9 inhibitor"}`),
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "patent-search", Content: `{"ok":true,"result":{"operation":"search","records":[],"retrieval":{"complete":true,"source_attempts":20,"source_failures":20}}}`}
	messages := []agentruntime.Message{boundary, call, result}
	tools := []agentruntime.ToolSchema{{Name: "patent_search"}, {Name: "skill"}, {Name: "web_research"}, {Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	if !runnerCorrectionExhaustedSourceToolsSinceBoundary(messages, "patent")["patentsearch"] {
		t.Fatal("completed empty patent discovery was not marked exhausted")
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("empty patent discovery kept selecting the same route: %#v", choice)
	}

	partial := agentruntime.Message{Role: "tool", ToolCallID: "patent-search", Content: `{"ok":true,"result":{"operation":"search","records":[],"retrieval":{"complete":false}}}`}
	partialMessages := []agentruntime.Message{boundary, call, partial}
	if runnerCorrectionExhaustedSourceToolsSinceBoundary(partialMessages, "patent")["patentsearch"] {
		t.Fatal("incomplete empty patent discovery was incorrectly marked exhausted")
	}
}

func TestPatentDiscoveryCorrectionCarriesEmptyRouteAcrossBoundaries(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=patent reason=requested_source_class_not_represented",
	}
	firstBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	call := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-search-1", Name: "patent_search", Arguments: json.RawMessage(`{"operation":"search","query":"oral small molecule PCSK9 inhibitor"}`),
	}}}
	result := agentruntime.Message{Role: "tool", ToolCallID: "patent-search-1", Content: `{"ok":true,"result":{"operation":"search","records":[],"retrieval":{"complete":true}}}`}
	secondBoundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	messages := []agentruntime.Message{firstBoundary, call, result, secondBoundary}
	if !runnerCorrectionPreviouslyExhaustedSourceTools(messages, "patent")["patentsearch"] {
		t.Fatal("completed empty patent discovery was not carried across correction boundaries")
	}
	tools := []agentruntime.ToolSchema{{Name: "patent_search"}, {Name: "skill"}, {Name: "web_research"}, {Name: "repl"}}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("new correction boundary replayed exhausted patent discovery: %#v", choice)
	}
}

func TestSourceCorrectionUsesRemainingConnectorThenFallsThroughToArtifactEdit(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a patent-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "evidence_record_class_missing:source_type=patent reason=requested_source_class_not_represented",
	}
	// The connector Skill was already materialized in an earlier execution
	// unit. No direct patent MCP method is advertised in this runtime.
	run.addExecutedSkillNames("patent-search", "mcp-patent-search")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	searchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "patent-search", Name: "patent_search", Arguments: json.RawMessage(`{"operation":"search","query":"oral small molecule PCSK9 inhibitor"}`),
	}}}
	searchResult := agentruntime.Message{Role: "tool", ToolCallID: "patent-search", Content: `{"ok":true,"result":{"operation":"search","records":[],"retrieval":{"complete":true}}}`}
	fetchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "web-fetch", Name: "web_fetch"}}}
	fetchResult := agentruntime.Message{Role: "tool", ToolCallID: "web-fetch", Content: `{"ok":true,"result":{"statusCode":200,"body":"A public landing page with patent context."}}`}
	researchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "web-research", Name: "web_research"}}}
	researchResult := agentruntime.Message{Role: "tool", ToolCallID: "web-research", Content: `{"ok":true,"result":{"quality":{"meetsTarget":true,"deepReadSources":1},"documents":[{"title":"context"}]}}`}
	webSearchCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "web-search", Name: "web_search"}}}
	webSearchResult := agentruntime.Message{Role: "tool", ToolCallID: "web-search", Content: `{"ok":true,"sources":[{"url":"https://example.test/patent-context"}]}`}
	messages := []agentruntime.Message{boundary, searchCall, searchResult, fetchCall, fetchResult, researchCall, researchResult, webSearchCall, webSearchResult}
	tools := []agentruntime.ToolSchema{
		{Name: "patent_search"}, {Name: "skill"}, {Name: "web_fetch"}, {Name: "web_research"}, {Name: "web_search"},
		{Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"},
	}
	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("untried connector bridge was skipped: %#v", choice)
	}
	replCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "repl-source", Name: "repl", Arguments: json.RawMessage(`{"code":"raise RuntimeError('connector unavailable')"}`),
	}}}
	replResult := agentruntime.Message{Role: "tool", ToolCallID: "repl-source", Content: `{"ok":false,"status":"failed","error":"connector unavailable"}`}
	messages = append(messages, replCall, replResult)
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, messages, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("exhausted capability routes did not converge on artifact repair: %#v", choice)
	}
}

func TestRunnerCorrectionResultUnchangedRequiresEveryArtifactUnchanged(t *testing.T) {
	changedReport := map[string]any{"ok": true, "artifacts": []any{
		map[string]any{"filename": "report.md", "unchanged": false},
		map[string]any{"filename": "sources.csv", "unchanged": true},
	}}
	if runnerCorrectionResultUnchanged(changedReport) {
		t.Fatal("a changed report paired with an unchanged ledger was classified as a no-op")
	}
	unchanged := map[string]any{"ok": true, "artifacts": []any{
		map[string]any{"filename": "report.md", "unchanged": true},
		map[string]any{"filename": "sources.csv", "unchanged": true},
	}}
	if !runnerCorrectionResultUnchanged(unchanged) {
		t.Fatal("all-unchanged artifacts were not classified as a no-op")
	}
}
