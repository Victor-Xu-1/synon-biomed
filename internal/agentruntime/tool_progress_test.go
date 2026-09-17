package agentruntime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestFailedToolCallGuardBlocksRepeatedNoOpMutation(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(
		FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			executions.Add(1)
			return ToolResult{Value: map[string]any{"ok": true, "changed": false}}, nil
		}),
		ToolSchema{Name: "edit_file", Capabilities: []string{"artifact-write"}},
	)
	first := ToolCall{Name: "edit_file", Arguments: json.RawMessage(
		`{"file_path":"evidence.csv","old_string":"same","new_string":"same","human_description":"first"}`,
	)}
	if result, err := guard.Execute(context.Background(), first); err != nil || ClassifyToolResult(result.Value) != ToolResultSucceeded {
		t.Fatalf("first no-op mutation=%#v err=%v", result.Value, err)
	}
	second := first
	second.Arguments = json.RawMessage(
		`{"file_path":"evidence.csv","old_string":"same","new_string":"same","human_description":"renamed"}`,
	)
	result, err := guard.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	value := mapValueForTest(t, result.Value)
	if value["code"] != "repeated_non_progressing_tool_call" || value["executed"] != false || executions.Load() != 1 {
		t.Fatalf("repeated no-op mutation=%#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardBlocksRepeatedUnchangedPublication(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(
		FuncToolGateway(func(_ context.Context, _ ToolCall) (ToolResult, error) {
			executions.Add(1)
			return ToolResult{Value: map[string]any{
				"ok":                 true,
				"completion_pending": true,
				"artifacts":          []any{map[string]any{"filename": "evidence.csv", "unchanged": true}},
			}}, nil
		}),
		ToolSchema{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}},
	)
	first := ToolCall{Name: "save_artifacts", Arguments: json.RawMessage(
		`{"files":["evidence.csv"],"human_description":"save corrected evidence"}`,
	)}
	if result, err := guard.Execute(context.Background(), first); err != nil || ClassifyToolResult(result.Value) != ToolResultSucceeded {
		t.Fatalf("first unchanged publication=%#v err=%v", result.Value, err)
	}
	second := first
	second.Arguments = json.RawMessage(
		`{"files":["evidence.csv"],"human_description":"final corrected evidence"}`,
	)
	result, err := guard.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	value := mapValueForTest(t, result.Value)
	if value["code"] != "repeated_non_progressing_tool_call" || value["executed"] != false || executions.Load() != 1 {
		t.Fatalf("repeated unchanged publication=%#v executions=%d", value, executions.Load())
	}
}

func TestFailedToolCallGuardDoesNotReopenFailureAfterRuntimeReportsNoWorkspaceWrites(t *testing.T) {
	var executions atomic.Int64
	guard := newFailedToolCallGuard(
		FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
			executions.Add(1)
			if call.Name == "save_artifacts" {
				return ToolResult{Value: map[string]any{
					"ok": false, "code": "invalid_delimited_artifact", "retryable": false,
				}}, nil
			}
			return ToolResult{Value: map[string]any{
				"ok": true, "stdout": "analysis state updated", "files_written": []any{},
			}}, nil
		}),
		ToolSchema{Name: "save_artifacts", Capabilities: []string{"artifact-publication"}},
		ToolSchema{Name: "repl", Capabilities: []string{"runtime-execution"}},
	)
	save := ToolCall{Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["evidence.csv"]}`)}
	if _, err := guard.Execute(context.Background(), save); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Execute(context.Background(), ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"state = 1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	retry, err := guard.Execute(context.Background(), save)
	if err != nil {
		t.Fatal(err)
	}
	value := mapValueForTest(t, retry.Value)
	if value["code"] != "repeated_failed_tool_call" || executions.Load() != 2 {
		t.Fatalf("empty workspace-write report reopened failed save: result=%#v executions=%d", value, executions.Load())
	}
	repeatedRuntime, err := guard.Execute(context.Background(), ToolCall{
		Name: "repl", Arguments: json.RawMessage(`{"code":"state = 1"}`),
	})
	if err != nil || ClassifyToolResult(repeatedRuntime.Value) != ToolResultSucceeded || executions.Load() != 3 {
		t.Fatalf("analysis-only runtime effect was globally classified as no-progress: result=%#v err=%v executions=%d", repeatedRuntime.Value, err, executions.Load())
	}
}

func TestWorkspaceMutationReportFailsOpenWhenScanOrShapeIsIncomplete(t *testing.T) {
	for name, value := range map[string]any{
		"dropped workspace root": map[string]any{
			"ok": true, "files_written": []any{}, "dropped_roots": []any{"."},
		},
		"malformed file report": map[string]any{
			"ok": true, "files_written": "unknown",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !workspaceMutationCommitted(
				"repl", []string{"runtime-execution"}, ToolResultSucceeded, value,
			) {
				t.Fatalf("incomplete workspace observation suppressed a possible mutation: %#v", value)
			}
		})
	}
}
