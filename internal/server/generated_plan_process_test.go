package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
	"synon-go/internal/toolgateway"
)

func researchToolSchemaForTest() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name: "web_research", Capabilities: []string{"research", "source-evidence"},
		Exposure: agentruntime.ToolExposureDeferred,
	}
}

func newGeneratedPlanProcessFixture(t *testing.T) (*Server, *workspace.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "research-process-project", UserID: "local", Name: "Research process", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "research-process-frame", ProjectID: project.ID, AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Research process",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{
		"version": 3, "task_summary": "Research and deliver",
		"phases": []any{
			map[string]any{"id": "phase-1", "name": "Research", "delegations": []any{map[string]any{
				"id": "track-1", "name": "Modules", "steps": []any{
					map[string]any{
						"id": "module-1", "title": "Mechanism", "description": "Research mechanism evidence.",
						"kind": "research", "output_module": "Mechanism", "research_question": "What establishes the mechanism?",
						"research_depth": "deep", "discovery_queries": []any{
							map[string]any{"language": "zh", "query": "作用机制 原始证据"},
							map[string]any{"language": "en", "query": "mechanism primary evidence"},
						},
					},
					map[string]any{
						"id": "module-2", "title": "Pipeline", "description": "Research development evidence.",
						"kind": "research", "output_module": "Pipeline", "research_question": "What is the development status?",
						"research_depth": "systematic", "discovery_queries": []any{
							map[string]any{"language": "zh", "query": "POLQ 抑制剂 研发进展"},
							map[string]any{"language": "en", "query": "POLQ inhibitor development pipeline"},
						},
					},
				},
			}}},
			map[string]any{"id": "phase-2", "name": "Delivery", "delegations": []any{map[string]any{
				"id": "track-2", "name": "Output", "steps": []any{map[string]any{
					"id": "delivery-1", "title": "Publish report", "description": "Save the report.", "kind": "delivery",
				}},
			}}},
		},
		"desired_outputs": []any{"report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "Sources are available."},
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_execution_authorized": true, "_plan_control_mode": "autonomous",
		"_plan_artifact_id": "plan", "_plan_version_id": "version", "_plan_json": plan,
		"_step_statuses": map[string]any{"module-1": map[string]any{
			"status": "in_progress", "title": "Mechanism", "description": "Research mechanism evidence.",
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	return New(Options{FileRoot: root, Workspace: store}), store, frame.ID
}

func TestResearchProcessInjectsOneBilingualModuleQuerySet(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	input := server.generatedPlanResearchQueryInput(frameID, "web_research", map[string]any{
		"operation": "search_and_fetch", "query": "model-selected focus",
		"query_variants": []any{
			"caller lane 1", "caller lane 2", "caller lane 3", "caller lane 4", "caller lane 5", "caller lane 6",
		},
	})
	if input["query"] != "作用机制 原始证据" || input["research_depth"] != "deep" {
		t.Fatalf("active module query or depth was not authoritative: %#v", input)
	}
	variants := stringValueSlice(input["query_variants"])
	if len(variants) != 1 || variants[0] != "mechanism primary evidence" {
		t.Fatalf("module bilingual queries did not reach web research: %#v", input)
	}
	webSearch := server.generatedPlanResearchQueryInput(frameID, "web_search", map[string]any{"query": "model-selected focus"})
	if webSearch["query"] != "作用机制 原始证据" || len(stringValueSlice(webSearch["query_variants"])) != 1 {
		t.Fatalf("web search did not share the module query set: %#v", webSearch)
	}
}

func TestAutonomousPlanPendingStepDoesNotDisplaceSubstantiveWork(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{"status": "completed"},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	if choice := gateway.generatedPlanRequiredToolChoice(nil, schemas); choice != nil {
		t.Fatalf("autonomous pending step displaced substantive model choice: %#v", choice)
	}
	if priority := gateway.generatedPlanPriorityToolChoice(nil, schemas); priority != nil {
		t.Fatalf("autonomous navigation became a forced control action: %#v", priority)
	}

	normalized := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{
		"step": "module-2", "status": "completed", "notes": "claimed without starting",
		"observations": []any{"unbound conclusion"},
	})
	if normalized["step"] != "module-2" || normalized["status"] != "completed" {
		t.Fatalf("explicit pending-step completion was rewritten: %#v", normalized)
	}
	if normalized["notes"] != "claimed without starting" || len(anySliceValue(normalized["observations"])) != 1 {
		t.Fatalf("explicit completion lost model-owned findings: %#v", normalized)
	}

	metadata, found, err = store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	approvedChoice := mapValue(gateway.generatedPlanRequiredToolChoice(nil, schemas))
	if approvedChoice["name"] != updateStepStatusToolName {
		t.Fatalf("explicitly approved plan lost strict progress control: %#v", approvedChoice)
	}
	approved := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{
		"step": "module-2", "status": "completed", "notes": "completion before approved start",
	})
	if approved["status"] != "in_progress" || approved["notes"] != nil {
		t.Fatalf("approved pending step bypassed its start transition: %#v", approved)
	}
}

func TestMissingDeliverableCorrectionFinishesActivePlanStepBeforeRepublishing(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{"status": "completed"},
		"module-2": map[string]any{"status": "in_progress"},
	}
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	missingOnly := "runner completion reference integrity failed " +
		"(unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=0 " +
		"invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=0 " +
		"invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=1)"
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{
			CorrectionReason: "artifact_reference_correction_required", CorrectionDetail: missingOnly,
		},
	}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "edit_file"}, {Name: "save_artifacts"}}
	choice := mapValue(gateway.generatedPlanPriorityToolChoice(nil, schemas))
	if choice["name"] != updateStepStatusToolName {
		t.Fatalf("pure missing-deliverable correction choice=%#v", choice)
	}

	gateway.taskRun.CorrectionDetail = strings.Replace(missingOnly,
		"cross_artifact_failures=0", "cross_artifact_failures=1", 1)
	if choice := mapValue(gateway.generatedPlanPriorityToolChoice(nil, schemas)); choice["name"] != updateStepStatusToolName {
		t.Fatalf("coexisting artifact finding blocked plan transition: %#v", choice)
	}
	gateway.taskRun.CorrectionDetail = strings.Replace(gateway.taskRun.CorrectionDetail,
		"missing_required_deliverables=1", "missing_required_deliverables=0", 1)
	if choice := gateway.generatedPlanPriorityToolChoice(nil, schemas); choice != nil {
		t.Fatalf("artifact-only correction was displaced by plan completion: %#v", choice)
	}
}

