package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type repeatedReadModel struct {
	requests int
}

func (m *repeatedReadModel) Complete(_ context.Context, _ ModelRequest) (ModelResponse, error) {
	m.requests++
	return ModelResponse{
		Message: Message{
			Role:    "assistant",
			Content: "I am still reading the same source.",
			ToolCalls: []ToolCall{{
				ID: "read-" + string(rune('0'+m.requests)), Name: "web_fetch",
				Arguments: json.RawMessage(`{"url":"https://example.org/source"}`),
			}},
		},
	}, nil
}

type reusedReadGateway struct {
	calls int
}

func (g *reusedReadGateway) Execute(_ context.Context, _ ToolCall) (ToolResult, error) {
	g.calls++
	return ToolResult{Value: map[string]any{
		"ok": true, "reused": true, "result": map[string]any{"body": "same evidence"},
	}}, nil
}

func TestEngineBoundsNarratedRepeatedReadRounds(t *testing.T) {
	model := &repeatedReadModel{}
	gateway := &reusedReadGateway{}
	engine := Engine{Model: model, Tools: gateway}
	_, err := engine.Run(context.Background(), RunRequest{
		Messages:                          []Message{{Role: "user", Content: "review the source"}},
		Tools:                             []ToolSchema{{Name: "web_fetch"}},
		MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 {
		t.Fatalf("repeated narrated reads error=%v", err)
	}
	if model.requests != 3 || gateway.calls != 3 {
		t.Fatalf("repeated narrated reads requests=%d gateway_calls=%d", model.requests, gateway.calls)
	}
}

func TestToolResultReportsNoProgressOnlyForExplicitReuseMarker(t *testing.T) {
	if !toolResultReportsNoProgress(map[string]any{"ok": true, "reused": true}) {
		t.Fatal("explicit reuse marker was not recognized")
	}
	if toolResultReportsNoProgress(map[string]any{"ok": true, "kernel_reused": true}) {
		t.Fatal("a reused execution kernel was mistaken for a reused tool result")
	}
	if toolResultReportsNoProgress(map[string]any{"ok": true, "body": "fresh"}) {
		t.Fatal("fresh result was incorrectly treated as no progress")
	}
}

func TestToolResultReportsNoProgressRequiresExplicitEffect(t *testing.T) {
	if toolResultReportsNoProgress(map[string]any{"ok": true, "idempotent": true}) {
		t.Fatal("an idempotency property was treated as proof that this invocation made no progress")
	}
	if !toolResultReportsNoProgress(map[string]any{
		"ok":     true,
		"effect": map[string]any{"schema": "synon.tool_effect.v1", "state": "unchanged"},
	}) {
		t.Fatal("an explicit unchanged effect was not recognized")
	}
	if toolResultReportsNoProgress(map[string]any{
		"ok":     true,
		"effect": map[string]any{"schema": "synon.tool_effect.v1", "state": "changed"},
	}) {
		t.Fatal("a changed effect was treated as no progress")
	}
}

type cosmeticIdempotentUpdateModel struct {
	requests int
}

func (m *cosmeticIdempotentUpdateModel) Complete(_ context.Context, _ ModelRequest) (ModelResponse, error) {
	m.requests++
	arguments, _ := json.Marshal(map[string]any{
		"step":              "current-step",
		"status":            "in_progress",
		"human_description": "heartbeat " + string(rune('0'+m.requests)),
	})
	return ModelResponse{Message: Message{
		Role:    "assistant",
		Content: "I am still working.",
		ToolCalls: []ToolCall{{
			ID: "status-" + string(rune('0'+m.requests)), Name: "state_transition", Arguments: arguments,
		}},
	}}, nil
}

type idempotentUpdateGateway struct {
	calls int
}

func (g *idempotentUpdateGateway) Execute(_ context.Context, _ ToolCall) (ToolResult, error) {
	g.calls++
	return ToolResult{Value: map[string]any{
		"ok": true, "idempotent": true,
		"effect": map[string]any{"schema": "synon.tool_effect.v1", "state": "unchanged"},
	}}, nil
}

func TestEngineBoundsCosmeticIdempotentUpdates(t *testing.T) {
	model := &cosmeticIdempotentUpdateModel{}
	gateway := &idempotentUpdateGateway{}
	_, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages:                          []Message{{Role: "user", Content: "finish the current work"}},
		Tools:                             []ToolSchema{{Name: "state_transition"}},
		MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 {
		t.Fatalf("cosmetic idempotent updates error=%v", err)
	}
	if len(noProgress.Calls) != 3 {
		t.Fatalf("no-progress recovery lost bounded execution identities: %#v", noProgress.Calls)
	}
	fingerprint := ExecutionCallFingerprint(noProgress.Calls[0].Name, noProgress.Calls[0].Arguments)
	for _, call := range noProgress.Calls[1:] {
		if ExecutionCallFingerprint(call.Name, call.Arguments) != fingerprint {
			t.Fatalf("presentation-only labels changed semantic execution identity: %#v", noProgress.Calls)
		}
	}
	if model.requests != 3 || gateway.calls != 3 {
		t.Fatalf("cosmetic idempotent updates requests=%d gateway_calls=%d", model.requests, gateway.calls)
	}
}

type convergingReadModel struct {
	requests int
	sawHint  bool
}

func (m *convergingReadModel) Complete(_ context.Context, request ModelRequest) (ModelResponse, error) {
	m.requests++
	for _, message := range request.Messages {
		if message.Role == "system" && strings.Contains(message.Content, "only idempotent receipts") {
			m.sawHint = true
		}
	}
	if m.sawHint {
		return ModelResponse{Message: Message{Role: "assistant", Content: "The requested evidence is already available."}}, nil
	}
	return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID: "read-once", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.org/source"}`),
	}}}}, nil
}

