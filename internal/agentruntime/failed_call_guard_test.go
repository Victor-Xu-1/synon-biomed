package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

type boundedRegisteredCorrectionGateway struct {
	executions atomic.Int64
}

type rotatingExecutionIdentityGateway struct {
	executions atomic.Int64
}

func (gateway *rotatingExecutionIdentityGateway) ToolCallExecutionIdentity(call ToolCall) (ToolCall, bool) {
	resolved := call
	url := "https://evidence.example/source-a"
	if gateway.executions.Load() > 0 {
		url = "https://evidence.example/source-b"
	}
	resolved.Arguments = json.RawMessage(`{"url":"` + url + `"}`)
	return resolved, true
}

func (gateway *rotatingExecutionIdentityGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	attempt := gateway.executions.Add(1)
	url := "https://evidence.example/source-a"
	if attempt > 1 {
		url = "https://evidence.example/source-b"
	}
	return ToolResult{
		Value: map[string]any{
			"ok": false, "code": "not_found", "retryable": false,
			"error": "source record was unavailable",
		},
		ExecutedArguments: json.RawMessage(`{"url":"` + url + `"}`),
	}, nil
}

func (gateway *boundedRegisteredCorrectionGateway) Execute(_ context.Context, _ ToolCall) (ToolResult, error) {
	if gateway.executions.Add(1) == 1 {
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "software_runtime_import_missing",
			"stderr": "ModuleNotFoundError: No module named 'pandas'",
		}}, nil
	}
	return ToolResult{Value: map[string]any{"ok": true, "stdout": "stdlib check passed"}}, nil
}

func TestFailedToolCallGuardAllowsMateriallyCorrectedRegisteredExecution(t *testing.T) {
	delegate := &boundedRegisteredCorrectionGateway{}
	guard := newFailedToolCallGuard(delegate)
	first, err := guard.Execute(context.Background(), ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"import pandas"}`),
	})
	if err != nil || !ClassifyToolResult(first.Value).HardFailed() {
		t.Fatalf("first fixed-job execution=%#v err=%v", first.Value, err)
	}
	second, err := guard.Execute(context.Background(), ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"import csv"}`),
	})
	if err != nil || ClassifyToolResult(second.Value) != ToolResultSucceeded || delegate.executions.Load() != 2 {
		t.Fatalf("corrected fixed-job execution=%#v err=%v executions=%d", second.Value, err, delegate.executions.Load())
	}
}

func TestFailedToolCallGuardUsesExecutedIdentityBeforeBlockingRetry(t *testing.T) {
	delegate := &rotatingExecutionIdentityGateway{}
	guard := newFailedToolCallGuard(delegate)
	requested := ToolCall{
		Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://model.example/repeated"}`),
	}

	first, err := guard.Execute(context.Background(), requested)
	if err != nil || delegate.executions.Load() != 1 ||
		!strings.Contains(string(first.ExecutedArguments), "source-a") {
		t.Fatalf("first normalized execution=%#v err=%v executions=%d", first, err, delegate.executions.Load())
	}
	second, err := guard.Execute(context.Background(), requested)
	if err != nil || delegate.executions.Load() != 2 ||
		!strings.Contains(string(second.ExecutedArguments), "source-b") {
		t.Fatalf("second normalized execution=%#v err=%v executions=%d", second, err, delegate.executions.Load())
	}
	third, err := guard.Execute(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	value := mapValueForTest(t, third.Value)
	if value["code"] != "repeated_failed_tool_call" || value["executed"] != false ||
		!IsNonExecutingPreflight(value) || delegate.executions.Load() != 2 {
		t.Fatalf("executed identity was not guarded correctly: %#v executions=%d", value, delegate.executions.Load())
	}
}

func TestEngineBlocksIdenticalFailedToolCallAcrossUnrelatedSuccess(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		if call.Name == "read_state" {
			return ToolResult{Value: map[string]any{"ok": true, "state": "current"}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": false, "error": "stale input"}}, nil
	}))
	failedCall := ToolCall{ID: "first", Name: "edit_file", Arguments: json.RawMessage(`{"path":"report.md","old":"stale"}`)}

	first, err := guard.Execute(context.Background(), failedCall)
	if err != nil || !ClassifyToolResult(first.Value).Failed() {
		t.Fatalf("first failure = %#v err=%v", first, err)
	}
	failedCall.ID = "blind-repeat"
	repeated, err := guard.Execute(context.Background(), failedCall)
	if err != nil {
		t.Fatal(err)
	}
	repeatedValue, _ := repeated.Value.(map[string]any)
	if repeatedValue["code"] != "repeated_failed_tool_call" || repeatedValue["ok"] != false || executions.Load() != 1 {
		t.Fatalf("blind repeat = %#v executions=%d", repeated.Value, executions.Load())
	}

	if _, err := guard.Execute(context.Background(), ToolCall{ID: "observe", Name: "read_state", Arguments: json.RawMessage(`{"path":"report.md"}`)}); err != nil {
		t.Fatal(err)
	}
	failedCall.ID = "after-unrelated-observation"
	blocked, err := guard.Execute(context.Background(), failedCall)
	if err != nil {
		t.Fatal(err)
	}
	blockedValue, _ := blocked.Value.(map[string]any)
	if blockedValue["code"] != "repeated_failed_tool_call" || executions.Load() != 2 {
		t.Fatalf("unrelated success reopened exact failure: value=%#v executions=%d", blocked.Value, executions.Load())
	}
}

