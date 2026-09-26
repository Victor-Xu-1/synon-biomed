package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func appendAdaptiveToolReceipt(t *testing.T, f *agentSaveArtifactsFixture, id, tool string, result map[string]any) {
	t.Helper()
	appendAdaptiveToolReceiptInput(t, f, id, tool, map[string]any{}, result)
}

func appendAdaptiveToolReceiptInput(t *testing.T, f *agentSaveArtifactsFixture, id, tool string, input, result map[string]any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": "running", "toolCallId": id, "toolName": tool,
		"toolPhase": "completed", "lifecyclePhase": "tool",
		"toolInput": input, "toolResult": result,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, created, err := f.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: f.claim, ClientMessageID: "adaptive-receipt-" + id,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload, Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("append receipt %s: created=%t error=%v", id, created, err)
	}
}

func adaptiveStageOption(label, outcome string, recommended bool) map[string]any {
	return map[string]any{
		"label": label, "description": outcome, "pros": "Preserves the current result.",
		"cons": "Requires a route choice.", "readiness": "Current task context only.",
		"readiness_status": "unverified", "decision_evidence": []any{"user-input:current-task"},
		"readiness_evidence": []any{}, "selection_basis": "user_objective",
		"expected_outcome": outcome, "selection_rationale": outcome,
		"recommended": recommended,
	}
}

func TestAutonomousCompletionRetainsUnfinishedPlanObligations(t *testing.T) {
	for _, status := range []string{"pending", "in_progress", "blocked", "skipped"} {
		t.Run(status, func(t *testing.T) {
			f := newAgentSaveArtifactsFixture(t)
			plan := revisePlanForTest(t, f, "adaptive-plan", revisionPlanInput("Inspect input", "Run analysis"))
			steps := anySliceValue(plan["steps"])
			first := stringValue(mapValue(steps[0])["id"])
			second := stringValue(mapValue(steps[1])["id"])
			if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "input-ready", map[string]any{
				"step": first, "status": "completed", "notes": "Input inspected; computation remains.",
			}); err != nil {
				t.Fatal(err)
			}
			if status != "pending" {
				if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "analysis-state", map[string]any{
					"step": second, "status": status, "notes": "No successful analysis result yet.",
				}); err != nil {
					t.Fatal(err)
				}
			}
			remaining, err := f.server.incompleteGeneratedPlanCondition(f.stream.SessionID)
			if err != nil || remaining == nil || len(remaining.condition.Steps) != 1 || remaining.condition.Steps[0].ID != second {
				t.Fatalf("%s work was accepted as whole-task completion: remaining=%#v error=%v", status, remaining, err)
			}
			correction := remaining.runnerCorrection()
			if !strings.Contains(correction.Detail, "execute") || strings.Contains(correction.Detail, "mark each completed, blocked, or skipped") {
				t.Fatalf("correction requested bookkeeping instead of substantive continuation: %q", correction.Detail)
			}
		})
	}
}

func TestAdaptiveCompletionPreservesShortReasoningTasks(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	if remaining, err := f.server.incompleteGeneratedPlanCondition(f.stream.SessionID); err != nil || remaining != nil {
		t.Fatalf("a task without a plan was forced to create one: %#v %v", remaining, err)
	}
	plan := revisePlanForTest(t, f, "short-plan", revisionPlanInput("Explain the supplied example"))
	step := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "short-done", map[string]any{
		"step": step, "status": "completed", "notes": "Explanation derived from the user's supplied example.",
	}); err != nil {
		t.Fatal(err)
	}
	if remaining, err := f.server.incompleteGeneratedPlanCondition(f.stream.SessionID); err != nil || remaining != nil {
		t.Fatalf("completed reasoning work acquired a tool quota: %#v %v", remaining, err)
	}
}