func TestAutonomousResearchCorrectionKeepsRequiredSourceReadReachable(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	module := mapValue(mapValue(metadata.ContextData["_step_statuses"])["module-1"])
	module["research_continuation"] = map[string]any{
		"schema":              "synon.research-source-read.v1",
		"reason":              "research_source_read_required",
		"required":            true,
		"blocking":            true,
		"required_capability": "evidence-read",
		"available_read_targets": []any{map[string]any{
			"url": "https://evidence.example/primary-record",
		}},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	missingOnly := "runner completion reference integrity failed " +
		"(unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=0 " +
		"invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=0 " +
		"invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=1)"
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{
			TaskIntent: "Research and deliver", CorrectionReason: "artifact_reference_correction_required",
			CorrectionDetail: missingOnly,
		},
	}
	tools := []agentruntime.ToolSchema{
		{Name: updateStepStatusToolName, Capabilities: []string{"plan-progress"}},
		{Name: "web_fetch", Capabilities: []string{"source-locator-read", "evidence-read"}},
		{Name: "edit_file", Capabilities: []string{"artifact-edit"}},
		{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}},
	}
	choice := mapValue(gateway.RequiredToolChoice(nil, tools))
	if choice["name"] != "web_fetch" {
		t.Fatalf("artifact correction displaced the required source read: %#v", choice)
	}
	normalized := server.generatedPlanResearchQueryInput(
		frameID, "web_fetch", map[string]any{"url": "https://model.example/guess"},
		[]string{"source-locator-read", "evidence-read"},
	)
	if normalized["url"] != "https://evidence.example/primary-record" {
		t.Fatalf("required source-read target did not reach the reader: %#v", normalized)
	}
}

func TestRepeatedInProgressUpdatePreservesRequiredResearchContinuation(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	module := mapValue(mapValue(metadata.ContextData["_step_statuses"])["module-1"])
	module["research_continuation"] = map[string]any{
		"schema": "synon.research-source-read.v1", "required": true, "blocking": true,
		"reason": "research_source_read_required", "required_capability": "evidence-read",
		"available_read_targets": []any{map[string]any{"url": "https://evidence.example/source"}},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	result, err := server.executeAgentUpdateStepStatus(
		context.Background(), frameID, "repeat-status", map[string]any{
			"step": "module-1", "status": "in_progress",
		},
	)
	continuation := mapValue(mapValue(result)["research_continuation"])
	if err != nil || !boolValue(continuation["required"], false) || !boolValue(mapValue(result)["idempotent"], false) {
		t.Fatalf("required continuation was cleared by repeated status update: result=%#v err=%v", result, err)
	}
}

func TestAutonomousPlanProgressPreservesRequestedStepIdentity(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	input := map[string]any{
		"step": "module-2", "status": "in_progress", "notes": "pipeline work started",
		"observations": []any{"pipeline observation"},
	}
	normalized := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, input)
	if normalized["step"] != "module-2" || normalized["status"] != "in_progress" ||
		normalized["notes"] != "pipeline work started" || len(anySliceValue(normalized["observations"])) != 1 {
		t.Fatalf("autonomous navigation rewrote model-owned progress: %#v", normalized)
	}
}

func TestAutonomousResearchSavePreservesExplicitSnapshotIntent(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	input := map[string]any{
		"files": []any{"report.md", "evidence.csv"},
		"destination": map[string]any{
			"report.md": "snapshot", "evidence.csv": "snapshot",
		},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{TaskIntent: "Generate report.md and evidence.csv"},
	}
	normalized := gateway.normalizeAdmittedToolArguments("save_artifacts", input)
	for _, path := range []string{"report.md", "evidence.csv"} {
		if mode := stringValue(mapValue(normalized["destination"])[path]); mode != "snapshot" {
			t.Fatalf("explicit %s snapshot intent was rewritten as %q: %#v", path, mode, normalized)
		}
	}
}

func TestResearchProcessNormalizesCanonicalToolAliases(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	progress := server.generatedPlanUpdateStepStatusInput(frameID, "UpdateStepStatus", map[string]any{
		"step": "stale-step", "status": "in_progress",
	})
	if progress["step"] != "module-1" {
		t.Fatalf("canonical progress alias bypassed active-step binding: %#v", progress)
	}
	research := server.generatedPlanResearchQueryInput(frameID, "WebResearch", map[string]any{
		"operation": "search_and_fetch", "query": "stale query",
	})
	if research["query"] != "作用机制 原始证据" || research["research_depth"] != "deep" ||
		stringValue(mapValue(research["research_session"])["mode"]) != "start" {
		t.Fatalf("canonical research alias bypassed module routing: %#v", research)
	}
}

func TestApprovedResearchProcessConvertsLaterStepStartIntoActiveResearchCompletion(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	withoutEvidence := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{
		"step": "Pipeline", "status": "in_progress", "notes": "pipeline notes",
		"observations": []any{"pipeline observation"}, "source_refs": []any{"pipeline-source"},
		"follow_ups": []any{"pipeline follow-up"},
	})
	if withoutEvidence["step"] != "module-1" || withoutEvidence["status"] != "in_progress" {
		t.Fatalf("research advanced without source evidence: %#v", withoutEvidence)
	}
	for _, field := range []string{"notes", "observations", "source_refs", "follow_ups"} {
		if _, present := withoutEvidence[field]; present {
			t.Fatalf("later-step %s leaked into the active module: %#v", field, withoutEvidence)
		}
	}

	metadata, found, err = store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	module := mapValue(mapValue(metadata.ContextData["_step_statuses"])["module-1"])
	module["source_receipts"] = []any{map[string]any{
		"event_id": 7, "tool_call_id": "source-7", "tool_name": "web_research", "material_role": "evidence",
	}}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	transition := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{
		"step": "Pipeline", "status": "in_progress", "notes": "pipeline notes",
		"observations": []any{"pipeline observation"}, "source_refs": []any{"pipeline-source"},
		"follow_ups": []any{"pipeline follow-up"},
	})
	if transition["step"] != "module-1" || transition["status"] != "completed" {
		t.Fatalf("later-step transition did not close the evidenced active module: %#v", transition)
	}
	for _, field := range []string{"notes", "observations", "source_refs", "follow_ups"} {
		if _, present := transition[field]; present {
			t.Fatalf("completed module inherited later-step %s: %#v", field, transition)
		}
	}
}

