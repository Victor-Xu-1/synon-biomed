package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	workspace "synon-go/internal/persistence/workspace"
)

const memoryExtractionTestModel = "memory-test-model"

type captureMemoryModelClient struct {
	request agentruntime.ModelRequest
}

func (client *captureMemoryModelClient) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	client.request = request
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: `{}`}}, nil
}

func TestMemoryRuntimeModelClientNormalizesNoToolChoice(t *testing.T) {
	request := agentruntime.ModelRequest{
		Messages:   []agentruntime.Message{{Role: "user", Content: "extract"}},
		Tools:      []agentruntime.ToolSchema{{Name: "read_file"}},
		ToolChoice: map[string]any{"type": "none"},
	}
	capture := &captureMemoryModelClient{}
	_, err := (memoryRuntimeModelClient{client: capture}).Complete(context.Background(), request)
	if err != nil || capture.request.ToolChoice != "none" || len(capture.request.Tools) != 1 {
		t.Fatalf("normalized request=%#v err=%v", capture.request, err)
	}
}

func TestWorkspaceCompactExtractionProviderContract(t *testing.T) {
	var (
		capturedMu sync.Mutex
		captured   map[string]any
		path       string
	)
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		capturedMu.Lock()
		captured = payload
		path = request.URL.Path
		capturedMu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "msg_extract", "role": "assistant", "model": memoryExtractionTestModel, "stop_reason": "tool_use",
			"content": []any{map[string]any{
				"type": "tool_use", "id": "toolu_emit", "name": memoryextract.EmitToolName,
				"input": map[string]any{"append": []any{map[string]any{"text": "Durable assay fact", "evidence": "observed"}}},
			}},
			"usage": map[string]any{"input_tokens": 17, "output_tokens": 9},
		})
	}))
	defer provider.Close()

	server := openWorkspaceMemoryToolServer(t)
	registerMemoryExtractionTestProvider(t, server, provider.URL)
	server.httpClient = provider.Client()
	extractor := server.newMemoryCompactExtractor(memorytools.Scope{UserID: "user-1", ProjectID: "project-1", FrameID: "frame-root"})
	result, err := extractor.ExtractMemories(context.Background(), memoryextract.ExtractRequest{
		Mode: "compact", Model: memoryExtractionTestModel,
		Transcript: "user: retain this assay fact", Prompt: "extract prompt",
		SchemaJSON: memoryextract.EmitSchemaJSON(), MaxTokens: memorypolicy.ExtractionMaxTokens,
		Deadline: memorypolicy.ExtractionDeadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendValues, _ := result["append"].([]any)
	if len(appendValues) != 1 {
		t.Fatalf("extracted operations = %#v", result)
	}

	capturedMu.Lock()
	payload := captured
	requestPath := path
	capturedMu.Unlock()
	if requestPath != "/v1/messages" || payload["model"] != memoryExtractionTestModel ||
		numberValue(payload["max_tokens"]) != memorypolicy.ExtractionMaxTokens || payload["temperature"] != nil {
		t.Fatalf("compact extraction request = path %q payload %#v", requestPath, payload)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("compact extraction messages = %#v", messages)
	}
	message, _ := messages[0].(map[string]any)
	parts, _ := message["content"].([]any)
	if message["role"] != "user" || len(parts) != 2 ||
		memoryTextBlock(parts[0]) != "<transcript>\nuser: retain this assay fact\n</transcript>" ||
		memoryTextBlock(parts[1]) != "extract prompt" {
		t.Fatalf("compact extraction content = %#v", message)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("compact extraction tools = %#v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	choice, _ := payload["tool_choice"].(map[string]any)
	if tool["name"] != memoryextract.EmitToolName || tool["description"] != memoryextract.EmitToolDescription ||
		tool["input_schema"] == nil || choice["type"] != "tool" || choice["name"] != memoryextract.EmitToolName {
		t.Fatalf("compact extraction tool contract = tool %#v choice %#v", tool, choice)
	}
}

func TestWorkspaceMemoryModelUsesDurableConversationSelectionWithoutSessionIndex(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if payload["model"] != memoryExtractionTestModel {
			t.Fatalf("selected model payload=%#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"msg_selected","role":"assistant","model":"memory-test-model","stop_reason":"end_turn","content":[{"type":"text","text":"selected"}]}`))
	}))
	defer provider.Close()
	server := openWorkspaceMemoryToolServer(t)
	registerMemoryExtractionTestProvider(t, server, provider.URL)
	server.httpClient = provider.Client()
	if _, err := server.workspaceStore.SetFrameRuntimeMetadata("frame-root", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"web_assistant": map[string]any{
			"conversation_overrides": map[string]any{"model": memoryExtractionTestModel, "model_revision": float64(1)},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := server.sessionStore.Delete("frame-root"); err != nil || !deleted {
		t.Fatalf("delete compatibility session deleted=%v err=%v", deleted, err)
	}
	client, err := server.newMemoryRuntimeModelClient(context.Background(), memorytools.Scope{
		UserID: "user-1", ProjectID: "project-1", FrameID: "frame-root", SourceFrameID: "frame-root",
	}, "missing-fallback-model", time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "extract"}}, MaxTokens: 16,
	})
	if err != nil || response.Message.Content != "selected" {
		t.Fatalf("selected response=%#v err=%v", response, err)
	}
	if _, err := server.newMemoryRuntimeModelClient(context.Background(), memorytools.Scope{
		UserID: "another-user", ProjectID: "project-1", FrameID: "frame-root", SourceFrameID: "frame-root",
	}, "", time.Second, 1); err == nil || !strings.Contains(err.Error(), "owner does not match") {
		t.Fatalf("owner mismatch error=%v", err)
	}
}

func TestWorkspaceLiteralRepairProviderContract(t *testing.T) {
	var (
		capturedMu sync.Mutex
		captured   map[string]any
	)
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		capturedMu.Lock()
		captured = payload
		capturedMu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "msg_repair", "role": "assistant", "model": memoryExtractionTestModel, "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": "Repaired final row"}},
			"usage":   map[string]any{"input_tokens": 11, "output_tokens": 4},
		})
	}))
	defer provider.Close()

	server := openWorkspaceMemoryToolServer(t)
	registerMemoryExtractionTestProvider(t, server, provider.URL)
	server.httpClient = provider.Client()
	model := &serverMemoryRepairModel{server: server, scope: memorytools.Scope{UserID: "user-1", ProjectID: "project-1"}}
	text, err := model.RepairMemoryText(context.Background(), memoryextract.RepairRequest{
		Prompt: "repair prompt", Model: memoryExtractionTestModel,
		MaxTokens: memorypolicy.LiteralRepairMaxTokens, Temperature: 0,
	})
	if err != nil || text != "Repaired final row" {
		t.Fatalf("repair result = %q, %v", text, err)
	}
	capturedMu.Lock()
	payload := captured
	capturedMu.Unlock()
	if payload["model"] != memoryExtractionTestModel ||
		numberValue(payload["max_tokens"]) != memorypolicy.LiteralRepairMaxTokens ||
		numberValue(payload["temperature"]) != 0 || payload["tools"] != nil || payload["tool_choice"] != nil {
		t.Fatalf("literal repair request = %#v", payload)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("literal repair messages = %#v", messages)
	}
	message, _ := messages[0].(map[string]any)
	parts, _ := message["content"].([]any)
	if message["role"] != "user" || len(parts) != 1 || memoryTextBlock(parts[0]) != "repair prompt" {
		t.Fatalf("literal repair messages = %#v", messages)
	}
}