func TestPlanProgressRejectsAStaleTaskInput(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	plan := revisePlanForTest(t, f, "scoped-plan", revisionPlanInput("Inspect current input"))
	step := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	ctx, run := appendLargeToolResultSource(t, f, "foreign-progress", updateStepStatusToolName)
	run.TaskIntentID = "different-logical-input"
	if _, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "foreign-progress", map[string]any{
		"step": step, "status": "completed",
	}); err == nil || !strings.Contains(err.Error(), "different task input") {
		t.Fatalf("stale task updated another plan: %v", err)
	}
	if stage := f.server.generatedPlanStageProgressSnapshot(f.stream.SessionID, run); stage != nil {
		t.Fatalf("stale task inherited stage progress: %#v", stage)
	}
}

func TestExecutionStepRejectsUnstructuredToolIdentity(t *testing.T) {
	input := revisionPlanInput("Run analysis")
	step := mapValue(anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(input["phases"])[0])["delegations"])[0])["steps"])[0])
	step["kind"] = generatedPlanStepKindExecution
	step["execution_tool"] = "python\nforge-result"
	if _, _, _, err := normalizeGeneratedPlan(input); err == nil || !strings.Contains(err.Error(), "execution_tool") {
		t.Fatalf("malformed tool identity entered a durable plan: %v", err)
	}
}

func TestExecutionPlanStepRequiresScopedSuccessfulToolReceipt(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Run computation")
	stepInput := mapValue(anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(input["phases"])[0])["delegations"])[0])["steps"])[0])
	stepInput["kind"] = generatedPlanStepKindExecution
	stepInput["execution_tool"] = "python"
	plan := revisePlanForTest(t, f, "execution-plan", input)
	step := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	appendAdaptiveToolReceipt(t, f, "old-compute", "python", map[string]any{
		"ok": true, "executed": true, "exit_status": "ok", "exit_code": 0,
	})
	ctx, _ := appendLargeToolResultSource(t, f, "start-execution-step", updateStepStatusToolName)
	if _, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "start-execution-step", map[string]any{
		"step": step, "status": "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	appendAdaptiveToolReceipt(t, f, "status-started", updateStepStatusToolName, map[string]any{
		"ok": true, "step": step, "status": "in_progress", "applied": true,
	})
	assertPending := func(ref string) {
		t.Helper()
		result, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "finish-execution-step", map[string]any{
			"step": step, "status": "completed", "execution_ref": ref,
		})
		if err != nil || mapValue(result)["status"] != "in_progress" || mapValue(result)["applied"] != false {
			t.Fatalf("unverified execution %q completed: result=%#v error=%v", ref, result, err)
		}
	}
	assertPending("")
	assertPending("old-compute")
	appendAdaptiveToolReceipt(t, f, "preflight", "python", map[string]any{
		"ok": false, "executed": false, "status": "code_preflight_required", "message": "Input missing.",
	})
	assertPending("preflight")
	appendAdaptiveToolReceipt(t, f, "failed-compute", "python", map[string]any{
		"ok": false, "executed": true, "exit_status": "error", "exit_code": 2,
	})
	assertPending("failed-compute")
	appendAdaptiveToolReceipt(t, f, "wrong-tool", "web_fetch", map[string]any{
		"ok": true, "executed": true, "result": "downloaded input",
	})
	assertPending("wrong-tool")
	appendAdaptiveToolReceipt(t, f, "still-running", "python", map[string]any{
		"ok": true, "executed": true, "status": "running", "result": map[string]any{"job_id": "job-1"},
	})
	assertPending("still-running")
	appendAdaptiveToolReceipt(t, f, "nested-failure", "python", map[string]any{
		"ok": true, "executed": true, "result": map[string]any{"exit_code": 2},
	})
	assertPending("nested-failure")
	appendAdaptiveToolReceipt(t, f, "computed", "python", map[string]any{
		"ok": true, "executed": true, "exit_status": "ok", "exit_code": 0,
		"stdout": "computed result",
	})
	result, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "finish-execution-step", map[string]any{
		"step": step, "status": "completed", "execution_ref": "computed",
	})
	if err != nil || mapValue(result)["status"] != "completed" || mapValue(result)["applied"] == false {
		t.Fatalf("valid execution receipt was rejected: %#v error=%v", result, err)
	}
	if remaining, err := f.server.incompleteGeneratedPlanCondition(f.stream.SessionID); err != nil || remaining != nil {
		t.Fatalf("completed execution stayed open: %#v %v", remaining, err)
	}
}