func TestFailedToolCallGuardTreatsWebFetchPromptChangesAsSameDeniedDestination(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "secure_fetch_redirect_denied", "retryable": false,
			"error": "redirect destination is not permitted by the outbound policy",
		}}, nil
	}))
	first, err := guard.Execute(context.Background(), ToolCall{
		ID: "fetch-1", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://doi.org/10.2210/pdb6n2k/pdb","prompt":"read the title"}`),
	})
	if err != nil || !ClassifyToolResult(first.Value).HardFailed() {
		t.Fatalf("first denied fetch=%#v err=%v", first.Value, err)
	}
	repeated, err := guard.Execute(context.Background(), ToolCall{
		ID: "fetch-2", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://doi.org/10.2210/pdb6n2k/pdb","prompt":"extract the metadata"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := repeated.Value.(map[string]any)
	if value["code"] != "source_destination_rejected" ||
		ClassifyToolResult(value) != ToolResultUnavailable ||
		value["recovery"] != "choose_a_materially_different_authoritative_source_or_continue_with_verified_evidence" ||
		executions.Load() != 1 {
		t.Fatalf("prompt-only WebFetch retry was not blocked: %#v executions=%d", repeated.Value, executions.Load())
	}
}

func TestFailedToolCallGuardIgnoresHumanDescriptionWhenDetectingRepeat(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "not_found", "retryable": false, "error": "source file not found",
		}}, nil
	}))
	first := ToolCall{Name: "download_public_scientific_file", Arguments: json.RawMessage(
		`{"url":"https://example.test/missing.tsv","filename":"missing.tsv","human_description":"下载数据"}`,
	)}
	second := ToolCall{Name: "download_public_scientific_file", Arguments: json.RawMessage(
		`{"url":"https://example.test/missing.tsv","filename":"missing.tsv","human_description":"重新下载数据"}`,
	)}
	if _, err := guard.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	result, err := guard.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := result.Value.(map[string]any)
	if value["code"] != "repeated_failed_tool_call" || executions.Load() != 1 {
		t.Fatalf("repeat result=%#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardAllowsCorrectedArgumentsImmediately(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		var input map[string]any
		_ = json.Unmarshal(call.Arguments, &input)
		if _, ok := input["max_phase"].(string); ok {
			return ToolResult{Value: map[string]any{"ok": false, "code": "invalid_tool_arguments"}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": true}}, nil
	}))
	if _, err := guard.Execute(context.Background(), ToolCall{ID: "quoted", Name: "drug_search", Arguments: json.RawMessage(`{"max_phase":"4"}`)}); err != nil {
		t.Fatal(err)
	}
	result, err := guard.Execute(context.Background(), ToolCall{ID: "corrected", Name: "drug_search", Arguments: json.RawMessage(`{"max_phase":4}`)})
	if err != nil || ClassifyToolResult(result.Value).Failed() || executions.Load() != 2 {
		t.Fatalf("corrected result = %#v err=%v executions=%d", result, err, executions.Load())
	}
	retry, err := guard.Execute(context.Background(), ToolCall{ID: "quoted-after-correction", Name: "drug_search", Arguments: json.RawMessage(`{"max_phase":"4"}`)})
	if err != nil || !ClassifyToolResult(retry.Value).Failed() || executions.Load() != 3 {
		t.Fatalf("same-tool correction did not clear stale failure: result=%#v err=%v executions=%d", retry, err, executions.Load())
	}
}