func TestResearchProcessPreservesPayloadForRequestedActionableStep(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	statuses := mapValue(metadata.ContextData["_step_statuses"])
	statuses["module-2"] = map[string]any{
		"status": "in_progress", "title": "Pipeline", "description": "Research development evidence.",
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	input := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{
		"step": "Pipeline", "status": "in_progress", "notes": "pipeline notes",
		"observations": []any{"pipeline observation"}, "source_refs": []any{"pipeline-source"},
		"follow_ups": []any{"pipeline follow-up"},
	})
	if input["step"] != "module-2" || input["notes"] != "pipeline notes" ||
		len(anySliceValue(input["observations"])) != 1 || len(anySliceValue(input["source_refs"])) != 1 ||
		len(anySliceValue(input["follow_ups"])) != 1 {
		t.Fatalf("requested actionable step lost its own payload: %#v", input)
	}
}

func TestResearchProcessRecoversDeterministicPlanControlAfterProviderProtocolFailure(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey] = int64(42)
	metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey] = int64(41)
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{TaskIntent: "Research and deliver"},
	}
	tools := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, researchToolSchemaForTest()}
	call, recovered := gateway.RecoverRequiredToolCall(updateStepStatusToolName, nil, tools)
	if !recovered || call.Name != updateStepStatusToolName || !call.RuntimeRecovered {
		t.Fatalf("control call=%#v recovered=%t", call, recovered)
	}
	var input map[string]any
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		t.Fatal(err)
	}
	if input["step"] != "module-1" || input["status"] != "completed" {
		t.Fatalf("control input=%#v", input)
	}
	if call, recovered := gateway.RecoverRequiredToolCall("web_research", nil, tools); recovered {
		t.Fatalf("scientific source call was synthesized by the runtime: %#v", call)
	}
}

func TestResearchProcessRecoversExactSourceOwnedContinuationAfterProviderProtocolFailure(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	module := mapValue(mapValue(metadata.ContextData["_step_statuses"])["module-1"])
	module["research_continuation"] = map[string]any{
		"continuation_id": "continuation-7",
		"next_actions": []any{map[string]any{
			"action": "fetch", "url": "https://example.test/evidence-7",
		}},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID}
	tools := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "web_fetch"}}
	call, recovered := gateway.RecoverRequiredToolCall("web_fetch", nil, tools)
	if !recovered || call.Name != "web_fetch" || !call.RuntimeRecovered ||
		!strings.Contains(string(call.Arguments), "https://example.test/evidence-7") {
		t.Fatalf("source continuation call=%#v recovered=%t", call, recovered)
	}

	module["research_continuation"] = nil
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	if call, recovered := gateway.RecoverRequiredToolCall("web_fetch", nil, tools); recovered {
		t.Fatalf("runtime invented a source call without durable continuation: %#v", call)
	}
}

func TestResearchProcessDoesNotForceAdvisorySourceContinuation(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	module := mapValue(mapValue(metadata.ContextData["_step_statuses"])["module-1"])
	module["research_continuation"] = map[string]any{
		"continuation_id": "advisory-7", "required": false, "blocking": false, "quality_advisory": true,
		"next_actions": []any{map[string]any{
			"action": "fetch", "url": "https://example.test/evidence-7",
		}},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID}
	tools := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "web_fetch"}}
	if call, recovered := gateway.RecoverRequiredToolCall("web_fetch", nil, tools); recovered {
		t.Fatalf("runtime replayed an advisory continuation: %#v", call)
	}
	if choice := gateway.generatedPlanPriorityToolChoice(nil, tools); choice != nil {
		t.Fatalf("advisory continuation forced tool choice: %#v", choice)
	}
}

func TestRuntimeRecoveredPlanControlHasTruthfulCheckpointAttribution(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	options := SessionRunnerChatOptions{SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	call := agentruntime.ToolCall{
		ID: "runtime-plan-control", Name: updateStepStatusToolName,
		Arguments: json.RawMessage(`{"step":"module-1","status":"completed"}`), RuntimeRecovered: true,
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := fixture.db.QueryRow(`SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND json_extract(payload_json,'$.modelToolCalls') IS NOT NULL ORDER BY event_id DESC LIMIT 1`,
		fixture.stream.UID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]any
	if json.Unmarshal(raw, &checkpoint) != nil {
		t.Fatalf("decode runtime recovery checkpoint: %s", raw)
	}
	calls := anySliceValue(checkpoint["modelToolCalls"])
	if len(calls) != 1 || !strings.Contains(stringValue(checkpoint["message"]), "runtime recovered") ||
		!boolValue(mapValue(calls[0])["runtimeRecovered"], false) {
		t.Fatalf("runtime recovery attribution missing: %s", raw)
	}
}

func TestGatewayReturnsTheExecutedPlanArguments(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun:      &sessionRunnerChatRun{TaskIntent: "Research and deliver"},
		allowedTools: []string{updateStepStatusToolName}, toolSchemas: schemas,
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "normalized-progress", Name: updateStepStatusToolName,
		Arguments: json.RawMessage(`{"step":"stale-step","status":"in_progress"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var executed map[string]any
	if json.Unmarshal(result.ExecutedArguments, &executed) != nil || executed["step"] != "module-1" || executed["status"] != "in_progress" {
		t.Fatalf("gateway did not return canonical executed arguments: %s", result.ExecutedArguments)
	}
}

func TestGatewayPreservesExecutedArgumentsWhenExecutionReturnsError(t *testing.T) {
	wantErr := errors.New("executor failed after admission")
	result, err := serverAgentRuntimeGatewayResult(serverAgentRuntimeGatewayReceipt{
		Result: agentruntime.ToolResult{Value: map[string]any{"ok": false}},
		Input:  map[string]any{"url": "https://evidence.example/executed"},
		Err:    wantErr,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("execution error was lost: %v", err)
	}
	var executed map[string]any
	if json.Unmarshal(result.ExecutedArguments, &executed) != nil ||
		executed["url"] != "https://evidence.example/executed" {
		t.Fatalf("executed identity was lost on error: %s", result.ExecutedArguments)
	}
}

func TestTerminalCheckpointSeparatesRequestedAndExecutedArguments(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	options := SessionRunnerChatOptions{SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	call := agentruntime.ToolCall{
		ID: "dual-input-source", Name: "web_research",
		Arguments: json.RawMessage(`{"operation":"search_and_fetch","query":"model next module"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments),
	}); err != nil {
		t.Fatal(err)
	}
	executed := `{"operation":"search_and_fetch","query":"source-owned follow-up","research_session":{"id":"session-1","mode":"continue"}}`
	if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID,
		Arguments: string(call.Arguments), ExecutedArguments: executed,
		Result: `{"ok":true,"result":{"documents":[],"quality":{"deepReadSources":0}}}`,
	}); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || len(items) != 1 || items[0].TerminalEventID <= 0 {
		t.Fatalf("terminal items=%#v err=%v", items, err)
	}
	var raw []byte
	if err := fixture.db.QueryRow(`SELECT payload_json FROM transcript_events WHERE stream_uid = ? AND event_id = ?`,
		fixture.stream.UID, items[0].TerminalEventID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]any
	if json.Unmarshal(raw, &checkpoint) != nil || stringValue(mapValue(checkpoint["toolInput"])["query"]) != "model next module" ||
		stringValue(mapValue(checkpoint["executedToolInput"])["query"]) != "source-owned follow-up" {
		t.Fatalf("terminal checkpoint collapsed input identities: %s", raw)
	}
}