func TestExecutionPlanRecognizesUniqueReceiptWithoutStatusOnlyRound(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Run computation", "Run a different computation")
	for _, raw := range anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(input["phases"])[0])["delegations"])[0])["steps"]) {
		step := mapValue(raw)
		step["kind"], step["execution_tool"] = generatedPlanStepKindExecution, "python"
	}
	plan := revisePlanForTest(t, f, "receipt-plan", input)
	steps := anySliceValue(plan["steps"])
	first, second := stringValue(mapValue(steps[0])["id"]), stringValue(mapValue(steps[1])["id"])
	appendAdaptiveToolReceipt(t, f, "actual-computation", "python", map[string]any{"ok": true, "exit_status": "ok", "exit_code": 0})
	ctx, _ := appendLargeToolResultSource(t, f, "record-result", updateStepStatusToolName)
	result, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "record-result", map[string]any{"step": first, "status": "completed"})
	if err != nil || mapValue(result)["status"] != "completed" || mapValue(result)["execution_ref"] != "actual-computation" {
		t.Fatalf("the unique successful scoped receipt was not recognized: %#v %v", result, err)
	}
	again, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "record-result-again", map[string]any{"step": first, "status": "completed"})
	if err != nil || mapValue(again)["status"] != "completed" || mapValue(again)["idempotent"] != true {
		t.Fatalf("replaying a completed step lost its receipt: %#v %v", again, err)
	}
	other, err := f.server.executeAgentUpdateStepStatus(ctx, f.stream.SessionID, "borrow-result", map[string]any{"step": second, "status": "completed", "execution_ref": "actual-computation"})
	if err != nil || mapValue(other)["status"] == "completed" {
		t.Fatalf("another computation borrowed the first step's receipt: %#v %v", other, err)
	}
}

func TestGeneratedPlanRejectsInventedExecutionToolBeforePersistence(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	input := revisionPlanInput("Run calculation")
	step := mapValue(anySliceValue(mapValue(anySliceValue(mapValue(anySliceValue(input["phases"])[0])["delegations"])[0])["steps"])[0])
	step["kind"], step["execution_tool"] = generatedPlanStepKindExecution, "imagined_executor"
	ctx, run := appendLargeToolResultSource(t, f, "invalid-plan-tool", generatePlanToolName)
	run.AutonomousPlanning = true
	run.setToolCapabilityCatalog([]agentruntime.ToolSchema{{Name: "python"}})
	if _, err := f.server.executeAgentGeneratePlan(ctx, f.stream.SessionID, "invalid-plan-tool", input); err == nil || !strings.Contains(err.Error(), "execution_tool") {
		t.Fatalf("an invented executor became a durable plan obligation: %v", err)
	}
}

func TestStageProgressUsesCurrentPlanWithoutDeclaringWholeTaskComplete(t *testing.T) {
	server, store, frameID := newGeneratedPlanProcessFixture(t)
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	statuses := mapValue(metadata.ContextData["_step_statuses"])
	statuses["module-1"] = map[string]any{"status": "completed"}
	statuses["module-2"] = map[string]any{"status": "blocked"}
	metadata.ContextData["_step_statuses"] = statuses
	if _, err := store.SetFrameRuntimeMetadata(frameID, metadata); err != nil {
		t.Fatal(err)
	}
	progress := server.generatedPlanStageProgressSnapshot(frameID, nil)
	if progress == nil || progress.Schema != "synon.plan_stage_progress.v1" ||
		progress.CompletedCount != 1 || progress.RemainingCount != 2 ||
		progress.CompletedSteps[0].ID != "module-1" || progress.RemainingSteps[0].Status != "blocked" {
		t.Fatalf("stage snapshot hid unfinished work: %#v", progress)
	}
	if remaining, err := server.incompleteGeneratedPlanCondition(frameID); err != nil || remaining == nil || len(remaining.condition.Steps) != 2 {
		t.Fatalf("stage discussion settled the whole task: %#v %v", remaining, err)
	}
	encoded, err := json.Marshal([]askUserQuestion{{Question: "Which direction?", StageProgress: progress}})
	if err != nil || !strings.Contains(string(encoded), `"stage_progress"`) || !strings.Contains(string(encoded), `"remaining_count":2`) {
		t.Fatalf("stage progress did not survive the existing question protocol: %s %v", encoded, err)
	}
}