func TestWorkspaceCompactExtractionMissingToolReturnsEmptyAndCancellationPropagates(t *testing.T) {
	t.Run("missing tool", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"id":"msg_none","role":"assistant","content":[{"type":"text","text":"no tool"}],"stop_reason":"end_turn"}`))
		}))
		defer provider.Close()
		server := openWorkspaceMemoryToolServer(t)
		registerMemoryExtractionTestProvider(t, server, provider.URL)
		server.httpClient = provider.Client()
		result, err := server.newMemoryCompactExtractor(memorytools.Scope{UserID: "user-1", ProjectID: "project-1"}).ExtractMemories(context.Background(), memoryextract.ExtractRequest{
			Mode: "compact", Model: memoryExtractionTestModel, Transcript: "user: three words here", Prompt: "extract",
			SchemaJSON: memoryextract.EmitSchemaJSON(),
		})
		if err != nil || len(result) != 0 {
			t.Fatalf("missing-tool result = %#v, %v", result, err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		release := make(chan struct{})
		provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer provider.Close()
		server := openWorkspaceMemoryToolServer(t)
		registerMemoryExtractionTestProvider(t, server, provider.URL)
		server.httpClient = provider.Client()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_, err := server.newMemoryCompactExtractor(memorytools.Scope{UserID: "user-1", ProjectID: "project-1"}).ExtractMemories(ctx, memoryextract.ExtractRequest{
			Mode: "compact", Model: memoryExtractionTestModel, Transcript: "user: three words here", Prompt: "extract",
			SchemaJSON: memoryextract.EmitSchemaJSON(),
		})
		close(release)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancellation error = %v", err)
		}
	})
}

func registerMemoryExtractionTestProvider(t *testing.T, server *Server, baseURL string) {
	t.Helper()
	enabled := true
	_, err := server.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "memory-extraction-test", UserID: "user-1", Name: "Memory extraction test provider",
		Type: "anthropic", BaseURL: baseURL + "/v1", Model: memoryExtractionTestModel, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func memoryTextBlock(value any) string {
	block, _ := value.(map[string]any)
	if block["type"] != "text" {
		return ""
	}
	text, _ := block["text"].(string)
	return text
}