func TestResearchProcessRepairsMissingProgressStatusFromActiveState(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	progress := server.generatedPlanUpdateStepStatusInput(frameID, updateStepStatusToolName, map[string]any{})
	if progress["step"] != "module-1" || progress["status"] != "in_progress" {
		t.Fatalf("missing progress identity was not restored from durable active state: %#v", progress)
	}
}

func TestResearchProcessSwitchesQueryAuthorityWithTheActiveModule(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{"status": "completed"},
		"module-2": map[string]any{"status": "in_progress"},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	input := server.generatedPlanResearchQueryInput(frameID, "web_research", map[string]any{
		"operation": "search_and_fetch", "query": "作用机制 原始证据", "research_depth": "focused",
	})
	if input["query"] != "POLQ 抑制剂 研发进展" || input["research_depth"] != "systematic" ||
		!stringSliceContains(stringValueSlice(input["query_variants"]), "POLQ inhibitor development pipeline") {
		t.Fatalf("preceding module query leaked into the active module: %#v", input)
	}
}

func TestResearchProcessPreservesFindingDrivenFollowUpAfterSeedCoverage(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{
			"status": "in_progress", "query_languages": []any{"zh", "en"},
			"source_receipts": []any{map[string]any{"tool_call_id": "deep-read-1"}},
		},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	input := server.generatedPlanResearchQueryInput(frameID, "web_research", map[string]any{
		"operation": "search_and_fetch", "query": "BRCA reversion resistance POLQ response",
		"query_variants": []any{"POLQ biomarker response subgroup"}, "research_depth": "focused",
	})
	if input["query"] != "BRCA reversion resistance POLQ response" || input["research_depth"] != "deep" ||
		!stringSliceContains(stringValueSlice(input["query_variants"]), "POLQ biomarker response subgroup") {
		t.Fatalf("finding-driven follow-up was overwritten by the seed query: %#v", input)
	}
}

func TestResearchProcessRestoresOnlyMissingLanguageLane(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{
			"status": "in_progress", "query_languages": []any{"zh"},
			"source_receipts": []any{map[string]any{"tool_call_id": "source-1"}},
		},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	input := server.generatedPlanResearchQueryInput(frameID, "web_research", map[string]any{
		"operation": "search_and_fetch", "query": "model retry", "query_variants": []any{"stale lane"},
	})
	if input["query"] != "mechanism primary evidence" || len(stringValueSlice(input["query_variants"])) != 0 {
		t.Fatalf("missing English lane was not isolated: %#v", input)
	}
}

func TestResearchProcessKeepsModelQueryWhenPlanDidNotPrewriteQueries(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	plan := mapValue(metadata.ContextData["_plan_json"])
	phase := mapValue(anySliceValue(plan["phases"])[0])
	delegation := mapValue(anySliceValue(phase["delegations"])[0])
	step := mapValue(anySliceValue(delegation["steps"])[0])
	delete(step, "discovery_queries")
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	input := server.generatedPlanResearchQueryInput(frameID, "web_research", map[string]any{
		"operation": "search_and_fetch", "query": "model-authored follow-up",
	})
	if input["query"] != "model-authored follow-up" || input["research_depth"] != "deep" {
		t.Fatalf("model-directed research query was replaced or lost its depth: %#v", input)
	}
	if stringValue(mapValue(input["research_session"])["mode"]) != "start" {
		t.Fatalf("initial plan research did not start a resumable source session: %#v", input)
	}

	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, researchToolSchemaForTest()}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: run, toolSchemas: schemas,
	}
	result, err := json.Marshal(map[string]any{
		"status": "in_progress", "source_receipts": []any{},
		"research_transition": map[string]any{"updated_investigation": map[string]any{"kind": "research"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "start-model-query", Name: updateStepStatusToolName}}},
		{Role: "tool", ToolCallID: "start-model-query", Content: string(result)},
	}
	if choice := gateway.RequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("plan selected a scientific executor instead of returning control to the outer model: %#v", choice)
	}
}