func TestAskUserPauseCarriesStageProgressInExistingQuestion(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	plan := revisePlanForTest(t, f, "stage-plan", revisionPlanInput("Inspect evidence", "Choose next computation"))
	step := stringValue(mapValue(anySliceValue(plan["steps"])[0])["id"])
	if _, err := f.server.executeAgentUpdateStepStatus(context.Background(), f.stream.SessionID, "record-stage", map[string]any{
		"step": step, "status": "completed", "notes": "Evidence inspected.",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, _ := appendLargeToolResultSource(t, f, "stage-decision", "ask_user")
	_, err := f.server.executeAgentAskUserQuestion(ctx, f.stream.SessionID, "stage-decision", "ask_user", map[string]any{
		"question": "Which computation should be run next?", "header": "Next route",
		"options": []any{
			adaptiveStageOption("Continue", "Continue the current research route.", true),
			adaptiveStageOption("Revise", "Revise the research route before computing.", false),
		},
	})
	var pause *agentruntime.PauseError
	if !errors.As(err, &pause) {
		t.Fatalf("stage choice did not pause on the existing question authority: %v", err)
	}
	questions := anySliceValue(pause.Data["questions"])
	stage := mapValue(mapValue(questions[0])["stage_progress"])
	if stage["schema"] != "synon.plan_stage_progress.v1" || numberValue(stage["remaining_count"]) != 1 ||
		stringValue(mapValue(anySliceValue(stage["remaining_steps"])[0])["title"]) != "Choose next computation" {
		t.Fatalf("AskUser pause omitted the server plan snapshot: %#v", stage)
	}
}

func TestAskUserStageProgressPersistsThroughRealRunnerPause(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "adaptive-stage-pause-frame"
	seedTranscriptWebFrame(t, store, "local", "adaptive-stage-project", frameID)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "stage-intent-message", ClientMessageID: "stage-intent",
		Text: "Inspect the supplied evidence and ask me to choose the next computation.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", frameID)
	plan := revisionPlanInput("Inspect evidence", "Choose next computation")
	question := map[string]any{
		"question": "Which computation should be run next?", "header": "Next route",
		"options": []any{
			adaptiveStageOption("Continue", "Continue the current route.", true),
			adaptiveStageOption("Revise", "Revise the route before computing.", false),
		},
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "I will inspect the evidence."},
			}}})
			return
		}
		var callID, toolName string
		var arguments string
		switch requests.Add(1) {
		case 1:
			callID, toolName, arguments = "stage-plan", generatePlanToolName, mustJSON(t, plan)
		case 2:
			callID, toolName = "stage-progress", updateStepStatusToolName
			arguments = `{"step":"Inspect evidence","status":"completed","notes":"Inspected supplied evidence."}`
		case 3:
			callID, toolName, arguments = "stage-choice", "ask_user", mustJSON(t, question)
		case 4:
			callID, toolName = "stage-finished", updateStepStatusToolName
			arguments = `{"step":"Choose next computation","status":"completed","notes":"Continued after the user's route choice."}`
		case 5:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "The requested analysis and route decision are complete."},
			}}})
			return
		default:
			http.Error(w, "unexpected provider round", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": callID, "type": "function", "function": map[string]any{
					"name": toolName, "arguments": arguments,
				},
			}}},
		}}})
	}))
	defer provider.Close()
	options := SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "adaptive-stage-runner", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "local-test", Model: "local-test", AllowedTools: []string{generatePlanToolName, updateStepStatusToolName, "ask_user"},
		LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 5, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	}
	paused, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || paused.Status != "awaiting_user_response" || requests.Load() != 3 {
		t.Fatalf("stage discussion did not park via runner: %#v requests=%d error=%v", paused, requests.Load(), err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("read durable stage question: found=%t error=%v", found, err)
	}
	pending := compatibilityServerPendingInputs(metadata.ContextData)
	if len(pending) != 1 {
		t.Fatalf("stage question disappeared from durable input: %#v", pending)
	}
	questions := anySliceValue(pending[0]["questions"])
	if len(questions) != 1 {
		t.Fatalf("stage question was not durably normalized: %#v", pending[0])
	}
	stage := mapValue(mapValue(questions[0])["stage_progress"])
	if stage["schema"] != "synon.plan_stage_progress.v1" || numberValue(stage["completed_count"]) != 1 ||
		numberValue(stage["remaining_count"]) != 1 ||
		stringValue(mapValue(anySliceValue(stage["remaining_steps"])[0])["title"]) != "Choose next computation" {
		t.Fatalf("durable AskUser stage scope is wrong: %#v", stage)
	}
	resolved := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/frames/"+frameID+"/resolve-input", "local", map[string]any{
			"responses": []any{map[string]any{
				"tool_id": "stage-choice", "action": "answer",
				"answers": map[string]any{"Which computation should be run next?": "Continue"},
			}},
		}, http.StatusOK)
	if resolved["status"] != "accepted" {
		t.Fatalf("stage discussion answer was rejected: %#v", resolved)
	}
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "adaptive-stage-resume", ClaimTTL: time.Second, Chat: options,
	})
	if err != nil || resumed.Status != "completed" || requests.Load() != 5 {
		t.Fatalf("stage discussion did not resume the same plan: %#v requests=%d error=%v", resumed, requests.Load(), err)
	}
	if remaining, err := server.incompleteGeneratedPlanCondition(frameID); err != nil || remaining != nil {
		t.Fatalf("resumed plan retained false unfinished work: %#v error=%v", remaining, err)
	}
}

