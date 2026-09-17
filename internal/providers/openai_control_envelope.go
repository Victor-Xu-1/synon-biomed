package providers

import (
	"encoding/json"
	"errors"
	"strings"

	"synon-go/internal/agentruntime"
)

const (
	openAITextControlBegin = "<|FunctionCallBegin|>"
	openAITextControlEnd   = "<|FunctionCallEnd|>"
)

type openAITextControlCall struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
}

// normalizeOpenAITextControlEnvelope repairs one provider-compatibility edge:
// some OpenAI-compatible endpoints serialize their internal completion signal
// into assistant text instead of returning a native tool call. Only the exact
// whole-response completion envelope is safe to recover. All other textual
// function calls are rejected so they can never leak to users or bypass the
// normal tool dispatcher.
func normalizeOpenAITextControlEnvelope(message agentruntime.Message) (agentruntime.Message, error) {
	content := strings.TrimSpace(message.Content)
	decoded := content
	for range 3 {
		var unquoted string
		if err := json.Unmarshal([]byte(decoded), &unquoted); err != nil || unquoted == decoded {
			break
		}
		decoded = strings.TrimSpace(unquoted)
	}

	// A few compatible endpoints leave JSON unicode escapes outside a quoted
	// JSON string. Decode only the delimiters needed for protocol recognition.
	decoded = strings.ReplaceAll(decoded, `\u003c`, "<")
	decoded = strings.ReplaceAll(decoded, `\u003e`, ">")
	if !strings.Contains(decoded, openAITextControlBegin) && !strings.Contains(decoded, openAITextControlEnd) {
		return message, nil
	}
	if !strings.HasPrefix(decoded, openAITextControlBegin) || !strings.HasSuffix(decoded, openAITextControlEnd) {
		return agentruntime.Message{}, newRetryableModelProtocolError(errors.New("OpenAI-compatible response contains a malformed textual function-call envelope"))
	}

	payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(decoded, openAITextControlBegin), openAITextControlEnd))
	var calls []openAITextControlCall
	if err := json.Unmarshal([]byte(payload), &calls); err != nil || len(calls) != 1 {
		return agentruntime.Message{}, newRetryableModelProtocolError(errors.New("OpenAI-compatible response contains an invalid textual function-call envelope"))
	}
	if strings.TrimSpace(calls[0].Name) != "complete_task" {
		return agentruntime.Message{}, newRetryableModelProtocolError(errors.New("OpenAI-compatible response emitted a non-native textual tool call"))
	}
	output, _ := calls[0].Parameters["output"].(string)
	output = strings.TrimSpace(output)
	if output == "" {
		return agentruntime.Message{}, newRetryableModelProtocolError(errors.New("OpenAI-compatible completion envelope is missing output"))
	}
	if len(message.ToolCalls) > 0 {
		// Native calls define this as a progress turn. Do not publish a competing
		// textual completion while those calls are still pending.
		message.Content = ""
		return message, nil
	}
	message.Content = output
	return message, nil
}