func TestFailedToolCallGuardAllowsChangingRegisteredCorrectionsButBlocksExactReplay(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "status": "failed", "code": "nonzero_exit", "retryable": true,
			"stderr": "AttributeError: documented invocation rejected the input",
		}}, nil
	}))
	call := func(id, capability, variant string) ToolCall {
		return ToolCall{ID: id, Name: "software_runtime", Arguments: json.RawMessage(
			`{"capability":"` + capability + `","executable":"runner","variant":"` + variant + `"}`,
		)}
	}

	first, err := guard.Execute(context.Background(), call("first", "molecular-docking", "first"))
	firstValue, _ := first.Value.(map[string]any)
	if err != nil || firstValue["code"] != "nonzero_exit" || executions.Load() != 1 {
		t.Fatalf("first execution unit=%#v err=%v executions=%d", firstValue, err, executions.Load())
	}
	blocked, err := guard.Execute(context.Background(), call("exact-replay", "molecular-docking", "first"))
	blockedValue, _ := blocked.Value.(map[string]any)
	if err != nil || blockedValue["code"] != "repeated_failed_tool_call" || blockedValue["executed"] != false ||
		!ClassifyToolResult(blocked.Value).HardFailed() || executions.Load() != 1 {
		t.Fatalf("exact retry boundary=%#v err=%v executions=%d", blockedValue, err, executions.Load())
	}
	second, err := guard.Execute(context.Background(), call("corrected-retry", "molecular-docking", "second"))
	secondValue, _ := second.Value.(map[string]any)
	if err != nil || secondValue["code"] != "nonzero_exit" || executions.Load() != 2 {
		t.Fatalf("corrected execution was blocked=%#v err=%v executions=%d", secondValue, err, executions.Load())
	}
	after, err := guard.Execute(context.Background(), call("third-correction", "molecular-docking", "third"))
	afterValue, _ := after.Value.(map[string]any)
	if err != nil || afterValue["code"] != "nonzero_exit" || executions.Load() != 3 {
		t.Fatalf("changing correction was count-gated: %#v err=%v executions=%d", afterValue, err, executions.Load())
	}
	different, err := guard.Execute(context.Background(), call("different-capability", "sequence-alignment", "first"))
	differentValue, _ := different.Value.(map[string]any)
	if err != nil || differentValue["code"] != "nonzero_exit" || executions.Load() != 4 {
		t.Fatalf("different registered capability was blocked: %#v err=%v executions=%d", differentValue, err, executions.Load())
	}
}

func TestFailedToolCallGuardDoesNotAggregateDifferentCorrectionsIntoClosedPath(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		if call.Name == "skill" {
			return ToolResult{Value: map[string]any{"ok": true, "status": "completed", "skill": "documented-runtime"}}, nil
		}
		executions.Add(1)
		var input map[string]any
		_ = json.Unmarshal(call.Arguments, &input)
		switch input["code"] {
		case "success":
			return ToolResult{Value: map[string]any{"ok": true, "stdout": "inspected"}}, nil
		case "second":
			return ToolResult{Value: map[string]any{
				"ok": false, "status": "failed", "code": "python_type_error",
				"stderr": "TypeError: second documented attempt failed differently",
			}}, nil
		default:
			return ToolResult{Value: map[string]any{
				"ok": false, "status": "failed", "code": "python_attribute_error",
				"stderr": "AttributeError: first attempt used an unavailable API",
			}}, nil
		}
	}))
	call := func(id, code string) ToolCall {
		return ToolCall{ID: id, Name: "python", Arguments: json.RawMessage(`{"environment":"chemistry","code":"` + code + `"}`)}
	}
	if first, err := guard.Execute(context.Background(), call("first", "first")); err != nil || !ClassifyToolResult(first.Value).Failed() {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	if success, err := guard.Execute(context.Background(), call("success", "success")); err != nil || ClassifyToolResult(success.Value).Failed() {
		t.Fatalf("success=%#v err=%v", success, err)
	}
	second, err := guard.Execute(context.Background(), call("second", "second"))
	secondValue, _ := second.Value.(map[string]any)
	if err != nil || secondValue["code"] != "python_type_error" {
		t.Fatalf("second=%#v err=%v", secondValue, err)
	}
	after, err := guard.Execute(context.Background(), call("after", "third"))
	afterValue, _ := after.Value.(map[string]any)
	if err != nil || afterValue["code"] != "python_attribute_error" || executions.Load() != 4 {
		t.Fatalf("after=%#v executions=%d err=%v", afterValue, executions.Load(), err)
	}
}