func TestAutonomousFalseFinalResumesSamePlanAndCompletes(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "adaptive-continuation-frame"
	seedTranscriptWebFrame(t, store, "local", "adaptive-continuation-project", frameID)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "adaptive-intent-message", ClientMessageID: "adaptive-intent",
		Text: "Inspect the supplied evidence, then provide the result.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", frameID)
	plan := revisionPlanInput("Analyze supplied evidence")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "I will check the supplied evidence."},
			}}})
			return
		}
		var message map[string]any
		switch requests.Add(1) {
		case 1:
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "adaptive-plan-call", "type": "function",
				"function": map[string]any{"name": generatePlanToolName, "arguments": string(mustJSON(t, plan))},
			}}}
		case 2:
			message = map[string]any{"role": "assistant", "content": "Progress noted; analysis will follow later."}
		case 3:
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "adaptive-progress-call", "type": "function",
				"function": map[string]any{"name": updateStepStatusToolName,
					"arguments": `{"step":"Analyze supplied evidence","status":"completed","notes":"Evidence analyzed."}`},
			}}}
		case 4:
			message = map[string]any{"role": "assistant", "content": "The requested analysis is complete."}
		default:
			http.Error(w, "unexpected provider round", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer provider.Close()
	options := SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "adaptive-first", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "local-test", Model: "local-test", AllowedTools: []string{generatePlanToolName, updateStepStatusToolName},
		LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 3, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	}
	first, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || first.Status != "interrupted" || first.InterruptionReasonCode != sessionRunnerPlanStepsIncompleteReasonCode ||
		!first.InterruptionAutoResume || requests.Load() != 2 {
		t.Fatalf("false final settled the unfinished plan: %#v requests=%d error=%v", first, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatal(err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("unfinished work had no continuation checkpoint: %#v %v", checkpoint, err)
	}
	options.RunnerID = "adaptive-second"
	options.TranscriptResumeSource = transcriptstore.ResumeSourceCheckpoint
	options.TranscriptCheckpoint = checkpoint.Sequence
	second, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || second.Status != "completed" || requests.Load() != 4 {
		t.Fatalf("same-plan continuation did not finish: %#v requests=%d error=%v", second, requests.Load(), err)
	}
}
