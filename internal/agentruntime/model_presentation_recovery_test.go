package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type malformedProgressActionModel struct {
	rounds      int
	malformed   string
	native      bool
	invalidArgs bool
	cancel      context.CancelFunc
}

func (model *malformedProgressActionModel) Complete(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	return model.CompleteStream(ctx, request, nil)
}

func (model *malformedProgressActionModel) CompleteStream(ctx context.Context, _ ModelRequest, emit func(ModelStreamEvent) error) (ModelResponse, error) {
	model.rounds++
	if model.rounds > 5 {
		return ModelResponse{Message: Message{Role: "assistant", Content: "Verified complete."}}, nil
	}
	if emit != nil {
		event := ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: model.malformed}
		if model.native {
			event = ModelStreamEvent{Kind: ModelStreamEventPublicProgressDelta, BlockID: "../unsafe", ContentDelta: "must stay private", ReasoningActive: true}
		}
		if err := emit(event); err != nil {
			return ModelResponse{}, err
		}
	}
	if model.cancel != nil {
		model.cancel()
	}
	args := json.RawMessage(fmt.Sprintf("{\"step\":%d}", model.rounds))
	if model.invalidArgs {
		args = json.RawMessage("{broken")
	}
	return ModelResponse{Message: Message{Role: "assistant", Content: model.malformed, ToolCalls: []ToolCall{{ID: fmt.Sprintf("action-%d", model.rounds), Name: "inspect", Arguments: args}}}}, nil
}

type nonStreamingProgressActionModel struct{ model *malformedProgressActionModel }

func (model nonStreamingProgressActionModel) Complete(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	return model.model.Complete(ctx, request)
}

func TestMalformedOptionalProgressDoesNotInterruptValidActions(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		for _, malformed := range []string{
			PublicProgressEnvelopeBegin + "{broken}" + PublicProgressEnvelopeEnd,
			PublicProgressEnvelopeBegin + "{unfinished",
			PublicProgressEnvelopeEnd,
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, malformed), func(t *testing.T) {
				model := &malformedProgressActionModel{malformed: malformed}
				var client ModelClient = model
				if !streaming {
					client = nonStreamingProgressActionModel{model: model}
				}
				calls, diagnostics := 0, 0
				engine := Engine{Model: client, Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
					calls++
					if call.ID != fmt.Sprintf("action-%d", calls) || string(call.Arguments) != fmt.Sprintf("{\"step\":%d}", calls) {
						t.Fatalf("changed action: %#v", call)
					}
					return ToolResult{Value: map[string]any{"ok": true}}, nil
				}), OnEvent: func(event Event) {
					if event.Type == EventPresentationDiagnostic {
						diagnostics++
					}
				}}
				result, err := engine.Run(context.Background(), RunRequest{
					Messages: []Message{{Role: "user", Content: "Complete the checks."}},
					Tools:    []ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}, MaxToolRounds: 8,
				})
				if err != nil || calls != 5 || diagnostics != 5 || result.FinalMessage.Content != "Verified complete." {
					t.Fatalf("calls=%d diagnostics=%d final=%q err=%v", calls, diagnostics, result.FinalMessage.Content, err)
				}
			})
		}
	}
}

func TestMalformedOptionalProgressDoesNotSuppressCancellationOrPublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := &malformedProgressActionModel{malformed: PublicProgressEnvelopeBegin + "{broken}", cancel: cancel}
	engine := Engine{Model: model}
	if _, _, err := engine.completeModelRound(ctx, ModelRequest{}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	cause := publicProgressPresentationError("downstream sentinel failure")
	model = &malformedProgressActionModel{malformed: "Ordinary progress."}
	engine = Engine{Model: model, OnModelDelta: func(ModelStreamEvent) error { return cause }}
	if _, _, err := engine.completeModelRound(context.Background(), ModelRequest{}, false); !errors.Is(err, cause) {
		t.Fatalf("publication error swallowed: %v", err)
	}
}

func TestMalformedOptionalProgressStillRejectsUnsafeArgumentsAndPrivateText(t *testing.T) {
	model := &malformedProgressActionModel{native: true, invalidArgs: true}
	calls := 0
	var visible strings.Builder
	engine := Engine{Model: model, Tools: FuncToolGateway(func(context.Context, ToolCall) (ToolResult, error) { calls++; return ToolResult{}, nil }),
		OnModelDelta: func(event ModelStreamEvent) error { visible.WriteString(event.ContentDelta); return nil }}
	_, _ = engine.Run(context.Background(), RunRequest{Tools: []ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}, MaxToolRounds: 2})
	if calls != 0 || strings.Contains(visible.String(), "must stay private") {
		t.Fatalf("unsafe side effect or private leak: calls=%d visible=%q", calls, visible.String())
	}
}