func TestFailedToolCallGuardDoesNotCountNonExecutingPreflightAsExecutionFailure(t *testing.T) {
	var decisions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		if call.Name == "search_skills" {
			return ToolResult{Value: map[string]any{"ok": true, "matches": []any{"python"}}}, nil
		}
		decisions.Add(1)
		if strings.Contains(string(call.Arguments), "run_fixed") {
			return ToolResult{Value: map[string]any{"ok": true, "stdout": "fixed"}}, nil
		}
		return ToolResult{Value: map[string]any{
			"ok": false, "status": "code_preflight_required", "executed": false,
			"message": "inspect the documented Python API",
		}}, nil
	}))
	call := ToolCall{Name: "python", Arguments: json.RawMessage(`{"working_dir":"task","code":"run()"}`)}
	first, err := guard.Execute(context.Background(), call)
	firstValue, _ := first.Value.(map[string]any)
	if err != nil || !IsNonExecutingPreflight(firstValue) || firstValue["terminal"] != nil || decisions.Load() != 1 {
		t.Fatalf("preflight decision=%#v err=%v decisions=%d", firstValue, err, decisions.Load())
	}
	if _, err := guard.Execute(context.Background(), ToolCall{Name: "search_skills", Arguments: json.RawMessage(`{"query":"python api"}`)}); err != nil {
		t.Fatal(err)
	}
	call.ID = "corrected-after-inspection"
	call.Arguments = json.RawMessage(`{"working_dir":"task","code":"run_fixed()"}`)
	second, err := guard.Execute(context.Background(), call)
	if err != nil || ClassifyToolResult(second.Value) != ToolResultSucceeded || decisions.Load() != 2 {
		t.Fatalf("corrected call was blocked by a non-executing preflight: %#v err=%v decisions=%d", second.Value, err, decisions.Load())
	}
}

func TestFailedToolCallGuardWaitsForExternalStateAfterRegisteredRuntimeFailure(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{"ok": false, "error": "runtime draining"}}, nil
	}))
	call := ToolCall{ID: "drain-1", Name: "software_runtime",
		Arguments: json.RawMessage(`{"capability":"molecular-docking","executable":"runner"}`)}
	first, err := guard.Execute(context.Background(), call)
	firstValue, _ := first.Value.(map[string]any)
	if err != nil || firstValue["failure_kind"] != "transient" || firstValue["terminal"] != true ||
		firstValue["retryable"] != false || executions.Load() != 1 {
		t.Fatalf("runtime external failure=%#v err=%v executions=%d", firstValue, err, executions.Load())
	}
	call.ID = "drain-retry"
	blocked, err := guard.Execute(context.Background(), call)
	blockedValue, _ := blocked.Value.(map[string]any)
	if err != nil || blockedValue["status"] != "external_state_required" ||
		blockedValue["next_action"] != "wait_for_external_state_then_start_new_execution" ||
		!ClassifyToolResult(blocked.Value).HardFailed() || executions.Load() != 1 {
		t.Fatalf("runtime was retried without external signal: %#v err=%v executions=%d", blockedValue, err, executions.Load())
	}
}

