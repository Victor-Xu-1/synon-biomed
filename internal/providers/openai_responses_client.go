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

func (c *runtimeModelClient) completeOpenAIResponses(ctx context.Context, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
	input, err := openAIResponsesInputFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	payload, err := json.Marshal(openAIResponsesRequest{
		Model:           c.profile.Model,
		Input:           input,
		Tools:           openAIResponsesToolsFromRuntime(request.Tools),
		MaxOutputTokens: request.MaxTokens,
		Temperature:     request.Temperature,
		ToolChoice:      openAIResponsesToolChoice(request.ToolChoice),
		Metadata:        request.Metadata,
		Store:           false,
	})
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	return c.completeJSON(ctx, attempt, c.profile.Provider.Endpoint, payload, request, c.openAIResponsesResponseDecoder)
}

func openAIResponsesToolChoice(choice any) any {
	value, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	kind, _ := value["type"].(string)
	name, _ := value["name"].(string)
	if strings.EqualFold(strings.TrimSpace(kind), "tool") && strings.TrimSpace(name) != "" {
		return map[string]any{"type": "function", "name": strings.TrimSpace(name)}
	}
	return choice
}

type openAIResponsesRequest struct {
	Model           string                     `json:"model"`
	Input           []openAIResponsesInputItem `json:"input"`
	Tools           []openAIResponsesTool      `json:"tools,omitempty"`
	MaxOutputTokens int                        `json:"max_output_tokens,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	ToolChoice      any                        `json:"tool_choice,omitempty"`
	Metadata        map[string]any             `json:"metadata,omitempty"`
	Store           bool                       `json:"store"`
	Stream          bool                       `json:"stream,omitempty"`
}

type openAIResponsesInputItem struct {
	Type      string  `json:"type"`
	Role      string  `json:"role,omitempty"`
	Content   any     `json:"content,omitempty"`
	CallID    string  `json:"call_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Arguments string  `json:"arguments,omitempty"`
	Output    *string `json:"output,omitempty"`
}

type openAIResponsesContentPart struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ImageURL   string `json:"image_url,omitempty"`
	FileData   string `json:"file_data,omitempty"`
	Filename   string `json:"filename,omitempty"`
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio,omitempty"`
}

type openAIResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIResponsesResponse struct {
	ID                string `json:"id,omitempty"`
	Model             string `json:"model,omitempty"`
	Status            string `json:"status,omitempty"`
	IncompleteDetails struct {
		Reason string `json:"reason,omitempty"`
	} `json:"incomplete_details,omitempty"`
	OutputText string                      `json:"output_text,omitempty"`
	Output     []openAIResponsesOutputItem `json:"output"`
	Usage      struct {
		InputTokens       int `json:"input_tokens"`
		OutputTokens      int `json:"output_tokens"`
		TotalTokens       int `json:"total_tokens"`
		InputTokenDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type openAIResponsesOutputItem struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text,omitempty"`
		Refusal string `json:"refusal,omitempty"`
	} `json:"content,omitempty"`
}

func openAIResponsesInputFromRuntime(messages []agentruntime.Message) ([]openAIResponsesInputItem, error) {
	out := make([]openAIResponsesInputItem, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "user"
		}
		if role == "tool" {
			callID := strings.TrimSpace(message.ToolCallID)
			if callID == "" {
				return nil, errors.New("OpenAI Responses tool result requires tool_call_id")
			}
			output := message.Content
			out = append(out, openAIResponsesInputItem{
				Type: "function_call_output", CallID: callID, Output: &output,
			})
			continue
		}
		content, err := openAIResponsesContentFromRuntime(message)
		if err != nil {
			return nil, err
		}
		if content != nil {
			out = append(out, openAIResponsesInputItem{
				Type: "message", Role: role, Content: content,
			})
		}
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			name := strings.TrimSpace(call.Name)
			if callID == "" || name == "" {
				return nil, errors.New("OpenAI Responses function call requires id and name")
			}
			arguments := strings.TrimSpace(string(call.Arguments))
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("OpenAI Responses function call %s has invalid JSON arguments", callID)
			}
			out = append(out, openAIResponsesInputItem{
				Type: "function_call", CallID: callID, Name: name, Arguments: arguments,
			})
		}
	}
	return out, nil
}