func TestResearchProcessExecutesAndReconcilesSourceOwnedContinuation(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	continuation := map[string]any{
		"reason": "research_follow_up_required", "continuation_id": "frontier-2",
		"research_session": map[string]any{"id": "wr-session", "mode": "continue"},
		"next_actions": []any{map[string]any{
			"action": "search_more", "query": "finding-driven follow-up",
		}},
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{
			"status": "in_progress", "title": "Mechanism", "description": "Research mechanism evidence.",
			"research_continuation": continuation,
		},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}

	normalized := server.generatedPlanResearchQueryInput(frameID, "web_search", map[string]any{
		"query": "stale model query",
	})
	if normalized["query"] != "finding-driven follow-up" || normalized["research_session"] != nil {
		t.Fatalf("source continuation was not resumed with its exact frontier: %#v", normalized)
	}

	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {
		Name: "web_search", Capabilities: []string{"search", "source-evidence"},
	}}
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID, taskRun: run}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "record-frontier", Name: updateStepStatusToolName,
		}}},
		{Role: "tool", ToolCallID: "record-frontier", Content: `{"ok":true,"status":"in_progress"}`},
	}
	if choice := mapValue(gateway.generatedPlanRequiredToolChoice(messages, schemas)); choice["name"] != "web_search" {
		t.Fatalf("source-owned continuation did not select its source tool: %#v", choice)
	}
	engine := agentruntime.Engine{Tools: gateway}
	if choice := mapValue(sessionRunnerStatefulInitialToolChoice(engine, messages, schemas)); choice["name"] != "web_search" {
		t.Fatalf("execution-unit boundary dropped the active source continuation: %#v", choice)
	}
	if choice := mapValue(sessionRunnerStatefulInitialToolChoice(
		engine, []agentruntime.Message{{Role: "user", Content: "compacted task"}}, schemas,
	)); choice["name"] != "web_search" {
		t.Fatalf("compacted history dropped durable active-plan state: %#v", choice)
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-orchestrator", Keywords: []string{"research and deliver"},
		RequiredSkills: []string{"mcp-research-source"}, Tools: []string{"skill", "repl", "web_search"},
	})
	server.skillCatalog = catalog
	run.addExecutedSkillNames("mcp-research-source", "research-orchestrator")
	skillSchemas := append([]agentruntime.ToolSchema{{Name: "skill"}, {Name: "repl"}}, schemas...)
	gateway.skillPolicy = runtimeSkillPolicyAuthority{}
	if choice := mapValue(gateway.RequiredToolChoice(messages, skillSchemas)); choice["name"] != "web_search" {
		t.Fatalf("generic Skill routing displaced the source-owned continuation: %#v", choice)
	}
	freshGateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-without-plan", taskRun: &sessionRunnerChatRun{TaskIntent: "new task"},
	}
	if choice := sessionRunnerStatefulInitialToolChoice(
		agentruntime.Engine{Tools: freshGateway}, []agentruntime.Message{{Role: "user", Content: "new task"}}, schemas,
	); choice != nil {
		t.Fatalf("brand-new task received a resumed-state tool constraint: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "follow-frontier", Name: "web_search",
			Arguments: json.RawMessage(`{"query":"finding-driven follow-up"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "follow-frontier", Content: `{"ok":true,"documents":[{"content":"evidence"}]}`},
	)
	if choice := gateway.generatedPlanRequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("executed autonomous continuation forced a status-only round: %#v", choice)
	}
}

func TestResearchProcessExecutesDiscoveredFetchAlternative(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{
			"status": "in_progress", "title": "Mechanism", "description": "Research mechanism evidence.",
			"research_continuation": map[string]any{
				"reason": "research_follow_up_required", "continuation_id": "frontier-fetch",
				"next_actions": []any{map[string]any{
					"action": "fetch", "url": "https://evidence.example/next-record",
				}},
			},
		},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	normalized := server.generatedPlanResearchQueryInput(frameID, "web_fetch", map[string]any{
		"url": "https://model-guessed.example/other",
	})
	if normalized["url"] != "https://evidence.example/next-record" {
		t.Fatalf("discovered fetch route was not preserved exactly: %#v", normalized)
	}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "web_fetch"}}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: &sessionRunnerChatRun{TaskIntent: "Research and deliver"},
	}
	resolved, ok := gateway.ToolCallExecutionIdentity(agentruntime.ToolCall{
		Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://model-guessed.example/other"}`),
	})
	var resolvedInput map[string]any
	if !ok || json.Unmarshal(resolved.Arguments, &resolvedInput) != nil ||
		resolvedInput["url"] != "https://evidence.example/next-record" {
		t.Fatalf("retry guard did not see the source-owned execution identity: ok=%t input=%#v", ok, resolvedInput)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "record-frontier", Name: updateStepStatusToolName}}},
		{Role: "tool", ToolCallID: "record-frontier", Content: `{"ok":true,"status":"in_progress"}`},
	}
	if choice := mapValue(gateway.generatedPlanRequiredToolChoice(messages, schemas)); choice["name"] != "web_fetch" {
		t.Fatalf("discovered source route did not select web_fetch: %#v", choice)
	}
}

func TestResearchProcessDoesNotRepeatStatusAtCompactBoundaryWithoutUnconsumedSource(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, researchToolSchemaForTest()}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: &sessionRunnerChatRun{TaskIntent: "Research and deliver"},
	}
	if _, err := server.executeAgentUpdateStepStatus(context.Background(), frameID, "start-module", map[string]any{
		"step": "module-1", "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	compacted := []agentruntime.Message{{Role: "user", Content: "compacted canonical task"}}
	if choice := gateway.generatedPlanRequiredToolChoice(compacted, schemas); choice != nil {
		t.Fatalf("plan selected a scientific executor at a compact boundary: %#v", choice)
	}

	metadata, found, err = server.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey] = int64(42)
	metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey] = int64(41)
	if _, err := server.workspaceStore.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	choice := mapValue(gateway.generatedPlanRequiredToolChoice(compacted, schemas))
	if choice["name"] != updateStepStatusToolName {
		t.Fatalf("unconsumed durable source result was not reconciled: %#v", choice)
	}
	if _, err := server.executeAgentUpdateStepStatus(context.Background(), frameID, "consume-source-attempt", map[string]any{
		"step": "module-1", "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	metadata, found, err = server.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("consumed metadata found=%t err=%v", found, err)
	}
	if got := int64(numberValue(metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey])); got != 42 {
		t.Fatalf("cursor-only source attempt was not consumed: %d", got)
	}
	if choice := gateway.generatedPlanRequiredToolChoice(compacted, schemas); choice != nil {
		t.Fatalf("consumed source attempt left a plan-owned scientific route: %#v", choice)
	}
}

