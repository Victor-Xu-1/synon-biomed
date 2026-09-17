package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"synon-go/internal/agentruntime"
	runtimekv "synon-go/internal/persistence/runtimekv"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func semanticBudgetEvent(t *testing.T, value map[string]any) transcriptstore.RunnerReplayEvent {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return transcriptstore.RunnerReplayEvent{
		Event: transcriptstore.Event{Type: "runner_checkpoint"}, ResolvedPayloadJSON: payload,
	}
}

func TestDurableSemanticFailureTracksLatestExecutionIdentityWithoutClosingTarget(t *testing.T) {
	firstInput := map[string]any{"capability": "molecular-docking", "executable": "runner", "code": "first"}
	firstArguments, _ := json.Marshal(firstInput)
	failure := func(input map[string]any, diagnostic string) transcriptstore.RunnerReplayEvent {
		return semanticBudgetEvent(t, map[string]any{
			"type": "runner_checkpoint", "toolPhase": "failed", "status": "failed",
			"toolName": "software_runtime", "toolInput": input,
			"toolResult": map[string]any{
				"ok": false, "code": "nonzero_exit", "stderr": diagnostic,
			},
		})
	}
	projected := []transcriptstore.RunnerReplayEvent{failure(firstInput, "ValueError: first failure")}
	state := durableSemanticFailureStateForTarget(projected, "software_runtime", firstArguments)
	if state.Attempts != 1 || state.Fingerprint == "" ||
		state.ExecutionIdentity != agentruntime.ExecutionCallFingerprint("software_runtime", firstArguments) {
		t.Fatalf("first durable failure state=%#v", state)
	}
	projected = append(projected, semanticBudgetEvent(t, map[string]any{
		"type": "runner_checkpoint", "toolPhase": "completed", "status": "completed",
		"toolName": "Skill", "toolResult": map[string]any{"ok": true},
	}))
	state = durableSemanticFailureStateForTarget(projected, "software_runtime", firstArguments)
	if state.Attempts != 1 {
		t.Fatalf("unrelated inspection changed execution failure state: %#v", state)
	}
	secondInput := map[string]any{"capability": "molecular-docking", "executable": "runner", "code": "corrected"}
	secondArguments, _ := json.Marshal(secondInput)
	projected = append(projected, failure(secondInput, "ValueError: corrected retry failed differently"))
	state = durableSemanticFailureStateForTarget(projected, "software_runtime", firstArguments)
	if state.Attempts != 2 || state.Fingerprint == "" ||
		state.ExecutionIdentity != agentruntime.ExecutionCallFingerprint("software_runtime", secondArguments) {
		t.Fatalf("latest corrected execution identity was not retained: %#v", state)
	}
}

func TestDurableSemanticFailureIgnoresOtherTargetsAndTerminalBoundary(t *testing.T) {
	projected := []transcriptstore.RunnerReplayEvent{
		semanticBudgetEvent(t, map[string]any{
			"type": "runner_checkpoint", "toolPhase": "failed", "status": "failed",
			"toolName": "edit_file", "toolResult": map[string]any{
				"ok": false, "code": "nonzero_exit", "stderr": "ValueError: unrelated",
			},
		}),
		semanticBudgetEvent(t, map[string]any{
			"type": "runner_checkpoint", "toolPhase": "failed", "status": "failed",
			"toolName": "software_runtime", "toolInput": map[string]any{"capability": "other"},
			"toolResult": map[string]any{
				"ok": false, "code": "execution_path_exhausted", "message": "terminal boundary",
			},
		}),
	}
	arguments, _ := json.Marshal(map[string]any{"capability": "molecular-docking"})
	if state := durableSemanticFailureStateForTarget(projected, "software_runtime", arguments); state.Attempts != 0 || state.Fingerprint != "" {
		t.Fatalf("unrelated or terminal failures consumed target state=%#v", state)
	}
}

func TestDurableSemanticFailureIgnoresNonExecutingPreflight(t *testing.T) {
	input := map[string]any{"code": `record = host.mcp("source-broker", "read", {})`}
	arguments, _ := json.Marshal(input)
	projected := []transcriptstore.RunnerReplayEvent{semanticBudgetEvent(t, map[string]any{
		"type": "runner_checkpoint", "toolPhase": prestartToolFailurePhase, "status": "failed",
		"toolName": "repl", "toolInput": input, "rejectedBeforeExecution": true,
		"toolResult": map[string]any{
			"ok": false, "executed": false, "status": "mcp_schema_preflight_required",
			"message": "load the exact connector contract",
		},
	})}
	if state := durableSemanticFailureStateForTarget(projected, "repl", arguments); state.Attempts != 0 || state.Fingerprint != "" {
		t.Fatalf("non-executing preflight consumed durable execution budget: %#v", state)
	}
}

