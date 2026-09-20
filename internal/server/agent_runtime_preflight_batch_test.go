package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/executionprep"
)

func TestGatewayBatchPreflightPreservesIndicesAndCancellation(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{server: &Server{}, allowedTools: []string{"python", "web_fetch"}}
	calls := []agentruntime.ToolCall{
		{ID: "source", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.org/source"}`)},
		{ID: "syntax", Name: "python", Arguments: json.RawMessage(`{"code":"print(f\"broken}\")"}`)},
	}
	diagnostics, err := gateway.ToolCallPreflightDiagnostics(context.Background(), calls)
	if err != nil || len(diagnostics) != 1 || !strings.Contains(diagnostics[1], "python_syntax_preflight_required") {
		t.Fatalf("diagnostics=%v error=%v", diagnostics, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if diagnostics, err := gateway.ToolCallPreflightDiagnostics(ctx, calls); !errors.Is(err, context.Canceled) || diagnostics != nil {
		t.Fatalf("cancellation became tool feedback: diagnostics=%v error=%v", diagnostics, err)
	}
	if _, err := gateway.ToolCallPreflightDiagnostics(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty batch lost cancellation: %v", err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := gateway.ToolCallPreflightDiagnostics(deadline, calls); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

type contextWitnessPreparer struct {
	parent context.Context
	t      *testing.T
}

func (preparer contextWitnessPreparer) PrepareExecutionSource(ctx context.Context, _ executionprep.Request) (executionprep.Result, error) {
	preparer.t.Helper()
	if !errors.Is(ctx.Err(), preparer.parent.Err()) {
		preparer.t.Fatalf("preparation detached from cancellation: %v", ctx.Err())
	}
	deadline, ok := ctx.Deadline()
	parentDeadline, _ := preparer.parent.Deadline()
	if !ok || deadline.After(parentDeadline) {
		preparer.t.Fatal("preparation extended the parent deadline")
	}
	return executionprep.Result{}, ctx.Err()
}

func TestExecutionPreparationInheritsCancellationAndEarlierDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	_ = agentExecutionPreparationPreflight(parent, "python", map[string]any{"code": "print(1)"}, nil, contextWitnessPreparer{parent: parent, t: t})
}

type gatewayBatchPreflightModel struct{ requests int }

func (model *gatewayBatchPreflightModel) Complete(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.requests++
	if model.requests == 1 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{
			{ID: "syntax", Name: "python", Arguments: json.RawMessage(`{"code":"print(f\"broken}\")"}`)},
		}}}, nil
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "No execution was required."}}, nil
}

func TestEngineUsesServerBatchPreflightBeforeToolLifecycle(t *testing.T) {
	model := &gatewayBatchPreflightModel{}
	gateway := serverAgentRuntimeToolGateway{server: &Server{}, allowedTools: []string{"python"}}
	toolStarts := 0
	engine := agentruntime.Engine{Model: model, Tools: gateway, OnEvent: func(event agentruntime.Event) {
		if event.Type == agentruntime.EventToolStarted {
			toolStarts++
		}
	}}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "Check the existing task."}},
		Tools:    []agentruntime.ToolSchema{{Name: "python"}},
	})
	if err != nil || toolStarts != 0 || model.requests != 2 || result.FinalMessage.Content != "No execution was required." {
		t.Fatalf("err=%v starts=%d requests=%d result=%#v", err, toolStarts, model.requests, result)
	}
}
