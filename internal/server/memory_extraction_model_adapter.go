package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
)

type serverMemoryCompactExtractor struct {
	server *Server
	scope  memorytools.Scope
}

type serverMemoryForkedExtractor struct {
	server      *Server
	scope       memorytools.Scope
	requestView *agentruntime.ModelRequest
}

type serverMemoryRepairModel struct {
	server *Server
	scope  memorytools.Scope
}

func (s *Server) newMemoryCompactExtractor(scope memorytools.Scope) memoryextract.Extractor {
	return &serverMemoryCompactExtractor{server: s, scope: scope}
}

func (s *Server) newMemoryForkedExtractor(scope memorytools.Scope, requestView *agentruntime.ModelRequest) memoryextract.Extractor {
	return &serverMemoryForkedExtractor{server: s, scope: scope, requestView: requestView}
}

func (s *Server) newMemoryLiteralRepairer(scope memorytools.Scope) *memoryextract.ModelLiteralRepairer {
	return memoryextract.NewModelLiteralRepairer(&serverMemoryRepairModel{server: s, scope: scope})
}

func (m *serverMemoryCompactExtractor) ExtractMemories(ctx context.Context, request memoryextract.ExtractRequest) (map[string]any, error) {
	if m == nil || m.server == nil {
		return nil, errors.New("compact memory extractor runtime is unavailable")
	}
	if request.Mode != "compact" {
		return nil, fmt.Errorf("compact memory extractor does not implement mode %q", request.Mode)
	}
	model := strings.TrimSpace(request.Model)
	schema, err := decodeMemoryModelJSONObject(request.SchemaJSON)
	if err != nil {
		return nil, fmt.Errorf("decode emit_memories schema: %w", err)
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = memorypolicy.ExtractionMaxTokens
	}
	timeout := request.Deadline
	if timeout <= 0 {
		timeout = memorypolicy.ExtractionDeadline
	}
	client, err := m.server.newMemoryRuntimeModelClient(ctx, m.scope, model, timeout, 1)
	if err != nil {
		return nil, err
	}
	response, err := client.Complete(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{
			Role: "user",
			Parts: []agentruntime.ContentPart{
				{Type: agentruntime.ContentPartText, Text: "<transcript>\n" + request.Transcript + "\n</transcript>"},
				{Type: agentruntime.ContentPartText, Text: request.Prompt},
			},
		}},
		Tools: []agentruntime.ToolSchema{{
			Name: memoryextract.EmitToolName, Description: memoryextract.EmitToolDescription, Parameters: schema,
		}},
		MaxTokens: maxTokens,
		ToolChoice: map[string]any{
			"type": "tool",
			"name": memoryextract.EmitToolName,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(response.Message.ToolCalls) == 0 || response.Message.ToolCalls[0].Name != memoryextract.EmitToolName {
		return map[string]any{}, nil
	}
	return decodeMemoryModelJSONObject(string(response.Message.ToolCalls[0].Arguments))
}

func (m *serverMemoryForkedExtractor) ExtractMemories(ctx context.Context, request memoryextract.ExtractRequest) (map[string]any, error) {
	if m == nil || m.server == nil {
		return nil, errors.New("forked memory extractor runtime is unavailable")
	}
	if request.Mode != "forked" {
		return nil, fmt.Errorf("forked memory extractor does not implement mode %q", request.Mode)
	}
	if m.requestView == nil {
		return nil, errors.New("forked memory extractor requires the last model request view")
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = memorypolicy.ExtractionForkedMaxTokens
	}
	timeout := request.Deadline
	if timeout <= 0 {
		timeout = memorypolicy.ExtractionDeadline
	}
	client, err := m.server.newMemoryRuntimeModelClient(ctx, m.scope, strings.TrimSpace(request.Model), timeout, 1)
	if err != nil {
		return nil, err
	}
	modelRequest := *m.requestView
	modelRequest.Messages = append([]agentruntime.Message(nil), m.requestView.Messages...)
	modelRequest.Messages = append(modelRequest.Messages, agentruntime.Message{Role: "user", Content: request.Prompt})
	modelRequest.MaxTokens = maxTokens
	modelRequest.ToolChoice = map[string]any{"type": "none"}
	response, err := client.Complete(ctx, modelRequest)
	if err != nil {
		return nil, err
	}
	return memoryextract.ParseForkedResponse(response.Message.Content), nil
}

func (m *serverMemoryRepairModel) RepairMemoryText(ctx context.Context, request memoryextract.RepairRequest) (string, error) {
	if m == nil || m.server == nil {
		return "", errors.New("memory literal-repair runtime is unavailable")
	}
	model := strings.TrimSpace(request.Model)
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = memorypolicy.LiteralRepairMaxTokens
	}
	client, err := m.server.newMemoryRuntimeModelClient(ctx, m.scope, model, memorypolicy.ExtractionDeadline, 1)
	if err != nil {
		return "", err
	}
	temperature := request.Temperature
	response, err := client.Complete(ctx, agentruntime.ModelRequest{
		Messages:    []agentruntime.Message{{Role: "user", Parts: []agentruntime.ContentPart{{Type: agentruntime.ContentPartText, Text: request.Prompt}}}},
		MaxTokens:   maxTokens,
		Temperature: &temperature,
	})
	if err != nil {
		return "", err
	}
	return response.Message.Content, nil
}

func decodeMemoryModelJSONObject(value string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("JSON value must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON value contains trailing data")
		}
		return nil, err
	}
	return result, nil
}