func openAIResponsesContentFromRuntime(message agentruntime.Message) (any, error) {
	if len(message.Parts) == 0 {
		if strings.TrimSpace(message.Content) == "" {
			return nil, nil
		}
		return message.Content, nil
	}
	parts := make([]openAIResponsesContentPart, 0, len(message.Parts)+1)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, openAIResponsesContentPart{Type: "input_text", Text: message.Content})
	}
	for index, part := range message.Parts {
		switch part.Type {
		case agentruntime.ContentPartText:
			parts = append(parts, openAIResponsesContentPart{Type: "input_text", Text: part.Text})
		case agentruntime.ContentPartImage, agentruntime.ContentPartDocument, agentruntime.ContentPartAudio:
			if part.Media == nil || part.Media.Source.Type != agentruntime.MediaSourceData || len(part.Media.Source.Data) == 0 {
				return nil, fmt.Errorf("OpenAI Responses media part %d must be normalized inline data", index)
			}
			encoded := base64.StdEncoding.EncodeToString(part.Media.Source.Data)
			switch part.Type {
			case agentruntime.ContentPartImage:
				parts = append(parts, openAIResponsesContentPart{Type: "input_image", ImageURL: "data:" + part.Media.MIMEType + ";base64," + encoded})
			case agentruntime.ContentPartDocument:
				parts = append(parts, openAIResponsesContentPart{Type: "input_file", FileData: "data:" + part.Media.MIMEType + ";base64," + encoded, Filename: part.Media.Filename})
			case agentruntime.ContentPartAudio:
				format := "wav"
				if part.Media.MIMEType == "audio/mpeg" {
					format = "mp3"
				}
				parts = append(parts, openAIResponsesContentPart{Type: "input_audio", InputAudio: &struct {
					Data   string `json:"data"`
					Format string `json:"format"`
				}{Data: encoded, Format: format}})
			}
		default:
			return nil, fmt.Errorf("OpenAI Responses does not support content part %q", part.Type)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return parts, nil
}

func openAIResponsesToolsFromRuntime(tools []agentruntime.ToolSchema) []openAIResponsesTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAIResponsesTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		out = append(out, openAIResponsesTool{
			Type: "function", Name: tool.Name, Description: tool.Description, Parameters: parameters,
		})
	}
	return out
}

func (c *runtimeModelClient) openAIResponsesResponseDecoder(body []byte, headers http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error) {
	var decoded openAIResponsesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agentruntime.ModelResponse{}, "", providerTokenUsage{}, err
	}
	usage := providerTokenUsage{
		PromptTokens:     decoded.Usage.InputTokens,
		CompletionTokens: decoded.Usage.OutputTokens,
		CacheReadTokens:  decoded.Usage.InputTokenDetails.CachedTokens,
		TotalTokens:      decoded.Usage.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	stopReason := decoded.Status
	if decoded.IncompleteDetails.Reason != "" {
		stopReason = decoded.IncompleteDetails.Reason
	}

	role := "assistant"
	textParts := make([]string, 0)
	toolCalls := make([]agentruntime.ToolCall, 0)
	for _, item := range decoded.Output {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "message":
			if value := strings.TrimSpace(item.Role); value != "" {
				role = value
			}
			for _, content := range item.Content {
				value := strings.TrimSpace(firstNonEmpty(content.Text, content.Refusal))
				if value != "" {
					textParts = append(textParts, value)
				}
			}
		case "function_call":
			if limited := responseOutputLimit(stopReason, true); limited != nil {
				return agentruntime.ModelResponse{}, decoded.ID, usage, limited
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				return agentruntime.ModelResponse{}, decoded.ID, usage, newRetryableModelProtocolError(
					errors.New("OpenAI Responses function call is missing call_id or name"),
				)
			}
			name, err := normalizeModelToolName(item.Name)
			if err != nil {
				return agentruntime.ModelResponse{}, decoded.ID, usage, newRetryableModelProtocolError(err)
			}
			protocolDiagnostic := ""
			arguments, valid := normalizeProviderToolArgumentsObject(item.Arguments)
			if !valid {
				protocolDiagnostic = fmt.Sprintf("OpenAI Responses function call %s has invalid JSON object arguments", callID)
				arguments = json.RawMessage(`{}`)
			}
			toolCalls = append(toolCalls, agentruntime.ToolCall{
				ID: callID, Name: name, Arguments: append(json.RawMessage(nil), arguments...),
				ProviderProtocolDiagnostic: protocolDiagnostic,
			})
		}
	}
	if len(textParts) == 0 && strings.TrimSpace(decoded.OutputText) != "" {
		textParts = append(textParts, strings.TrimSpace(decoded.OutputText))
	}
	if len(textParts) == 0 && len(toolCalls) == 0 {
		return agentruntime.ModelResponse{}, decoded.ID, usage,
			newProviderEmptyResponseError("OpenAI Responses response has no output")
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: role, Content: strings.Join(textParts, "\n\n"), ToolCalls: toolCalls,
	}, Model: decoded.Model, StopReason: stopReason}, decoded.ID, usage, nil
}