func TestFailedToolCallGuardBoundsUnavailableConnectorAcrossQueryChanges(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": true, "sourceUnavailable": true, "code": "upstream_unavailable",
			"retryable": true, "error": "connector is temporarily unavailable",
		}}, nil
	}))

	for attempt := 1; attempt <= maxTransientSourceUnavailableAttempts; attempt++ {
		result, err := guard.Execute(context.Background(), ToolCall{
			ID: string(rune('a' + attempt)), Name: "mcp__chembl__compound_search",
			Arguments: json.RawMessage(`{"name":"candidate-` + string(rune('a'+attempt)) + `"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		value, _ := result.Value.(map[string]any)
		if attempt < maxTransientSourceUnavailableAttempts && value["retryable"] != true {
			t.Fatalf("attempt %d was terminated too early: %#v", attempt, value)
		}
		if attempt == maxTransientSourceUnavailableAttempts &&
			(value["code"] != "source_unavailable_retry_exhausted" || value["retryable"] != false) {
			t.Fatalf("final unavailable attempt was not bounded: %#v", value)
		}
	}

	blocked, err := guard.Execute(context.Background(), ToolCall{
		ID: "blocked", Name: "mcp__chembl__get_compound",
		Arguments: json.RawMessage(`{"name":"a-materially-different-query"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := blocked.Value.(map[string]any)
	if value["code"] != "source_unavailable_retry_exhausted" || executions.Load() != maxTransientSourceUnavailableAttempts {
		t.Fatalf("connector-level unavailable budget was bypassed: %#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardDoesNotTreatSourceBudgetBoundaryAsTransient(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "sourceUnavailable": true, "code": "source_budget_reached",
			"retryable": false, "error": "source advisory boundary reached",
		}}, nil
	}))
	call := ToolCall{Name: "web_search", Arguments: json.RawMessage(`{"query":"same"}`)}
	if _, err := guard.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	repeated, err := guard.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := repeated.Value.(map[string]any)
	if value["code"] != "repeated_failed_tool_call" || executions.Load() != 1 {
		t.Fatalf("source budget boundary was incorrectly treated as transient: %#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardQuarantinesDefinitiveScientificDownloadURL(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"sourceUnavailable": true, "status": "source_response_mismatch",
			"code": "source_response_mismatch", "retryable": false,
		}}, nil
	}))
	first := ToolCall{Name: "download_public_scientific_file", Arguments: json.RawMessage(
		`{"url":"https://example.test/article.pdf","filename":"article.pdf","human_description":"Downloading article"}`,
	)}
	if result, err := guard.Execute(context.Background(), first); err != nil ||
		ClassifyToolResult(result.Value) != ToolResultUnavailable {
		t.Fatalf("first definitive unavailable result=%#v err=%v", result.Value, err)
	}
	second := ToolCall{Name: "download_public_scientific_file", Arguments: json.RawMessage(
		`{"url":"https://example.test/article.pdf","filename":"renamed.pdf","human_description":"Retrying article"}`,
	)}
	result, err := guard.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := result.Value.(map[string]any)
	if value["code"] != "source_destination_rejected" ||
		value["recovery"] != "choose_a_materially_different_authoritative_source_or_continue_with_verified_evidence" ||
		executions.Load() != 1 {
		t.Fatalf("definitive URL was not quarantined: %#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardBoundsRCSBAuthorityUnavailableAcrossEntries(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "error": "rcsb download authority is unavailable",
		}}, nil
	}))

	for attempt := 1; attempt <= maxTransientSourceUnavailableAttempts; attempt++ {
		result, err := guard.Execute(context.Background(), ToolCall{
			ID: string(rune('a' + attempt)), Name: "download_rcsb_file",
			Arguments: json.RawMessage(`{"entry_id":"7U5` + string(rune('0'+attempt)) + `","format":"cif"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		value, _ := result.Value.(map[string]any)
		if attempt == maxTransientSourceUnavailableAttempts &&
			(value["code"] != "source_unavailable_retry_exhausted" || value["retryable"] != false) {
			t.Fatalf("RCSB connector budget did not terminate: %#v", value)
		}
	}
	blocked, err := guard.Execute(context.Background(), ToolCall{
		ID: "blocked", Name: "download_rcsb_file",
		Arguments: json.RawMessage(`{"entry_id":"5NH3","format":"pdb"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := blocked.Value.(map[string]any)
	if value["code"] != "source_unavailable_retry_exhausted" || executions.Load() != maxTransientSourceUnavailableAttempts {
		t.Fatalf("RCSB unavailable result bypassed connector budget: %#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardBoundsArbitrarySourceCapability(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "sourceUnavailable": true, "error": "upstream unavailable",
		}}, nil
	}), ToolSchema{
		Name: "future_source_reader", Capabilities: []string{"source-evidence", "evidence-read"},
	})
	for attempt := 0; attempt < maxTransientSourceUnavailableAttempts; attempt++ {
		result, err := guard.Execute(context.Background(), ToolCall{
			ID: fmt.Sprintf("source-%d", attempt), Name: "future_source_reader",
			Arguments: json.RawMessage(fmt.Sprintf(`{"record":%d}`, attempt)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if attempt == maxTransientSourceUnavailableAttempts-1 {
			value, _ := result.Value.(map[string]any)
			if value["code"] != "source_unavailable_retry_exhausted" || value["retryable"] != false {
				t.Fatalf("capability source budget did not settle: %#v", value)
			}
		}
	}
	blocked, err := guard.Execute(context.Background(), ToolCall{
		ID: "source-blocked", Name: "future_source_reader", Arguments: json.RawMessage(`{"record":99}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := blocked.Value.(map[string]any)
	if value["code"] != "source_unavailable_retry_exhausted" || executions.Load() != maxTransientSourceUnavailableAttempts {
		t.Fatalf("capability source route repeated after exhaustion: %#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardUsesArbitraryMutationCapability(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		if call.Name == "future_writer" {
			return ToolResult{Value: map[string]any{"ok": false, "error": "invalid current bytes"}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": true}}, nil
	}),
		ToolSchema{Name: "future_writer", Capabilities: []string{"artifact-write"}},
		ToolSchema{Name: "future_mutator", Capabilities: []string{"artifact-write"}},
	)
	write := ToolCall{Name: "future_writer", Arguments: json.RawMessage(`{"path":"result.dat"}`)}
	if _, err := guard.Execute(context.Background(), write); err != nil {
		t.Fatal(err)
	}
	blocked, err := guard.Execute(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := blocked.Value.(map[string]any); value["code"] != "repeated_failed_tool_call" {
		t.Fatalf("unchanged failure was not blocked: %#v", blocked.Value)
	}
	if _, err := guard.Execute(context.Background(), ToolCall{
		Name: "future_mutator", Arguments: json.RawMessage(`{"path":"result.dat"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Execute(context.Background(), write); err != nil || executions.Load() != 3 {
		t.Fatalf("capability mutation did not reopen the corrected call: err=%v executions=%d", err, executions.Load())
	}
}

func TestSemanticFailureFingerprintUsesCapabilityAuthority(t *testing.T) {
	failure := map[string]any{"ok": false, "code": "software_output_validation_failed", "error": "invalid output"}
	arguments := json.RawMessage(`{"working_dir":"analysis"}`)
	if fingerprint := SemanticFailureFingerprintWithCapabilities(
		"future_compute", arguments, failure, []string{"runtime-execution"},
	); fingerprint == "" {
		t.Fatal("runtime capability did not receive semantic recovery identity")
	}
	if fingerprint := SemanticFailureFingerprintWithCapabilities(
		"future_reader", arguments, failure, []string{"source-evidence", "read-only"},
	); fingerprint != "" {
		t.Fatalf("read-only source capability was misclassified as mutable execution: %q", fingerprint)
	}
}

func TestFailedToolCallGuardHonorsTrustedBoundedExactRetryBudget(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		attempt := executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "invalid_review_evidence",
			"retryable": attempt < 2, "attempts": attempt, "max_attempts": 2,
		}}, nil
	}))
	call := ToolCall{Name: "submit_completion_review", Arguments: json.RawMessage(`{"verdict":"revise"}`)}

	call.ID = "first"
	first, err := guard.Execute(context.Background(), call)
	if err != nil || !ClassifyToolResult(first.Value).HardFailed() || executions.Load() != 1 {
		t.Fatalf("first bounded failure=%#v err=%v executions=%d", first.Value, err, executions.Load())
	}
	call.ID = "second"
	second, err := guard.Execute(context.Background(), call)
	if err != nil || !ClassifyToolResult(second.Value).HardFailed() || executions.Load() != 2 {
		t.Fatalf("second bounded failure=%#v err=%v executions=%d", second.Value, err, executions.Load())
	}
	call.ID = "third"
	third, err := guard.Execute(context.Background(), call)
	thirdValue, _ := third.Value.(map[string]any)
	if err != nil || thirdValue["code"] != "repeated_failed_tool_call" || executions.Load() != 2 {
		t.Fatalf("exhausted bounded retry=%#v err=%v executions=%d", third.Value, err, executions.Load())
	}
}

func TestFailedToolCallGuardAllowsRetryAfterWorkspaceCorrection(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		if call.Name == "save_artifacts" {
			return ToolResult{Value: map[string]any{
				"ok": false, "error": "unsupported evidence reference", "retryable": false,
			}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": true, "files_written": []any{"report.md"}}}, nil
	}))
	save := ToolCall{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"]}`)}
	if _, err := guard.Execute(context.Background(), save); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"report.md"}`)}); err != nil {
		t.Fatal(err)
	}
	retry, err := guard.Execute(context.Background(), save)
	if err != nil || executions.Load() != 3 {
		t.Fatalf("workspace correction did not reopen exact save call: result=%#v err=%v executions=%d", retry, err, executions.Load())
	}
	value, _ := retry.Value.(map[string]any)
	if value["code"] == "repeated_failed_tool_call" {
		t.Fatalf("exact save call remained blocked after workspace correction: %#v", retry.Value)
	}
}

func TestFailedToolCallGuardAllowsChangingInvalidCorrectionsAndBlocksExactReplay(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "invalid_tool_arguments", "retryable": true,
			"issues": []any{map[string]any{"path": "/query", "keyword": "required", "actual": "missing"}},
		}}, nil
	}))
	for index, arguments := range []string{`{"chembl_id":"CHEMBL1"}`, `{"drug_name":"gefitinib"}`} {
		result, err := guard.Execute(context.Background(), ToolCall{ID: string(rune('a' + index)), Name: "compound_search", Arguments: json.RawMessage(arguments)})
		if err != nil {
			t.Fatal(err)
		}
		value, _ := result.Value.(map[string]any)
		if value["code"] != "invalid_tool_arguments" || value["retryable"] != true {
			t.Fatalf("changing invalid correction was rewritten: %#v", result.Value)
		}
	}
	thirdCall := ToolCall{ID: "third", Name: "compound_search", Arguments: json.RawMessage(`{"name":"erlotinib"}`)}
	third, err := guard.Execute(context.Background(), thirdCall)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := third.Value.(map[string]any); value["code"] != "invalid_tool_arguments" || executions.Load() != 3 {
		t.Fatalf("third changed correction was count-gated: %#v executions=%d", third.Value, executions.Load())
	}
	thirdCall.ID = "third-repeat"
	blocked, err := guard.Execute(context.Background(), thirdCall)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := blocked.Value.(map[string]any); value["code"] != "repeated_failed_tool_call" || executions.Load() != 3 {
		t.Fatalf("exact invalid replay was not blocked: %#v executions=%d", blocked.Value, executions.Load())
	}
}

type admissionAwareGateway struct {
	executions *atomic.Int64
}

func (g admissionAwareGateway) AdmitsToolCall(call ToolCall) bool {
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil {
		return false
	}
	_, hasURL := input["url"].(string)
	_, hasPrompt := input["prompt"]
	return hasURL && !hasPrompt
}

func (g admissionAwareGateway) ToolCallAdmissionDiagnostic(ToolCall) string {
	return `{"code":"invalid_tool_arguments","issues":[{"path":"/prompt","expected":"field is not allowed"}]}`
}

func (g admissionAwareGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	g.executions.Add(1)
	if !g.AdmitsToolCall(call) {
		return ToolResult{Value: map[string]any{
			"ok": false, "code": "invalid_tool_arguments", "retryable": true,
		}}, nil
	}
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func TestFailedToolCallGuardAllowsAdmittedCorrectionAfterParallelInvalidCalls(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(admissionAwareGateway{executions: &executions})
	for _, arguments := range []string{
		`{"url":"https://example.test/a","prompt":"extract A"}`,
		`{"url":"https://example.test/b","prompt":"extract B"}`,
	} {
		result, err := guard.Execute(context.Background(), ToolCall{Name: "web_fetch", Arguments: json.RawMessage(arguments)})
		if err != nil || !ClassifyToolResult(result.Value).Failed() {
			t.Fatalf("invalid parallel call = %#v err=%v", result.Value, err)
		}
	}
	corrected, err := guard.Execute(context.Background(), ToolCall{
		Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test/a"}`),
	})
	if err != nil || ClassifyToolResult(corrected.Value).Failed() || executions.Load() != 3 {
		t.Fatalf("admitted correction = %#v err=%v executions=%d", corrected.Value, err, executions.Load())
	}
}

