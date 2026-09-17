package agentruntime

import "testing"

type testToolResultEnvelope struct {
	result map[string]any
}

func (value testToolResultEnvelope) ToolResultEnvelope() map[string]any {
	return value.result
}

func TestClassifyToolResultUsesClosedProductionEnvelopeContract(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  ToolResultOutcome
	}{
		{name: "plain success", value: map[string]any{"ok": true, "result": map[string]any{"items": []any{1}}}, want: ToolResultSucceeded},
		{name: "nested ok false", value: map[string]any{"ok": true, "result": map[string]any{"ok": false}}, want: ToolResultFailed},
		{name: "success false", value: map[string]any{"success": false}, want: ToolResultFailed},
		{name: "mcp is error", value: map[string]any{"isError": true}, want: ToolResultFailed},
		{name: "source unavailable", value: map[string]any{"sourceUnavailable": true}, want: ToolResultUnavailable},
		{name: "recoverable search unavailable", value: map[string]any{
			"failure": map[string]any{"kind": "search_unavailable", "recoverable": true},
		}, want: ToolResultUnavailable},
		{name: "typed source unavailable", value: testToolResultEnvelope{result: map[string]any{
			"sourceUnavailable": true,
			"error":             "HTTP source returned 404 Not Found.",
		}}, want: ToolResultUnavailable},
		{name: "stop reason", value: map[string]any{"stopReason": "source_unavailable"}, want: ToolResultUnavailable},
		{name: "all sources unavailable", value: map[string]any{"sources": []any{
			map[string]any{"status": "sourceUnavailable"}, map[string]any{"status": "error"},
		}}, want: ToolResultUnavailable},
		{name: "mixed sources partial", value: map[string]any{"sources": []any{
			map[string]any{"status": "fetched"}, map[string]any{"status": "sourceUnavailable"},
		}}, want: ToolResultPartial},
		{name: "partial errors", value: map[string]any{"ok": true, "errors": []any{"one"}}, want: ToolResultPartial},
		{name: "recoverable failed envelope", value: map[string]any{
			"ok": false, "partial": true, "status": "partial", "recovery": "retry the failed subset",
		}, want: ToolResultPartial},
		{name: "nil error is success", value: map[string]any{"ok": true, "error": nil}, want: ToolResultSucceeded},
		{name: "nested payload false is not envelope", value: map[string]any{"ok": true, "result": map[string]any{
			"ok": true, "records": []any{map[string]any{"success": false}},
		}}, want: ToolResultSucceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyToolResult(test.value); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNonExecutingPreflightIsDistinctFromExecutionFailure(t *testing.T) {
	preflight := map[string]any{
		"ok": false, "executed": false, "status": "python_syntax_preflight_required",
		"message": "Correct syntax before execution.",
	}
	if !IsNonExecutingPreflight(preflight) || ClassifyToolResult(preflight) != ToolResultFailed {
		t.Fatalf("preflight classification=%s recognized=%t", ClassifyToolResult(preflight), IsNonExecutingPreflight(preflight))
	}
	if !IsNonExecutingPreflight(map[string]any{
		"ok": false, "executed": false, "code": "python_syntax_preflight_required",
		"message": "Correct syntax before execution.",
	}) {
		t.Fatal("code-only non-executing preflight was not recognized")
	}
	if !IsNonExecutingPreflight(map[string]any{
		"ok": false, "executed": false, "preflight": true,
		"error": "the retry guard rejected this call before execution",
	}) {
		t.Fatal("explicit guard preflight marker was not recognized")
	}
	for _, value := range []any{
		map[string]any{"ok": false, "executed": true, "status": "python_syntax_preflight_required", "message": "failed"},
		map[string]any{"ok": false, "executed": false, "status": "failed", "message": "failed"},
		map[string]any{"ok": false, "executed": false, "status": "code_preflight_required"},
		map[string]any{"ok": false, "executed": false, "preflight": true},
	} {
		if IsNonExecutingPreflight(value) {
			t.Fatalf("execution failure mistaken for preflight: %#v", value)
		}
	}
}

func TestToolFailureEventMessageExposesOnlySafeFailureCode(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "top-level code",
			value: map[string]any{"ok": false, "code": "managed_environment_binary_abi_incompatible", "details": "private"},
			want:  "tool result reported failure: managed_environment_binary_abi_incompatible",
		},
		{
			name:  "nested code",
			value: map[string]any{"ok": false, "error": map[string]any{"code": "bundled_environment_immutable", "message": "private"}},
			want:  "tool result reported failure: bundled_environment_immutable",
		},
		{
			name:  "unsafe code falls back",
			value: map[string]any{"ok": false, "code": "bad code: /private/path"},
			want:  "tool result reported failure",
		},
		{name: "plain error falls back", value: map[string]any{"ok": false, "error": "private diagnostic"}, want: "tool result reported failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ToolFailureEventMessage(test.value); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}
