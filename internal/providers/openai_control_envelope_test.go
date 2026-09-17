package providers

import (
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestNormalizeOpenAITextControlEnvelopeRecoversCompletionOutput(t *testing.T) {
	message, err := normalizeOpenAITextControlEnvelope(agentruntime.Message{
		Role:    "assistant",
		Content: `"\u003c|FunctionCallBegin|\u003e[{\"name\":\"complete_task\",\"parameters\":{\"output\":\"任务已完成。\"}}]\u003c|FunctionCallEnd|\u003e"`,
	})
	if err != nil {
		t.Fatalf("normalize envelope: %v", err)
	}
	if message.Content != "任务已完成。" {
		t.Fatalf("content = %q", message.Content)
	}
}

func TestNormalizeOpenAITextControlEnvelopeSuppressesCompletionBesideNativeCalls(t *testing.T) {
	message, err := normalizeOpenAITextControlEnvelope(agentruntime.Message{
		Role:    "assistant",
		Content: `<|FunctionCallBegin|>[{"name":"complete_task","parameters":{"output":"done"}}]<|FunctionCallEnd|>`,
		ToolCalls: []agentruntime.ToolCall{{
			ID: "call-1", Name: "read_file",
		}},
	})
	if err != nil {
		t.Fatalf("normalize envelope: %v", err)
	}
	if message.Content != "" || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %#v", message)
	}
}

func TestNormalizeOpenAITextControlEnvelopeRejectsTextualToolCalls(t *testing.T) {
	_, err := normalizeOpenAITextControlEnvelope(agentruntime.Message{
		Role:    "assistant",
		Content: `<|FunctionCallBegin|>[{"name":"read_file","parameters":{"path":"x"}}]<|FunctionCallEnd|>`,
	})
	if err == nil || !strings.Contains(err.Error(), "non-native textual tool call") {
		t.Fatalf("err = %v", err)
	}
}