func TestFailedToolCallGuardKeepsChangingEditCorrectionsRecoverable(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions.Add(1)
		if call.Name == "read_file" {
			return ToolResult{Value: map[string]any{"ok": true, "content": "current"}}, nil
		}
		return ToolResult{Value: map[string]any{
			"ok": false, "executed": false, "status": "edit_preflight_required",
			"code": "edit_conflict", "retryable": true,
			"message":  "The file changed or the requested old_string is not an exact unique match.",
			"recovery": "read_file_then_retry_with_current_exact_text",
		}}, nil
	}))
	edit := func(id, old string) ToolResult {
		result, err := guard.Execute(context.Background(), ToolCall{
			ID: id, Name: "edit_file",
			Arguments: json.RawMessage(`{"file_path":"report.md","old_string":"` + old + `","new_string":"new"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := mapValueForTest(t, edit("edit-1", "stale one").Value)
	if first["retryable"] != true || first["recovery"] != "read_file_then_retry_with_current_exact_text" {
		t.Fatalf("first edit conflict=%#v", first)
	}
	if _, err := guard.Execute(context.Background(), ToolCall{
		ID: "read-1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"report.md"}`),
	}); err != nil {
		t.Fatal(err)
	}
	second := mapValueForTest(t, edit("edit-2", "current but mismatched").Value)
	if second["code"] != "edit_conflict" || second["retryable"] != true {
		t.Fatalf("second edit conflict=%#v", second)
	}
	third := mapValueForTest(t, edit("edit-3", "another variation").Value)
	if third["code"] != "edit_conflict" || third["retryable"] != true || executions.Load() != 4 {
		t.Fatalf("third edit conflict=%#v executions=%d", third, executions.Load())
	}
}

