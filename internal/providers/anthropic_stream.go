package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

type anthropicStreamUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index,omitempty"`
	Message struct {
		ID    string               `json:"id"`
		Role  string               `json:"role"`
		Usage anthropicStreamUsage `json:"usage"`
	} `json:"message,omitempty"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
		Text string `json:"text,omitempty"`
	} `json:"content_block,omitempty"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
	Usage anthropicStreamUsage `json:"usage,omitempty"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type anthropicStreamTool struct {
	ID, Name  string
	Arguments strings.Builder
}

type anthropicStreamAccumulator struct {
	requestID        string
	role             string
	content          strings.Builder
	tools            map[int]*anthropicStreamTool
	usage            providerTokenUsage
	emitted          bool
	stopped          bool
	toolBoundarySent bool
}

func (c *streamingRuntimeModelClient) completeAnthropicStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	system, messages, err := anthropicMessagesFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	payload, err := json.Marshal(anthropicMessageRequest{
		Model: c.profile.Model, MaxTokens: effectiveMaxTokens(request.MaxTokens, defaultAnthropicMaxTokens), System: system,
		Messages: messages, Tools: anthropicToolsFromRuntime(request.Tools), Temperature: request.Temperature,
		ToolChoice: anthropicToolChoice(request.ToolChoice), Stream: true,
	})
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	return c.completeNativeStream(ctx, request, emit, c.profile.Provider.Endpoint, payload,
		c.anthropicResponseDecoder, readAnthropicStream)
}

func readAnthropicStream(client *runtimeModelClient, response http.Response, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	acc := anthropicStreamAccumulator{role: "assistant", tools: make(map[int]*anthropicStreamTool)}
	err := readNativeSSE(response.Body, client.profile.Request.MaxResponseBytes, func(eventName string, raw []byte) error {
		var event anthropicStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		eventType := firstNonEmpty(strings.TrimSpace(event.Type), strings.TrimSpace(eventName))
		switch eventType {
		case "message_start":
			acc.requestID = strings.TrimSpace(event.Message.ID)
			if role := strings.TrimSpace(event.Message.Role); role != "" {
				acc.role = role
			}
			acc.usage.PromptTokens = event.Message.Usage.InputTokens
			acc.usage.CompletionTokens = event.Message.Usage.OutputTokens
			acc.usage.CacheReadTokens = event.Message.Usage.CacheReadInputTokens
			acc.usage.CacheWriteTokens = event.Message.Usage.CacheCreationInputTokens
		case "content_block_start":
			switch event.ContentBlock.Type {
			case "text":
				if event.ContentBlock.Text != "" {
					acc.content.WriteString(event.ContentBlock.Text)
				}
			case "tool_use":
				name, err := normalizeModelToolName(event.ContentBlock.Name)
				if err != nil {
					return err
				}
				if !acc.toolBoundarySent {
					if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventToolCallBoundary}); err != nil {
						return err
					}
					acc.toolBoundarySent = true
				}
				acc.tools[event.Index] = &anthropicStreamTool{ID: strings.TrimSpace(event.ContentBlock.ID), Name: name}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					acc.content.WriteString(event.Delta.Text)
					acc.emitted = true
					if err := emit(agentruntime.ModelStreamEvent{
						Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: event.Delta.Text,
					}); err != nil {
						return err
					}
				}
			case "input_json_delta":
				tool := acc.tools[event.Index]
				if tool == nil {
					return fmt.Errorf("Anthropic input_json_delta references unknown content block %d", event.Index)
				}
				tool.Arguments.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			if event.Usage.OutputTokens != 0 {
				acc.usage.CompletionTokens = event.Usage.OutputTokens
			}
		case "message_stop":
			acc.stopped = true
		case "error":
			return fmt.Errorf("Anthropic stream error %s: %s", strings.TrimSpace(event.Error.Type), strings.TrimSpace(event.Error.Message))
		}
		return nil
	})
	if err != nil {
		if acc.emitted && len(acc.tools) == 0 && isRecoverableProviderStreamFailure(err) {
			err = newProviderStreamRecoveryCandidate(err)
		}
		return agentruntime.ModelResponse{}, acc.requestID, acc.usage, acc.emitted, err
	}
	result, requestID, usage, emitted, err := acc.response()
	if err != nil && acc.emitted && len(acc.tools) == 0 && isRecoverableProviderStreamFailure(err) {
		err = newProviderStreamRecoveryCandidate(err)
	}
	return result, requestID, usage, emitted, err
}

func (a *anthropicStreamAccumulator) response() (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	a.usage.TotalTokens = a.usage.PromptTokens + a.usage.CompletionTokens + a.usage.CacheReadTokens + a.usage.CacheWriteTokens
	if !a.stopped {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
			fmt.Errorf("%w: %w", errProviderResponseTruncated, errProviderStreamIncomplete)
	}
	if a.role != "" && a.role != "assistant" {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted, errors.New("Anthropic stream response role must be assistant")
	}
	indexes := make([]int, 0, len(a.tools))
	for index := range a.tools {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]agentruntime.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		tool := a.tools[index]
		if tool.ID == "" || tool.Name == "" {
			return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted, newRetryableModelProtocolError(
				fmt.Errorf("Anthropic tool_use block %d is missing id or name", index),
			)
		}
		protocolDiagnostic := ""
		arguments, valid := normalizeProviderToolArgumentsObject(tool.Arguments.String())
		if !valid {
			protocolDiagnostic = fmt.Sprintf("Anthropic tool_use block %d has invalid JSON object input", index)
			arguments = json.RawMessage(`{}`)
		}
		calls = append(calls, agentruntime.ToolCall{
			ID: tool.ID, Name: tool.Name, Arguments: arguments,
			ProviderProtocolDiagnostic: protocolDiagnostic,
		})
	}
	content := a.content.String()
	if strings.TrimSpace(content) == "" && len(calls) == 0 {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted, errors.New("Anthropic stream response has no supported content blocks")
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: content, ToolCalls: calls}}, a.requestID, a.usage, a.emitted, nil
}