func TestSemanticFailureCacheRequiresExplicitExecutionFact(t *testing.T) {
	store := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{runtimeStore: store}
	call := agentruntime.ToolCall{
		ID: "repl-call", Name: "repl", Arguments: json.RawMessage(`{"code":"run_source()"}`),
	}
	server.recordAgentRuntimeSemanticFailure("session-1", call, map[string]any{
		"ok": false, "executed": false, "status": "mcp_schema_preflight_required",
		"message": "load the exact connector contract",
	})
	if entries, err := store.List(agentRuntimeSemanticFailureNamespace); err != nil || len(entries) != 0 {
		t.Fatalf("preflight entered execution cache: entries=%#v err=%v", entries, err)
	}
	failure := map[string]any{
		"ok": false, "status": "failed", "code": "python_value_error",
		"stderr": "ValueError: source operation failed",
	}
	server.recordAgentRuntimeSemanticFailure("session-1", call, failure)
	target := agentruntime.SemanticFailureTarget(call.Name, call.Arguments)
	fingerprint := agentruntime.SemanticFailureFingerprint(call.Name, call.Arguments, failure)
	if _, err := store.Set(agentRuntimeSemanticFailureNamespace, "legacy-ambiguous", map[string]any{
		"sessionId": "session-1", "tool": "repl", "target": target, "fingerprint": fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	state := server.agentRuntimeSemanticFailureState("session-1", "repl", call.Arguments)
	if state.Attempts != 1 || state.Fingerprint != fingerprint ||
		state.ExecutionIdentity != agentruntime.ExecutionCallFingerprint(call.Name, call.Arguments) {
		t.Fatalf("semantic cache accepted an ambiguous legacy preflight: %#v", state)
	}
}

func TestDurableEditConflictTracksMateriallyChangedRetry(t *testing.T) {
	input := map[string]any{"file_path": "report.md", "old_string": "stale", "new_string": "new"}
	arguments, _ := json.Marshal(input)
	failure := func(old string) transcriptstore.RunnerReplayEvent {
		return semanticBudgetEvent(t, map[string]any{
			"type": "runner_checkpoint", "toolPhase": "failed", "status": "failed",
			"toolName": "edit_file", "toolInput": map[string]any{
				"file_path": "report.md", "old_string": old, "new_string": "new",
			},
			"toolResult": map[string]any{
				"ok": false, "executed": false, "code": "edit_conflict", "retryable": true,
				"message": "The file changed or the requested old_string is not an exact unique match.",
			},
		})
	}
	projected := []transcriptstore.RunnerReplayEvent{failure("stale")}
	state := durableSemanticFailureStateForTarget(projected, "edit_file", arguments)
	if state.Attempts != 1 {
		t.Fatalf("first edit state=%#v", state)
	}
	projected = append(projected, semanticBudgetEvent(t, map[string]any{
		"type": "runner_checkpoint", "toolPhase": "completed", "status": "completed",
		"toolName": "read_file", "toolInput": map[string]any{"file_path": "report.md"},
		"toolResult": map[string]any{"ok": true, "content": "current"},
	}))
	state = durableSemanticFailureStateForTarget(projected, "edit_file", arguments)
	if state.Attempts != 1 {
		t.Fatalf("fresh read changed durable execution identity: %#v", state)
	}
	projected = append(projected, failure("current but mismatched"))
	state = durableSemanticFailureStateForTarget(projected, "edit_file", arguments)
	secondArguments, _ := json.Marshal(map[string]any{
		"file_path": "report.md", "old_string": "current but mismatched", "new_string": "new",
	})
	if state.Attempts != 2 || state.ExecutionIdentity != agentruntime.ExecutionCallFingerprint("edit_file", secondArguments) {
		t.Fatalf("second edit conflict identity=%#v", state)
	}
}

func TestExecutionIdentityIgnoresPresentationButDetectsMaterialCorrection(t *testing.T) {
	first := json.RawMessage(`{"environment":"python","code":"run()","human_description":"Running"}`)
	presentationOnly := json.RawMessage(`{"environment":"python","code":"run()","human_description":"Retrying"}`)
	corrected := json.RawMessage(`{"environment":"python","code":"run_fixed()","human_description":"Retrying"}`)
	firstIdentity := agentruntime.ExecutionCallFingerprint("python", first)
	if firstIdentity != agentruntime.ExecutionCallFingerprint("python", presentationOnly) {
		t.Fatal("presentation copy changed execution identity")
	}
	if firstIdentity == agentruntime.ExecutionCallFingerprint("python", corrected) {
		t.Fatal("corrected code did not change execution identity")
	}
}

func TestDurableSemanticBoundariesAreNonExecutingLifecycleEvents(t *testing.T) {
	arguments := json.RawMessage(`{"environment":"python","code":"run()"}`)
	boundaries := []map[string]any{
		durableSemanticExecutionBoundary("python", arguments, durableSemanticFailureState{
			Attempts: 1, Fingerprint: "python|target|python_execution_failed|NameError",
			ExecutionIdentity: agentruntime.ExecutionCallFingerprint("python", arguments),
		}),
		durableSemanticPreflightBoundary(map[string]any{
			"ok": false, "status": "external_state_required",
			"message": "wait for the external runtime to recover",
		}),
	}
	for _, boundary := range boundaries {
		if boundary["executed"] != false || boundary["preflight"] != true ||
			!agentruntime.IsNonExecutingPreflight(boundary) {
			t.Fatalf("durable boundary was not marked as non-executing: %#v", boundary)
		}
	}
	corrected := json.RawMessage(`{"environment":"python","code":"run_fixed()"}`)
	if boundary := durableSemanticExecutionBoundary("python", corrected, durableSemanticFailureState{
		Attempts: 1, Fingerprint: "python|target|python_execution_failed|NameError",
		ExecutionIdentity: agentruntime.ExecutionCallFingerprint("python", arguments),
	}); boundary != nil {
		t.Fatalf("materially corrected execution was blocked: %#v", boundary)
	}
}