func mapValueForTest(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value=%#v", value)
	}
	return result
}

func TestFailedToolCallGuardBoundsRepeatedNonRetryablePartialFailures(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Value: map[string]any{
			"artifacts": []any{map[string]any{"filename": "structure.png"}},
			"errors": []any{map[string]any{
				"code": "unsupported_evidence_references", "retryable": false,
			}},
		}}, nil
	}))
	call := ToolCall{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.html","structure.png"]}`)}

	for attempt := 1; attempt <= 2; attempt++ {
		result, err := guard.Execute(context.Background(), call)
		if err != nil || ClassifyToolResult(result.Value) != ToolResultPartial {
			t.Fatalf("partial attempt %d = %#v err=%v", attempt, result.Value, err)
		}
	}
	blocked, err := guard.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := blocked.Value.(map[string]any)
	if value["code"] != "repeated_non_retryable_tool_call" || value["retryable"] != false || executions.Load() != 2 {
		t.Fatalf("unbounded non-retryable partial failure = %#v executions=%d", blocked.Value, executions.Load())
	}
}

func TestFailedToolCallGuardAllowsPartialRetryAfterWorkspaceCorrection(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		attempt := executions.Add(1)
		if call.Name == "save_artifacts" && attempt <= 2 {
			return ToolResult{Value: map[string]any{
				"artifacts": []any{map[string]any{"filename": "risk_landscape.png"}},
				"errors": []any{map[string]any{
					"code": "unsupported_evidence_references", "retryable": false,
				}},
			}}, nil
		}
		if call.Name == "edit_file" {
			return ToolResult{Value: map[string]any{
				"ok": true, "files_written": []any{"report.md"},
			}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": true}}, nil
	}))
	save := ToolCall{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md","risk_landscape.png"]}`)}
	if result, err := guard.Execute(context.Background(), save); err != nil || ClassifyToolResult(result.Value) != ToolResultPartial {
		t.Fatalf("first partial save=%#v err=%v", result.Value, err)
	}
	if result, err := guard.Execute(context.Background(), save); err != nil || ClassifyToolResult(result.Value) != ToolResultPartial {
		t.Fatalf("second partial save=%#v err=%v", result.Value, err)
	}
	if _, err := guard.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"report.md"}`)}); err != nil {
		t.Fatal(err)
	}
	retry, err := guard.Execute(context.Background(), save)
	if err != nil || ClassifyToolResult(retry.Value) != ToolResultSucceeded || executions.Load() != 4 {
		t.Fatalf("partial save remained blocked after workspace correction: result=%#v err=%v executions=%d", retry.Value, err, executions.Load())
	}
	if value, _ := retry.Value.(map[string]any); value["code"] == "repeated_non_retryable_tool_call" {
		t.Fatalf("stale partial guard blocked corrected save=%#v", retry.Value)
	}
}