func TestEngineUsesPrivateRecoveryContextAfterIdempotentRound(t *testing.T) {
	model := &convergingReadModel{}
	gateway := &reusedReadGateway{}
	result, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages: []Message{{Role: "user", Content: "review the source"}},
		Tools:    []ToolSchema{{Name: "web_fetch"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !model.sawHint || model.requests != 2 || gateway.calls != 1 {
		t.Fatalf("recovery hint=%t requests=%d gateway_calls=%d", model.sawHint, model.requests, gateway.calls)
	}
	if result.FinalMessage.Content != "The requested evidence is already available." {
		t.Fatalf("final message=%q", result.FinalMessage.Content)
	}
}

type alternatingReusedReadModel struct {
	requests int
}

func (m *alternatingReusedReadModel) Complete(_ context.Context, _ ModelRequest) (ModelResponse, error) {
	m.requests++
	name := "web_fetch"
	arguments := json.RawMessage(`{"url":"https://example.org/source"}`)
	if m.requests%2 == 0 {
		name = "read_file"
		arguments = json.RawMessage(`{"path":"evidence.md"}`)
	}
	return ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID: "alternating-" + string(rune('0'+m.requests)), Name: name, Arguments: arguments,
	}}}}, nil
}

func TestEngineBoundsAlternatingNoProgressToolRounds(t *testing.T) {
	model := &alternatingReusedReadModel{}
	gateway := &reusedReadGateway{}
	_, err := (Engine{Model: model, Tools: gateway}).Run(context.Background(), RunRequest{
		Messages:                          []Message{{Role: "user", Content: "finish from the available evidence"}},
		Tools:                             []ToolSchema{{Name: "web_fetch"}, {Name: "read_file"}},
		MaxConsecutiveIdenticalToolRounds: 3,
	})
	var noProgress *ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 {
		t.Fatalf("alternating no-progress error=%v", err)
	}
	if model.requests != 3 || gateway.calls != 3 {
		t.Fatalf("alternating requests=%d gateway_calls=%d", model.requests, gateway.calls)
	}
}
