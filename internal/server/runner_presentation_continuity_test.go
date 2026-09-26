package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"synon-go/internal/agentruntime"
)

// Broken optional progress must not discard a valid, independently validated
// action. More than the old correction budget proves this is not just a retry.
type continuityProgressModel struct{ rounds int }

func (model *continuityProgressModel) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return model.CompleteStream(ctx, request, nil)
}

func (model *continuityProgressModel) CompleteStream(ctx context.Context, _ agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.ModelResponse{}, err
	}
	model.rounds++
	if model.rounds > 5 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "五个步骤均已完成。"}}, nil
	}
	if emit != nil {
		// Missing block identity and then an unmatched boundary used to return
		// an error before the provider could deliver the already selected action.
		for _, event := range []agentruntime.ModelStreamEvent{
			{Kind: agentruntime.ModelStreamEventPublicProgressDelta, ContentDelta: "invalid progress"},
			{Kind: agentruntime.ModelStreamEventPublicProgressBoundary, BlockID: "absent"},
		} {
			if err := emit(event); err != nil {
				return agentruntime.ModelResponse{}, err
			}
		}
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: fmt.Sprintf("step-%d", model.rounds), Name: "inspect", Arguments: json.RawMessage(fmt.Sprintf(`{"step":%d}`, model.rounds)),
	}}}}, nil
}

func TestPresentationMalformedProgressDoesNotStopSubsequentActions(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		t.Run(language, func(t *testing.T) {
			checkMalformedProgressContinuity(t, language)
		})
	}
}

func checkMalformedProgressContinuity(t *testing.T, language string) {
	model := &continuityProgressModel{}
	audits, calls := 0, 0
	client := &sessionRunnerResponseLanguageModelClient{delegate: model, language: language, audit: func(record map[string]any) {
		if record["decision"] == "progress_presentation_discarded" {
			audits++
		}
	}}
	engine := agentruntime.Engine{Model: client, Tools: agentruntime.FuncToolGateway(func(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		calls++
		if call.ID != fmt.Sprintf("step-%d", calls) || string(call.Arguments) != fmt.Sprintf(`{"step":%d}`, calls) {
			t.Fatalf("action changed or repeated: %#v", call)
		}
		return agentruntime.ToolResult{Value: map[string]any{"ok": true, "step": calls}}, nil
	})}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "完成五步检查"}},
		Tools:         []agentruntime.ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object", "properties": map[string]any{"step": map[string]any{"type": "integer"}}, "additionalProperties": false}}},
		MaxToolRounds: 8,
	})
	if err != nil || calls != 5 || model.rounds != 6 || audits != 5 || result.FinalMessage.Content != "五个步骤均已完成。" {
		t.Fatalf("calls=%d rounds=%d audits=%d final=%q err=%v", calls, model.rounds, audits, result.FinalMessage.Content, err)
	}
}

func TestPresentationContinuationStillPropagatesCancellationAndPublicationFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("durable progress publication failed")} {
		client := &sessionRunnerResponseLanguageModelClient{language: "zh", delegate: responseLanguageFixtureModel{
			events:   []agentruntime.ModelStreamEvent{{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: "正在检查已保存的文件。"}},
			response: agentruntime.ModelResponse{Message: agentruntime.Message{ToolCalls: []agentruntime.ToolCall{{ID: "must-not-run", Name: "inspect"}}}},
		}}
		response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(agentruntime.ModelStreamEvent) error { return cause })
		if !errors.Is(err, cause) || len(response.Message.ToolCalls) != 0 {
			t.Fatalf("publication failure released action: %#v err=%v", response, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &sessionRunnerResponseLanguageModelClient{language: "zh", delegate: &continuityProgressModel{}}
	if _, err := client.CompleteStream(ctx, agentruntime.ModelRequest{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request continued: %v", err)
	}
}

func TestPresentationUnclosedProgressKeepsFinalAnswer(t *testing.T) {
	client := &sessionRunnerResponseLanguageModelClient{language: "zh", delegate: responseLanguageFixtureModel{
		events:   []agentruntime.ModelStreamEvent{{Kind: agentruntime.ModelStreamEventPublicProgressDelta, BlockID: "unfinished", ContentDelta: "不可发布的进度"}},
		response: agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "这是已核对的最终答复。"}},
	}}
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, nil)
	if err != nil || response.Message.Content != "这是已核对的最终答复。" {
		t.Fatalf("optional progress suppressed final answer: %#v err=%v", response, err)
	}
}
