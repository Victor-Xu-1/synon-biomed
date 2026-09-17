package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"synon-go/internal/agentruntime"
)

type durableInlineResultModel struct {
	calls int
}

func (model *durableInlineResultModel) Complete(
	_ context.Context,
	_ agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.calls++
	if model.calls == 1 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant",
			ToolCalls: []agentruntime.ToolCall{{
				ID: "policy-json-output", Name: "python", Arguments: json.RawMessage(`{"code":"write json"}`),
			}},
		}}, nil
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "continued after recoverable workspace policy result",
	}}, nil
}

func TestDurableInlineKernelResultOverridesTransientLiveValue(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"ok": false, "status": "partial", "partial": true, "recoverable": true,
		"stderr": "JSON output was removed",
		"workspace_policy_violations": []map[string]any{{
			"code": "json_workspace_file_forbidden", "path": "result.json",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	model := &durableInlineResultModel{}
	engine := agentruntime.Engine{
		Model: model,
		Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
			result := agentruntime.ToolResult{
				Value: map[string]any{"ok": false, "error": "transient reconstructed value"},
				Materialized: &agentruntime.MaterializedToolResult{
					JSON: raw, SHA256: hex.EncodeToString(digest[:]), Outcome: agentruntime.ToolResultFailed,
				},
			}
			bindDurableInlineToolResultValue(&result)
			return result, nil
		}),
		MaxToolResultBytes: 1 << 20,
	}

	run, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "generate a scientific report"}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("recoverable materialized result stopped the task: %v", err)
	}
	if model.calls != 2 || run.FinalMessage.Content != "continued after recoverable workspace policy result" {
		t.Fatalf("model calls=%d final=%#v", model.calls, run.FinalMessage)
	}
}

func TestDurableInlinePythonModuleFailureReturnsToModelForCorrection(t *testing.T) {
	raw := json.RawMessage(`{"cell_index":1,"code":"python_module_not_found","dropped_roots":null,"exec_id":"exec-module-failure","exit_status":"error","files_written":null,"kernel_id":"kernel-module-failure","kernel_kind":"analysis","kernel_reused":false,"ok":false,"stderr":"Traceback (most recent call last):\n  File \"\u003ckernel:1\u003e\", line 1, in \u003cmodule\u003e\n    import missing_distribution_name\nModuleNotFoundError: No module named 'missing_distribution_name'\n","stdout":"","tool_use_id":"agent-module-failure"}`)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, reencoded) {
		t.Logf("raw=%s", raw)
		t.Logf("reencoded=%s", reencoded)
	}
	digest := sha256.Sum256(raw)
	model := &durableInlineResultModel{}
	engine := agentruntime.Engine{
		Model: model,
		Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
			result := agentruntime.ToolResult{
				Value: map[string]any{"ok": false, "error": "transient kernel failure"},
				Materialized: &agentruntime.MaterializedToolResult{
					JSON: raw, SHA256: hex.EncodeToString(digest[:]), Outcome: agentruntime.ToolResultFailed,
				},
			}
			bindDurableInlineToolResultValue(&result)
			return result, nil
		}),
		MaxToolResultBytes: 50_000,
	}

	run, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "convert the existing structure"}},
		MaxToolRounds: 2,
	})
	if err != nil {
		var infrastructureErr *agentruntime.LargeToolResultInfrastructureError
		if errors.As(err, &infrastructureErr) {
			t.Fatalf("module failure stopped the correction turn: code=%s cause=%v", infrastructureErr.ReasonCode(), errors.Unwrap(infrastructureErr))
		}
		t.Fatalf("module failure stopped the correction turn: %v", err)
	}
	if model.calls != 2 || run.FinalMessage.Content != "continued after recoverable workspace policy result" {
		t.Fatalf("model calls=%d final=%#v", model.calls, run.FinalMessage)
	}
}
