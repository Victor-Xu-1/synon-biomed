package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"synon-go/internal/agentruntime"
)

const (
	anthropicAPIVersion       = "2023-06-01"
	defaultAnthropicMaxTokens = 4096
)

func effectiveMaxTokens(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

type anthropicMessageRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type      string                  `json:"type"`
	Text      string                  `json:"text,omitempty"`
	ID        string                  `json:"id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Input     map[string]any          `json:"input,omitempty"`
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   string                  `json:"content,omitempty"`
	IsError   bool                    `json:"is_error,omitempty"`
	Source    *anthropicContentSource `json:"source,omitempty"`
	Title     string                  `json:"title,omitempty"`
}

type anthropicContentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicMessageResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model,omitempty"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (c *runtimeModelClient) completeAnthropic(ctx context.Context, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
	system, messages, err := anthropicMessagesFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	payload, err := json.Marshal(anthropicMessageRequest{
		Model:       c.profile.Model,
		MaxTokens:   effectiveMaxTokens(request.MaxTokens, defaultAnthropicMaxTokens),
		System:      system,
		Messages:    messages,
		Tools:       anthropicToolsFromRuntime(request.Tools),
		Temperature: request.Temperature,
		ToolChoice:  anthropicToolChoice(request.ToolChoice),
	})
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	return c.completeJSON(ctx, attempt, c.profile.Provider.Endpoint, payload, request, c.anthropicResponseDecoder)
}

func anthropicToolChoice(choice any) any {
	value, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	kind, _ := value["type"].(string)
	name, _ := value["name"].(string)
	if strings.EqualFold(strings.TrimSpace(kind), "tool") && strings.TrimSpace(name) != "" {
		return map[string]any{"type": "tool", "name": strings.TrimSpace(name)}
	}
	if function, ok := value["function"].(map[string]any); ok {
		if name, ok := function["name"].(string); ok && strings.TrimSpace(name) != "" {
			return map[string]any{"type": "tool", "name": strings.TrimSpace(name)}
		}
	}
	return choice
}

func anthropicMessagesFromRuntime(messages []agentruntime.Message) (string, []anthropicMessage, error) {
	systemParts := make([]string, 0)
	out := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "system":
			if len(message.Parts) > 0 {
				return "", nil, errors.New("Anthropic system messages do not support media parts")
			}
			if text := strings.TrimSpace(message.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "assistant":
			if len(message.Parts) > 0 {
				return "", nil, errors.New("Anthropic assistant history does not support media parts")
			}
			blocks := make([]anthropicContentBlock, 0, 1+len(message.ToolCalls))
			if text := strings.TrimSpace(message.Content); text != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: text})
			}
			for _, call := range message.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name),
					Input: jsonObjectFromRaw(call.Arguments),
				})
			}
			out = appendAnthropicMessage(out, "assistant", blocks)
		case "tool":
			if len(message.Parts) > 0 {
				return "", nil, errors.New("Anthropic tool results do not support media parts")
			}
			out = appendAnthropicMessage(out, "user", []anthropicContentBlock{{
				Type: "tool_result", ToolUseID: strings.TrimSpace(message.ToolCallID), Content: message.Content,
			}})
		default:
			blocks := make([]anthropicContentBlock, 0, len(message.Parts)+1)
			if text := strings.TrimSpace(message.Content); text != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: text})
			}
			for index, part := range message.Parts {
				switch part.Type {
				case agentruntime.ContentPartText:
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: part.Text})
				case agentruntime.ContentPartImage, agentruntime.ContentPartDocument:
					if part.Media == nil || part.Media.Source.Type != agentruntime.MediaSourceData || len(part.Media.Source.Data) == 0 {
						return "", nil, fmt.Errorf("Anthropic media part %d must be normalized inline data", index)
					}
					source := &anthropicContentSource{MediaType: part.Media.MIMEType}
					if part.Type == agentruntime.ContentPartImage {
						source.Type = "base64"
						source.Data = base64.StdEncoding.EncodeToString(part.Media.Source.Data)
						blocks = append(blocks, anthropicContentBlock{Type: "image", Source: source})
					} else if part.Media.MIMEType == "application/pdf" {
						source.Type = "base64"
						source.Data = base64.StdEncoding.EncodeToString(part.Media.Source.Data)
						blocks = append(blocks, anthropicContentBlock{Type: "document", Source: source, Title: part.Media.Filename})
					} else {
						source.Type = "text"
						source.Data = string(part.Media.Source.Data)
						blocks = append(blocks, anthropicContentBlock{Type: "document", Source: source, Title: part.Media.Filename})
					}
				case agentruntime.ContentPartAudio:
					return "", nil, errors.New("Anthropic Messages does not support audio input parts")
				default:
					return "", nil, fmt.Errorf("Anthropic does not support content part %q", part.Type)
				}
			}
			out = appendAnthropicMessage(out, "user", blocks)
		}
	}
	return strings.Join(systemParts, "\n\n"), out, nil
}

func appendAnthropicMessage(messages []anthropicMessage, role string, blocks []anthropicContentBlock) []anthropicMessage {
	if len(blocks) == 0 {
		return messages
	}
	if len(messages) > 0 && messages[len(messages)-1].Role == role {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, blocks...)
		return messages
	}
	return append(messages, anthropicMessage{Role: role, Content: blocks})
}

func anthropicToolsFromRuntime(tools []agentruntime.ToolSchema) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		out = append(out, anthropicTool{
			Name: strings.TrimSpace(tool.Name), Description: strings.TrimSpace(tool.Description), InputSchema: parameters,
		})
	}
	return out
}

func (c *runtimeModelClient) anthropicResponseDecoder(body []byte, _ http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error) {
	var decoded anthropicMessageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agentruntime.ModelResponse{}, "", providerTokenUsage{}, err
	}
	usage := providerTokenUsage{
		PromptTokens: decoded.Usage.InputTokens, CompletionTokens: decoded.Usage.OutputTokens,
		CacheReadTokens: decoded.Usage.CacheReadInputTokens, CacheWriteTokens: decoded.Usage.CacheCreationInputTokens,
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	if role := strings.TrimSpace(decoded.Role); role != "" && role != "assistant" {
		return agentruntime.ModelResponse{}, decoded.ID, usage, errors.New("Anthropic response role must be assistant")
	}
	if len(decoded.Content) == 0 {
		return agentruntime.ModelResponse{}, decoded.ID, usage, errors.New("Anthropic response has no content blocks")
	}
	text := make([]string, 0, len(decoded.Content))
	toolCalls := make([]agentruntime.ToolCall, 0)
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			if value := strings.TrimSpace(block.Text); value != "" {
				text = append(text, value)
			}
		case "tool_use":
			if limited := responseOutputLimit(decoded.StopReason, true); limited != nil {
				return agentruntime.ModelResponse{}, decoded.ID, usage, limited
			}
			if strings.TrimSpace(block.ID) == "" {
				return agentruntime.ModelResponse{}, decoded.ID, usage, errors.New("Anthropic tool_use block is missing id or name")
			}
			name, err := normalizeModelToolName(block.Name)
			if err != nil {
				return agentruntime.ModelResponse{}, decoded.ID, usage, err
			}
			input := block.Input
			if input == nil {
				input = map[string]any{}
			}
			raw, err := json.Marshal(input)
			if err != nil {
				return agentruntime.ModelResponse{}, decoded.ID, usage, err
			}
			toolCalls = append(toolCalls, agentruntime.ToolCall{
				ID: strings.TrimSpace(block.ID), Name: name, Arguments: json.RawMessage(raw),
			})
		}
	}
	if len(text) == 0 && len(toolCalls) == 0 {
		return agentruntime.ModelResponse{}, decoded.ID, usage, errors.New("Anthropic response has no supported content blocks")
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant", Content: strings.Join(text, "\n\n"), ToolCalls: toolCalls,
		}, Model: decoded.Model, StopReason: decoded.StopReason},
		decoded.ID,
		usage,
		nil
}
