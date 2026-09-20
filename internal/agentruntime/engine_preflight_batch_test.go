package agentruntime

import (
	"context"
	"errors"
	"testing"
)

type batchPreflightGateway struct {
	diagnostics        map[int]string
	err                error
	preflights         int
	legacyCalls        int
	batchSize          int
	executed           []string
	cancel             context.CancelFunc
	ignoreCancellation bool
}

// A legacy implementation must not become a second admission authority.
func (g *batchPreflightGateway) ToolCallPreflightDiagnostic(ToolCall) string {
	g.legacyCalls++
	return ""
}

func (g *batchPreflightGateway) ToolCallPreflightDiagnostics(ctx context.Context, calls []ToolCall) (map[int]string, error) {
	g.preflights++
	g.batchSize = len(calls)
	if g.cancel != nil {
		g.cancel()
	}
	if err := ctx.Err(); err != nil && !g.ignoreCancellation {
		return nil, err
	}
	return g.diagnostics, g.err
}

func (g *batchPreflightGateway) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	g.executed = append(g.executed, call.ID)
	return ToolResult{Value: map[string]any{"ok": true}}, nil
}

func TestEngineBatchPreflightBeforeAnyPublication(t *testing.T) {
	sentinel := errors.New("receipt authority unavailable")
	for _, test := range []struct {
		name               string
		diagnostics        map[int]string
		err                error
		cancel             bool
		ignoreCancellation bool
	}{
		{name: "authority failure", err: sentinel},
		{name: "canceled receipt lookup", cancel: true},
		{name: "lookup returns after cancellation", cancel: true, ignoreCancellation: true},
		{name: "invalid positive index", diagnostics: map[int]string{2: "invalid"}},
		{name: "invalid negative index", diagnostics: map[int]string{-1: "invalid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gateway := &batchPreflightGateway{diagnostics: test.diagnostics, err: test.err, ignoreCancellation: test.ignoreCancellation}
			if test.cancel {
				gateway.cancel = cancel
			}
			model := &capturingRequestModelClient{responses: []ModelResponse{
				{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "first", Name: "repl", Arguments: []byte(`{}`)}, {ID: "second", Name: "repl", Arguments: []byte(`{}`)}}}},
				{Message: Message{Role: "assistant", Content: "finished"}},
			}}
			published := 0
			_, err := (Engine{Model: model, Tools: gateway, OnEvent: func(event Event) {
				if event.Type == EventModelResponse || event.Type == EventToolStarted {
					published++
				}
			}}).Run(ctx, RunRequest{Messages: []Message{{Role: "user", Content: "Complete the existing task"}}, Tools: []ToolSchema{{Name: "repl"}}})
			if err == nil {
				t.Fatal("preflight authority failure was ignored")
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("lost typed error: %v", err)
			}
			if test.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if gateway.preflights != 1 || gateway.batchSize != 2 || gateway.legacyCalls != 0 || len(gateway.executed) != 0 || published != 0 {
				t.Fatalf("preflights=%d size=%d legacy=%d executed=%v published=%d", gateway.preflights, gateway.batchSize, gateway.legacyCalls, gateway.executed, published)
			}
		})
	}
}

func TestEngineBatchPreflightKeepsExactCallFeedback(t *testing.T) {
	gateway := &batchPreflightGateway{diagnostics: map[int]string{1: `{"code":"durable_no_progress_route_closed","message":"An unchanged execution is already complete.","recovery":"Choose a materially different action."}`}}
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "unadvertised", Name: "unknown", Arguments: []byte(`{}`)},
			{ID: "malformed", Name: "repl", Arguments: []byte(`{}`), ProviderProtocolDiagnostic: "invalid provider arguments"},
			{ID: "allowed", Name: "repl", Arguments: []byte(`{"value":1}`)},
			{ID: "closed", Name: "repl", Arguments: []byte(`{"value":2}`)},
		}}},
		{Message: Message{Role: "assistant", Content: "finished"}},
	}}
	_, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Complete the existing task"}}, Tools: []ToolSchema{{Name: "repl"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gateway.preflights != 1 || gateway.batchSize != 2 || gateway.legacyCalls != 0 || len(gateway.executed) != 1 || gateway.executed[0] != "allowed" {
		t.Fatalf("gateway=%#v", gateway)
	}
	if len(model.requests) != 2 {
		t.Fatalf("requests=%d", len(model.requests))
	}
	found := false
	for _, message := range model.requests[1].Messages {
		if message.Role == "tool" && message.ToolCallID == "closed" {
			found = true
		}
	}
	if !found {
		t.Fatal("rejected exact call lost its normal tool feedback")
	}
}