func TestResearchSourceCheckpointAdvancesDurableReconciliationCursor(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	planInput := revisionPlanInput("Inspect source")
	phase := mapValue(anySliceValue(planInput["phases"])[0])
	delegation := mapValue(anySliceValue(phase["delegations"])[0])
	planStep := mapValue(anySliceValue(delegation["steps"])[0])
	planStep["kind"] = generatedPlanStepKindResearch
	planStep["output_module"] = "source_analysis"
	planStep["research_question"] = "What does the source establish?"
	planStep["research_depth"] = "deep"
	plan := revisePlanForTest(t, fixture, "source-cursor-plan", planInput)
	stepID := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	if _, err := fixture.server.executeAgentUpdateStepStatus(context.Background(), fixture.stream.FrameID, "start-source-step", map[string]any{
		"step": stepID, "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID,
	}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntent: "Research and deliver",
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	call := agentruntime.ToolCall{
		ID: "source-cursor-call", Name: "web_search",
		Arguments: json.RawMessage(`{"query":"mechanism primary evidence"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(options, run, "running", "tool web_search started", call.ID, "start", map[string]any{
		"toolName": call.Name, "toolInput": map[string]any{"query": "mechanism primary evidence"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(options, run, "completed", "tool web_search completed", call.ID, "completed", map[string]any{
		"toolName":   call.Name,
		"toolInput":  map[string]any{"query": "mechanism primary evidence"},
		"toolResult": map[string]any{"ok": true, "sources": []any{map[string]any{"url": "https://example.test/source"}}},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || len(items) != 1 || items[0].TerminalEventID <= 0 {
		t.Fatalf("terminal source event items=%#v err=%v", items, err)
	}
	metadata, found, err := fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	if got := int64(numberValue(metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey])); got != items[0].TerminalEventID {
		t.Fatalf("latest source cursor=%d terminal event=%d", got, items[0].TerminalEventID)
	}
	if _, err := fixture.server.executeAgentUpdateStepStatus(
		withTranscriptRunnerChatRun(context.Background(), run), fixture.stream.FrameID, "reconcile-source", map[string]any{
			"step": stepID, "status": "in_progress",
		},
	); err != nil {
		t.Fatal(err)
	}
	metadata, found, err = fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("reconciled metadata found=%t err=%v", found, err)
	}
	if got := int64(numberValue(metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey])); got != items[0].TerminalEventID {
		t.Fatalf("consumed source cursor=%d terminal event=%d", got, items[0].TerminalEventID)
	}
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, sessionID: fixture.stream.FrameID, taskRun: run,
	}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, researchToolSchemaForTest()}
	choice := mapValue(gateway.generatedPlanRequiredToolChoice(
		[]agentruntime.Message{{Role: "user", Content: "compacted canonical task"}}, schemas,
	))
	if choice["name"] == updateStepStatusToolName {
		t.Fatalf("consumed source cursor re-entered progress reconciliation: %#v", choice)
	}
}

func TestResearchFailedSourceCheckpointAdvancesCursorButPreflightDoesNot(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	planInput := revisionPlanInput("Inspect failing source")
	phase := mapValue(anySliceValue(planInput["phases"])[0])
	delegation := mapValue(anySliceValue(phase["delegations"])[0])
	planStep := mapValue(anySliceValue(delegation["steps"])[0])
	planStep["kind"] = generatedPlanStepKindResearch
	planStep["output_module"] = "source_analysis"
	planStep["research_question"] = "What does the source establish?"
	planStep["research_depth"] = "deep"
	plan := revisePlanForTest(t, fixture, "failed-source-cursor-plan", planInput)
	stepID := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	if _, err := fixture.server.executeAgentUpdateStepStatus(context.Background(), fixture.stream.FrameID, "start-failed-source-step", map[string]any{
		"step": stepID, "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	failedCall := agentruntime.ToolCall{
		ID: "failed-source-call", Name: "web_research", Arguments: json.RawMessage(`{"query":"primary evidence"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{failedCall}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(options, run, "running", "tool started", failedCall.ID, "start", map[string]any{
		"toolName": failedCall.Name, "toolInput": map[string]any{"query": "primary evidence"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(options, run, "failed", "upstream reset", failedCall.ID, "failed", map[string]any{
		"toolName": failedCall.Name, "toolInput": map[string]any{"query": "primary evidence"},
		"toolResult": map[string]any{"ok": false, "error": map[string]any{"code": "upstream_reset"}},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, run.ToolBatchIDs[failedCall.ID])
	if err != nil || len(items) != 1 || items[0].TerminalEventID <= 0 {
		t.Fatalf("failed source terminal items=%#v err=%v", items, err)
	}
	metadata, found, err := fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("failed source metadata found=%t err=%v", found, err)
	}
	want := items[0].TerminalEventID
	if got := int64(numberValue(metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey])); got != want {
		t.Fatalf("failed source cursor=%d terminal event=%d", got, want)
	}

	preflightCall := agentruntime.ToolCall{
		ID: "preflight-source-call", Name: "web_research", Arguments: json.RawMessage(`{"query":"blocked before execution"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{preflightCall}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(options, run, "failed", "preflight required", preflightCall.ID, prestartToolFailurePhase, map[string]any{
		"toolName": preflightCall.Name, "toolInput": map[string]any{"query": "blocked before execution"},
		"toolResult": map[string]any{
			"ok": false, "executed": false, "status": "network_preflight_required", "message": "approval required",
		},
		"rejectedBeforeExecution": true,
	}); err != nil {
		t.Fatal(err)
	}
	metadata, found, err = fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("preflight metadata found=%t err=%v", found, err)
	}
	if got := int64(numberValue(metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey])); got != want {
		t.Fatalf("non-executing preflight advanced source cursor=%d want=%d", got, want)
	}
}

func TestResearchSourceCursorDoesNotCrossPlanVersions(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	first := revisePlanForTest(t, fixture, "cursor-plan-first", revisionPlanInput("Initial module"))
	metadata, found, err := fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey] = int64(99)
	metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey] = int64(98)
	if _, err := fixture.store.SetFrameRuntimeMetadata(fixture.stream.FrameID, metadata); err != nil {
		t.Fatal(err)
	}
	second := revisePlanForTest(t, fixture, "cursor-plan-second", revisionPlanInput("Replacement module"))
	if second["version_id"] == first["version_id"] {
		t.Fatal("changed plan did not create a new immutable version")
	}
	metadata, found, err = fixture.store.GetFrameRuntimeMetadata(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("revised metadata found=%t err=%v", found, err)
	}
	if _, present := metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey]; present {
		t.Fatalf("revised plan retained latest source cursor: %#v", metadata.ContextData)
	}
	if _, present := metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey]; present {
		t.Fatalf("revised plan retained consumed source cursor: %#v", metadata.ContextData)
	}
}

func TestResearchProcessDoesNotPromoteCompoundResearchAfterModuleStart(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	server.skillCatalog = skills.NewCatalog()
	server.skillCatalog.AddSkill(skills.Skill{
		Name: "source-workflow", Tools: []string{"web_research"},
	})
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: run,
		toolSchemas: []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {
			Name: "web_research", Capabilities: []string{"research"},
			Exposure: agentruntime.ToolExposureDeferred,
		}},
	}
	if additions := gateway.AdditionalModelToolSchemas([]agentruntime.ToolSchema{{Name: updateStepStatusToolName}}); len(additions) != 0 {
		t.Fatalf("plan state independently promoted a deferred research Tool: %#v", additions)
	}
	run.addExecutedSkillNames("source-workflow")
	additions := gateway.AdditionalModelToolSchemas([]agentruntime.ToolSchema{{Name: updateStepStatusToolName}})
	if agentRuntimeToolSchemaNamed(additions, "web_research") {
		t.Fatalf("selected Skill promoted registered hidden web_research: %#v", additions)
	}
	result, err := json.Marshal(map[string]any{
		"status": "in_progress", "source_receipts": []any{},
		"research_transition": map[string]any{"updated_investigation": map[string]any{"kind": "research"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "start-module", Name: updateStepStatusToolName}}},
		{Role: "tool", ToolCallID: "start-module", Content: string(result)},
	}
	if choice := gateway.RequiredToolChoice(messages, additions); choice != nil {
		t.Fatalf("module start selected a compound scientific executor: %#v", choice)
	}
}

func TestResearchProcessReconcilesUnconsumedSourceBeforeSkillConnectorHandoff(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData[generatedPlanResearchLatestSourceEventIDKey] = int64(42)
	metadata.ContextData[generatedPlanResearchConsumedSourceEventIDKey] = int64(41)
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-orchestrator", Keywords: []string{"research and deliver"},
		RequiredSkills: []string{"mcp-research-source"}, Tools: []string{"skill", "repl", "web_research"},
	})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	run.addExecutedSkillNames("mcp-research-source", "research-orchestrator")
	tools := []agentruntime.ToolSchema{
		{Name: "skill"}, {Name: "repl"}, researchToolSchemaForTest(), {Name: updateStepStatusToolName},
		{Name: "mcp__research-source__search"},
	}
	if choice := mapValue(server.generatedPlanResearchReconciliationToolChoice(frameID, tools)); choice["name"] != updateStepStatusToolName {
		t.Fatalf("durable source cursor did not expose its control transition: %#v", choice)
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: run, toolSchemas: tools,
		skillPolicy: runtimeSkillPolicyAuthority{},
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "later-edit", Name: "edit_file"}}},
		{Role: "tool", ToolCallID: "later-edit", Content: `{"ok":true,"changed":true}`},
	}
	choice := mapValue(gateway.RequiredToolChoice(messages, tools))
	if choice["name"] != updateStepStatusToolName {
		t.Fatalf("Skill connector bypassed unconsumed plan evidence: %#v", choice)
	}
}

func TestResearchProcessContinuesAfterGenericResearchSkillActivation(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-orchestrator", Keywords: []string{"research and deliver"},
		RequiredSkills: []string{"mcp-research-source"},
		Tools:          []string{"search_skills", "skill", "repl", "web_search", "web_fetch", "fetch_article_fulltext", "patent_search", "save_artifacts"},
	})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	schemas := []agentruntime.ToolSchema{
		{Name: "search_skills"}, {Name: "skill"}, {Name: "repl"}, {Name: "web_search"},
		{Name: "web_fetch"}, {Name: "fetch_article_fulltext"},
		{Name: "patent_search"}, {Name: "save_artifacts"}, {Name: updateStepStatusToolName},
		{Name: "mcp__research-source__search"},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID, taskRun: run, toolSchemas: schemas,
		skillPolicy: runtimeSkillPolicyAuthority{},
	}
	if choice := gateway.RequiredToolChoice(nil, schemas); choice != nil {
		t.Fatalf("autonomous pending step forced a control transition before model action: %#v", choice)
	}
	normalized := gateway.normalizeAdmittedToolArguments("skill", map[string]any{"skill": "unrelated-skill"})
	if normalized["skill"] != "unrelated-skill" {
		t.Fatalf("runtime rewrote a model-owned Skill choice: %#v", normalized)
	}
	run.addExecutedSkillNames("mcp-research-source", "research-orchestrator")
	result, err := json.Marshal(map[string]any{"status": "in_progress", "source_receipts": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "start-module", Name: updateStepStatusToolName}}},
		{Role: "tool", ToolCallID: "start-module", Content: string(result)},
	}
	if choice := gateway.RequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("loaded Skill forced an autonomous pending-step transition: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "mcp-source", Name: "repl", Arguments: json.RawMessage(`{"code":"result = host.mcp('research-source','search',{'query':'evidence'})"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "mcp-source", Content: `{"ok":true,"stdout":"source result"}`},
	)
	if choice := gateway.RequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("connector execution forced an autonomous pending-step transition: %#v", choice)
	}
	normalizedProgress := gateway.normalizeAdmittedToolArguments(updateStepStatusToolName, map[string]any{
		"step": "stale-step", "status": "in_progress",
	})
	if normalizedProgress["step"] != "module-1" {
		t.Fatalf("pending module control identity was not normalized: %#v", normalizedProgress)
	}
	if _, err := server.executeAgentUpdateStepStatus(context.Background(), frameID, "start-research", normalizedProgress); err != nil {
		t.Fatal(err)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "start-research", Name: updateStepStatusToolName,
			Arguments: json.RawMessage(`{"step":"module-1","status":"in_progress"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "start-research", Content: string(result)},
	)
	if choice := gateway.RequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("activated module retained a plan-owned scientific executor: %#v", choice)
	}
}

func TestResearchProcessFailsOpenWhenActivatedSkillDependencyIsUnavailable(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-orchestrator", Keywords: []string{"research and deliver"},
		RequiredSkills: []string{"mcp-missing"}, Tools: []string{"skill", "web_search"},
	})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	schemas := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "web_search"}}
	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID, taskRun: run, toolSchemas: schemas}
	if choice := gateway.RequiredToolChoice(nil, schemas); choice != nil {
		t.Fatalf("unavailable optional Skill dependency selected a scientific executor: %#v", choice)
	}
}

func TestResearchProcessGenericMCPHandoffDoesNotRequirePlan(t *testing.T) {
	server, _, _ := newGeneratedPlanProcessFixture(t)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-orchestrator", Keywords: []string{"research and deliver"},
		RequiredSkills: []string{"mcp-research-source"},
		Tools:          []string{"skill", "repl", "web_search", "web_fetch"},
	})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{TaskIntent: "Research and deliver"}
	schemas := []agentruntime.ToolSchema{
		{Name: "skill"}, {Name: "repl"}, {Name: "web_search"}, {Name: "web_fetch"},
		{Name: "mcp__research-source__search"},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-without-plan", taskRun: run, toolSchemas: schemas,
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "broad-discovery", Name: "web_search",
			Arguments: json.RawMessage(`{"query":"POLQ","query_variants":["POLQ inhibitor","POLQ 抑制剂"]}`),
		}}},
		{Role: "tool", ToolCallID: "broad-discovery", Content: `{"ok":true,"result":{"sources":[{"url":"https://example.test"}]}}`},
	}
	if choice := gateway.RequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("planless task received a forced Skill or source route: %#v", choice)
	}

	run.addExecutedSkillNames("mcp-research-source", "research-orchestrator")
	skillMessages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "research-skill", Name: "skill", Arguments: json.RawMessage(`{"skill":"research-orchestrator"}`),
		}}},
		{Role: "tool", ToolCallID: "research-skill", Content: "loaded research connector contract"},
	}
	if choice := gateway.RequiredToolChoice(skillMessages, schemas); choice != nil {
		t.Fatalf("loaded Skill forced its connector instead of returning to the outer model: %#v", choice)
	}
	skillMessages = append(skillMessages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "research-mcp", Name: "repl", Arguments: json.RawMessage(`{"code":"result = host.mcp('research-source','search',{'query':'evidence'})"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "research-mcp", Content: `{"ok":true}`},
	)
	if choice := gateway.RequiredToolChoice(skillMessages, schemas); choice != nil {
		t.Fatalf("completed connector forced a second source route: %#v", choice)
	}
}

func TestResearchProcessPlanBookkeepingCannotRejectArtifactPublication(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	input := map[string]any{
		"files": []any{"report.md", "evidence.csv"},
		"destination": map[string]any{
			"report.md":    "snapshot",
			"evidence.csv": "snapshot",
		},
	}
	call := agentruntime.ToolCall{
		ID: "publish", Name: "save_artifacts",
		Arguments: json.RawMessage(`{"files":["report.md","evidence.csv"],"destination":{"report.md":"snapshot","evidence.csv":"snapshot"}}`),
	}
	execution := &serverAgentRuntimeGatewayExecution{
		gateway: serverAgentRuntimeToolGateway{server: server, sessionID: frameID},
		call:    call,
	}
	invocation := toolgateway.NewInvocation(context.Background(), call.ID, call.Name, call.Arguments, execution)
	invocation.CanonicalName = call.Name
	invocation.Input = input
	serverAgentRuntimeGatewayPreflight(invocation)
	if invocation.ContinuationState() != toolgateway.Continue {
		t.Fatalf("plan bookkeeping rejected first artifact publication: status=%q value=%#v", invocation.Status, invocation.Value)
	}
}

func TestResearchProcessPreservesExplicitSnapshotsBeforeAndDuringDelivery(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	input := map[string]any{
		"files": []any{"report.md", "evidence.csv"}, "language": "text",
		"human_description": "Saving report and evidence",
		"destination":       map[string]any{"report.md": "snapshot", "evidence.csv": "snapshot"},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{TaskIntent: "Generate report.md and evidence.csv"},
	}
	staged := gateway.normalizeAdmittedToolArguments("save_artifacts", input)
	for _, file := range []string{"report.md", "evidence.csv"} {
		if mode := stringValue(mapValue(staged["destination"])[file]); mode != "snapshot" {
			t.Fatalf("pre-delivery %s snapshot was rewritten as %q input=%#v", file, mode, staged)
		}
	}
	if stringValue(mapValue(input["destination"])["report.md"]) != "snapshot" {
		t.Fatalf("staging mutated the provider audit input: %#v", input)
	}

	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1": map[string]any{"status": "completed"},
		"module-2": map[string]any{"status": "completed"},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	delivery := gateway.normalizeAdmittedToolArguments("save_artifacts", input)
	if mode := stringValue(mapValue(delivery["destination"])["report.md"]); mode != "snapshot" {
		t.Fatalf("delivery snapshot was not preserved: %#v", delivery)
	}
}

func TestResearchSaveNormalizationCannotDemoteExplicitDeliverables(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: frameID,
		taskRun: &sessionRunnerChatRun{TaskIntent: "生成一份专业报告和证据表。"},
	}
	normalized := gateway.normalizeAdmittedToolArguments("save_artifacts", map[string]any{
		"files": []any{"report.md", "evidence.csv"},
		"destination": map[string]any{
			"report.md": "snapshot", "evidence.csv": "snapshot",
		},
	})
	for _, file := range []string{"report.md", "evidence.csv"} {
		if mode := stringValue(mapValue(normalized["destination"])[file]); mode != "snapshot" {
			t.Fatalf("research deliverable %s was demoted: %#v", file, normalized)
		}
	}
}

func TestDeliveryStepPublishesArtifactsPreparedBeforeDelivery(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	metadata.ContextData["_step_statuses"] = map[string]any{
		"module-1":   map[string]any{"status": "completed"},
		"module-2":   map[string]any{"status": "completed"},
		"delivery-1": map[string]any{"status": "in_progress"},
	}
	metadata.ContextData["_plan_approved"] = true
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: frameID, taskRun: &sessionRunnerChatRun{TaskIntent: "Research and deliver"}}
	schemas := []agentruntime.ToolSchema{{Name: updateStepStatusToolName}, {Name: "save_artifacts"}}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "prepare-artifacts", Name: "save_artifacts",
			Arguments: json.RawMessage(`{"files":["report.md","evidence.csv"]}`),
		}}},
		{Role: "tool", ToolCallID: "prepare-artifacts", Content: `{"ok":true}`},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "start-delivery", Name: updateStepStatusToolName,
			Arguments: json.RawMessage(`{"step":"delivery-1","status":"in_progress"}`),
		}}},
		{Role: "tool", ToolCallID: "start-delivery", Content: `{"ok":true,"step":"delivery-1","status":"in_progress"}`},
	}
	if choice := gateway.generatedPlanRequiredToolChoice(messages, schemas); choice != nil {
		t.Fatalf("delivery did not receive an authoring turn after synthesis handoff: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "revise-artifacts", Name: "edit_file",
			Arguments: json.RawMessage(`{"file_path":"report.md","old_string":"draft","new_string":"grounded report"}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "revise-artifacts", Content: `{"ok":true}`},
	)
	if choice := mapValue(gateway.generatedPlanRequiredToolChoice(messages, schemas)); choice["name"] != "save_artifacts" {
		t.Fatalf("delivery revision did not advance to publication: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "publish-artifacts", Name: "save_artifacts",
			Arguments: json.RawMessage(`{"files":["report.md","evidence.csv"]}`),
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "publish-artifacts", Content: `{"ok":true}`},
	)
	if choice := mapValue(gateway.generatedPlanRequiredToolChoice(messages, schemas)); choice["name"] != updateStepStatusToolName {
		t.Fatalf("published delivery was not returned to plan completion: %#v", choice)
	}
}

func TestAutonomousPlanCompletionDoesNotGateFinalOutput(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	remaining, err := server.incompleteGeneratedPlanStepTitles(frameID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("autonomous navigation became a completion gate: %v", remaining)
	}
}

func TestCompactSummaryCarriesDurableResearchNavigationOnce(t *testing.T) {
	server, _, frameID := newGeneratedPlanProcessFixture(t)
	summary := server.compactSummaryWithPlanNavigation(frameID, "Compact session summary")
	for _, expected := range []string{generatedPlanResearchNavigationSchema, "module-1", "Mechanism"} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("compact summary lost %q: %s", expected, summary)
		}
	}
	again := server.compactSummaryWithPlanNavigation(frameID, summary)
	if strings.Count(again, generatedPlanResearchNavigationSchema) != 1 {
		t.Fatalf("compact summary duplicated research navigation: %s", again)
	}
}
