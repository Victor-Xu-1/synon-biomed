package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"synon-go/internal/toolprogress"
)

func TestEngineKeepsLongToolRunningAndEmitsProgress(t *testing.T) {
	model := &staticModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "long-tool-call", Name: "long_tool", Arguments: []byte(`{"seconds":1}`),
		}}}},
		{Message: Message{Role: "assistant", Content: "long tool completed"}},
	}}
	var events []Event
	engine := Engine{
		Model:                model,
		ToolProgressInterval: 5 * time.Millisecond,
		Tools: FuncToolGateway(func(ctx context.Context, call ToolCall) (ToolResult, error) {
			if call.Name != "long_tool" {
				t.Fatalf("tool name = %q", call.Name)
			}
			timer := time.NewTimer(35 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				return ToolResult{Value: map[string]any{"ok": true, "status": "completed"}}, nil
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		}),
		OnEventError: func(event Event) error {
			if event.Type == EventToolProgress {
				return errors.New("simulated progress checkpoint outage")
			}
			return nil
		},
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	}

	result, err := engine.Run(context.Background(), RunRequest{
		Messages:      []Message{{Role: "user", Content: "run the long tool"}},
		Tools:         []ToolSchema{{Name: "long_tool", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "long tool completed" {
		t.Fatalf("final message = %#v", result.FinalMessage)
	}
	progressCount := 0
	lastOrdinal := 0
	for _, event := range events {
		if event.Type != EventToolProgress {
			continue
		}
		progressCount++
		if event.ToolName != "long_tool" || event.ToolCallID != "long-tool-call" || event.Elapsed <= 0 {
			t.Fatalf("invalid progress event = %#v", event)
		}
		if event.ProgressOrdinal <= lastOrdinal {
			t.Fatalf("progress ordinal did not increase: previous=%d current=%d", lastOrdinal, event.ProgressOrdinal)
		}
		lastOrdinal = event.ProgressOrdinal
	}
	if progressCount < 2 {
		t.Fatalf("expected at least two progress events during the long call, got %d (%#v)", progressCount, events)
	}
	if !hasEvent(events, EventToolCompleted, "long_tool") || !hasEvent(events, EventFinal, "") {
		t.Fatalf("terminal events missing after progress: %#v", events)
	}
}

func TestEnginePublishesObservedToolProgressWithoutChangingToolExecution(t *testing.T) {
	phasePercent := float64(64)
	bytesPerSecond := float64(224320)
	completed, total := int64(2), int64(8)
	var events []Event
	engine := Engine{
		ToolProgressInterval: 50 * time.Millisecond,
		Tools: FuncToolGateway(func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			toolprogress.Report(ctx, toolprogress.Update{
				Phase: "downloading_packages", PhasePercent: &phasePercent, BytesPerSecond: &bytesPerSecond,
				CompletedItems: &completed, TotalItems: &total,
			})
			return ToolResult{Value: map[string]any{"ok": true}}, nil
		}),
		OnEvent: func(event Event) { events = append(events, event) },
	}
	result, err := engine.executeToolGatewayWithProgress(context.Background(), ToolCall{
		ID: "environment-call", Name: "manage_environments", Arguments: []byte(`{"mode":"create"}`),
	})
	if err != nil || result.Value == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(events) != 1 || events[0].Progress == nil {
		t.Fatalf("events=%#v", events)
	}
	progress := events[0].Progress
	if progress.Phase != "downloading_packages" || progress.PhasePercent == nil || *progress.PhasePercent != 64 ||
		progress.BytesPerSecond == nil || *progress.BytesPerSecond != 224320 ||
		progress.CompletedItems == nil || *progress.CompletedItems != 2 ||
		progress.TotalItems == nil || *progress.TotalItems != 8 {
		t.Fatalf("progress=%#v", progress)
	}
}

func TestEngineFlushesLastObservedProgressBeforeResult(t *testing.T) {
	first := make(chan struct{})
	var phases []string
	e := Engine{ToolProgressInterval: time.Hour, OnEvent: func(event Event) {
		if event.Progress != nil {
			phases = append(phases, event.Progress.Phase)
			if len(phases) == 1 {
				close(first)
			}
		}
	}, Tools: FuncToolGateway(func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		toolprogress.Report(ctx, toolprogress.Update{Phase: "downloading_packages"})
		<-first
		toolprogress.Report(ctx, toolprogress.Update{Phase: "verifying_environment"})
		return ToolResult{Value: map[string]any{"ok": true}}, nil
	})}
	if _, err := e.executeToolGatewayWithProgress(context.Background(), ToolCall{ID: "flush", Name: "install"}); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[1] != "verifying_environment" {
		t.Fatalf("last observed phase lost: %v", phases)
	}
}
