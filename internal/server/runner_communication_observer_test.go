package server

import (
	"context"
	"encoding/json"
	"strings"
	"synon-go/internal/agentruntime"
	"testing"
)

type nativeCommunicationFixture struct {
	responses []agentruntime.ModelResponse
	calls     int
}

func (model *nativeCommunicationFixture) Complete(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	response := model.responses[model.calls]
	model.calls++
	return response, nil
}
func (model *nativeCommunicationFixture) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	response, err := model.Complete(ctx, request)
	if err != nil {
		return response, err
	}
	if emit != nil {
		if response.Message.Content != "" {
			if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: response.Message.Content}); err != nil {
				return agentruntime.ModelResponse{}, err
			}
		}
		if len(response.Message.ToolCalls) > 0 {
			if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventToolCallBoundary}); err != nil {
				return agentruntime.ModelResponse{}, err
			}
		}
	}
	return response, nil
}

func TestNativeToolPreambleDoesNotRelaxRequiredToolChoice(t *testing.T) {
	for _, allow := range []bool{false, true} {
		model := &nativeCommunicationFixture{responses: []agentruntime.ModelResponse{
			{Message: agentruntime.Message{Role: "assistant", Content: "先检查已有记录。", ToolCalls: []agentruntime.ToolCall{{ID: "wrong", Name: "other", Arguments: json.RawMessage(`{}`)}}}},
			{Message: agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "right", Name: "inspect", Arguments: json.RawMessage(`{}`)}}}},
			{Message: agentruntime.Message{Role: "assistant", Content: "检查完成。"}},
		}}
		executed := []string{}
		prose := []string{}
		engine := agentruntime.Engine{Model: model, Tools: agentruntime.FuncToolGateway(func(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
			executed = append(executed, call.Name)
			return agentruntime.ToolResult{Value: map[string]any{"ok": true}}, nil
		}), OnModelDelta: func(event agentruntime.ModelStreamEvent) error {
			if event.ContentDelta != "" {
				prose = append(prose, event.ContentDelta)
			}
			return nil
		}, AllowToolPreamble: func(text string) bool { return allow && sessionRunnerPublicProgressNarration(text) == text }}
		result, err := engine.Run(context.Background(), agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "检查记录"}}, Tools: []agentruntime.ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}, InitialToolChoice: map[string]any{"type": "tool", "name": "inspect"}, MaxToolRounds: 2})
		if err != nil || result.FinalMessage.Content != "检查完成。" || len(executed) != 1 || executed[0] != "inspect" || model.calls != 3 {
			t.Fatalf("allow=%t executed=%v calls=%d err=%v", allow, executed, model.calls, err)
		}
		count := 0
		for _, text := range prose {
			if text == "先检查已有记录。" {
				count++
			}
		}
		want := 0
		if allow {
			want = 1
		}
		if count != want {
			t.Fatalf("allow=%t preambles=%d prose=%v", allow, count, prose)
		}
	}
}

func TestCommunicationObserverNeverGeneratesOrPublishesText(t *testing.T) {
	text := "先查看已有资料。"
	model := &nativeCommunicationFixture{responses: []agentruntime.ModelResponse{{Message: agentruntime.Message{Content: text, ReasoningContent: "PRIVATE_SENTINEL"}}}}
	var record map[string]any
	bytes := 0
	observer := &sessionRunnerCommunicationObserver{delegate: model, publicationBytes: func() int { return bytes }, audit: func(value map[string]any) { record = value }}
	streamed := ""
	response, err := observer.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error { streamed += event.ContentDelta; return nil })
	bytes = len(text)
	observer.recordBoundary(false)
	encoded, _ := json.Marshal(record)
	if err != nil || model.calls != 1 || streamed != text || response.Message.Content != text || record["published_bytes"] != len(text) || strings.Contains(string(encoded), text) || strings.Contains(string(encoded), "PRIVATE_SENTINEL") {
		t.Fatalf("calls=%d streamed=%q audit=%s err=%v", model.calls, streamed, encoded, err)
	}
}
