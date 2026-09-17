package agentruntime

import (
	"bytes"
	"context"

	"encoding/base64"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAIChatClient struct {
	Endpoint       string
	APIKey         string
	Model          string
	HTTPClient     *http.Client
	RequestTimeout time.Duration
	MaxAttempts    int
}

func (c OpenAIChatClient) Complete(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return ModelResponse{}, errors.New("OpenAI chat endpoint is required")
	}
	if strings.TrimSpace(c.Model) == "" {
		return ModelResponse{}, errors.New("OpenAI chat model is required")
	}
	rawRequest, err := json.Marshal(openAIChatRequest{
		Model:       c.Model,
		Messages:    openAIMessagesFromRuntime(request.Messages),
		Tools:       openAIToolsFromRuntime(request.Tools),
		Temperature: 0.2,
		Metadata:    request.Metadata,
	})
	if err != nil {
		return ModelResponse{}, err
	}
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retryable, err := c.completeAttempt(ctx, rawRequest, request.Headers)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryable || attempt >= maxAttempts {
			break
		}
		if err := waitModelRetry(ctx, attempt, lastErr); err != nil {
			return ModelResponse{}, err
		}
	}
	return ModelResponse{}, lastErr
}

func (c OpenAIChatClient) completeAttempt(ctx context.Context, rawRequest []byte, headers map[string]string) (ModelResponse, bool, error) {
	requestCtx := ctx
	cancel := func() {}
	if c.RequestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.RequestTimeout)
	}
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.Endpoint, bytes.NewReader(rawRequest))
	if err != nil {
		return ModelResponse{}, false, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		httpRequest.Header.Set(key, value)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return ModelResponse{}, requestCtx.Err() == nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOpenAIChatResponseBytes+1))
	if err != nil {
		return ModelResponse{}, false, err
	}
	if len(body) > maxOpenAIChatResponseBytes {
		return ModelResponse{}, false, fmt.Errorf("OpenAI chat response exceeds %d bytes", maxOpenAIChatResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		responseErr := fmt.Errorf("OpenAI chat endpoint returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		if retryable {
			responseErr = withModelRetryAfter(responseErr, response.Header, time.Now())
		}
		return ModelResponse{}, retryable, responseErr
	}
	var decoded openAIChatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ModelResponse{}, false, err
	}
	if len(decoded.Choices) == 0 {
		return ModelResponse{}, false, errors.New("OpenAI chat response has no choices")
	}
	requestID := strings.TrimSpace(decoded.ID)
	if requestID == "" {
		for _, key := range []string{"x-request-id", "request-id"} {
			if requestID = strings.TrimSpace(response.Header.Get(key)); requestID != "" {
				break
			}
		}
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = strings.TrimSpace(c.Model)
	}
	totalTokens := decoded.Usage.TotalTokens
	if totalTokens == 0 && (decoded.Usage.PromptTokens != 0 || decoded.Usage.CompletionTokens != 0) {
		totalTokens = decoded.Usage.PromptTokens + decoded.Usage.CompletionTokens
	}
	return ModelResponse{
		Message:    runtimeMessageFromOpenAI(decoded.Choices[0].Message),
		Model:      model,
		RequestID:  requestID,
		StopReason: decoded.Choices[0].FinishReason,
		Usage: ModelUsage{
			InputTokens:     decoded.Usage.PromptTokens,
			OutputTokens:    decoded.Usage.CompletionTokens,
			CacheReadTokens: decoded.Usage.PromptTokensDetails.CachedTokens,
			TotalTokens:     totalTokens,
		},
	}, false, nil
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func openAIMessagesFromRuntime(messages []Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, openAIMessage{
			Role:       message.Role,
			Content:    openAIContentFromRuntime(message),
			ToolCallID: message.ToolCallID,
			ToolCalls:  openAIToolCallsFromRuntime(message.ToolCalls),
		})
	}
	return out
}

func openAIContentFromRuntime(message Message) any {
	if len(message.Parts) == 0 {
		if message.Content == "" {
			return nil
		}
		return message.Content
	}
	parts := make([]any, 0, len(message.Parts)+1)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{"type": "text", "text": message.Content})
	}
	for _, part := range message.Parts {
		switch part.Type {
		case ContentPartText:
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		case ContentPartImage:
			if part.Media != nil {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + part.Media.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.Media.Source.Data),
					},
				})
			}
		case ContentPartDocument:
			if part.Media != nil {
				parts = append(parts, map[string]any{
					"type": "file",
					"file": map[string]any{
						"filename":  part.Media.Filename,
						"file_data": "data:" + part.Media.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.Media.Source.Data),
					},
				})
			}
		case ContentPartAudio:
			if part.Media != nil {
				format := "wav"
				if part.Media.MIMEType == "audio/mpeg" {
					format = "mp3"
				}
				parts = append(parts, map[string]any{
					"type": "input_audio",
					"input_audio": map[string]any{
						"data": base64.StdEncoding.EncodeToString(part.Media.Source.Data), "format": format,
					},
				})
			}
		}
	}
	return parts
}

func openAIToolCallsFromRuntime(calls []ToolCall) []openAIToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openAIToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, openAIToolCall{
			ID:   call.ID,
			Type: "function",
			Function: openAIToolCallFunction{
				Name:      call.Name,
				Arguments: string(call.Arguments),
			},
		})
	}
	return out
}

func openAIToolsFromRuntime(tools []ToolSchema) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return out
}

func runtimeMessageFromOpenAI(message openAIMessage) Message {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		role = "assistant"
	}
	return Message{
		Role:       role,
		Content:    openAITextContent(message.Content),
		ToolCallID: message.ToolCallID,
		ToolCalls:  runtimeToolCallsFromOpenAI(message.ToolCalls),
	}
}

func openAITextContent(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok || stringValueRuntime(part["type"]) != "text" {
			continue
		}
		if text := strings.TrimSpace(stringValueRuntime(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func stringValueRuntime(value any) string {
	text, _ := value.(string)
	return text
}

func runtimeToolCallsFromOpenAI(calls []openAIToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		arguments := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		out = append(out, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
