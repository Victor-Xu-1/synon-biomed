package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"synon-go/internal/agentruntime"
)

const runnerPublicProgressField = "public_progress"
const runnerPublicProgressFieldDescription = "Public progress following the shared communication contract: one or two short sentences with an observed finding or uncertainty, then the next action and its purpose, in the user's language. Separate from the operation label and final answer. Routine operations may use an empty string when minLength is zero. In parallel batches update only on the first call."

// This is a model-facing response field, not an executable tool argument.
// It shares the primary model request with the selected action. The executable
// schema, permissions, admission checks and gateway remain unchanged.
func projectRunnerResponseContract(request agentruntime.ModelRequest, due bool) (agentruntime.ModelRequest, error) {
	request.Tools = append([]agentruntime.ToolSchema(nil), request.Tools...)
	for index, tool := range request.Tools {
		parameters := maps.Clone(tool.Parameters)
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		properties, _ := parameters["properties"].(map[string]any)
		properties = maps.Clone(properties)
		if properties == nil {
			properties = map[string]any{}
		}
		if _, collision := properties[runnerPublicProgressField]; collision {
			return request, fmt.Errorf("tool %q reserves the primary response progress field", tool.Name)
		}
		minimum := 0
		if due {
			minimum = 1
		}
		properties[runnerPublicProgressField] = map[string]any{
			"type": "string", "minLength": minimum, "maxLength": sessionRunnerPublicProgressNarrationMaxRunes,
			"description": runnerPublicProgressFieldDescription,
		}
		required := []string{}
		switch values := parameters["required"].(type) {
		case []string:
			required = append(required, values...)
		case []any:
			for _, value := range values {
				if name, ok := value.(string); ok {
					required = append(required, name)
				}
			}
		}
		parameters["properties"] = properties
		parameters["required"] = append(required, runnerPublicProgressField)
		request.Tools[index].Parameters = parameters
	}
	return request, nil
}

type sessionRunnerResponseContractClient struct {
	delegate        agentruntime.ModelClient
	progressDue     func() bool
	progressAllowed func() bool
}

func (client *sessionRunnerResponseContractClient) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return client.CompleteStream(ctx, request, nil)
}

// Preserve native streaming through the existing candidate/public boundary.
// A valid native preamble wins over the structured field, so there is never a
// second copy of the same model turn's explanation. Structured progress is
// released only after the complete response has validated its JSON arguments.
func (client *sessionRunnerResponseContractClient) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	projected, err := projectRunnerResponseContract(request, client.progressDue != nil && client.progressDue())
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	var response agentruntime.ModelResponse
	if streaming, ok := client.delegate.(agentruntime.StreamingModelClient); ok {
		response, err = streaming.CompleteStream(ctx, projected, emit)
	} else {
		response, err = client.delegate.Complete(ctx, projected)
		if response.Message.Content != "" && emit != nil {
			if dispatchErr := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: response.Message.Content}); dispatchErr != nil {
				return response, dispatchErr
			}
		}
	}
	if err != nil {
		return response, err
	}
	progress, blockID := extractRunnerResponseProgress(&response)
	if progress == "" || sessionRunnerPublicProgressNarration(response.Message.Content) != "" || strings.Contains(response.Message.Content, agentruntime.PublicProgressEnvelopeBegin) {
		return response, nil
	}
	if client.progressAllowed != nil && !client.progressAllowed() {
		return response, nil
	}
	response.Message.Content = progress
	events := []agentruntime.ModelStreamEvent{
		{Kind: agentruntime.ModelStreamEventToolCallBoundary},
		{Kind: agentruntime.ModelStreamEventPublicProgressDelta, BlockID: blockID, ContentDelta: progress},
		{Kind: agentruntime.ModelStreamEventPublicProgressBoundary, BlockID: blockID},
	}
	for _, event := range events {
		if emit != nil {
			if err := emit(event); err != nil {
				return response, err
			}
		}
	}
	return response, nil
}

func extractRunnerResponseProgress(response *agentruntime.ModelResponse) (string, string) {
	progress, blockID := "", ""
	for index := range response.Message.ToolCalls {
		call := &response.Message.ToolCalls[index]
		var args map[string]json.RawMessage
		if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
			continue
		}
		encoded, exists := args[runnerPublicProgressField]
		if !exists {
			continue
		}
		delete(args, runnerPublicProgressField)
		call.Arguments, _ = json.Marshal(args)
		var text string
		if json.Unmarshal(encoded, &text) != nil || progress != "" {
			continue
		}
		text = strings.TrimSpace(text)
		if safe := sessionRunnerPublicProgressNarration(text); safe != "" {
			progress = safe
			blockID = fmt.Sprintf("response-%x", sha256.Sum256([]byte(call.ID+"\x00"+safe)))
		}
	}
	return progress, blockID
}
