package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestBuildModelProfileNormalizesAnthropicMessagesEndpoint(t *testing.T) {
	profile, err := buildModelProfile(workspace.ModelProvider{
		ID: "anthropic-a", UserID: "user-1", Name: "Anthropic", Type: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-test", Enabled: true,
	}, nil, "user-1", ResolutionInput{RequestTimeout: 45 * time.Second, MaxAttempts: 2, MaxResponseBytes: 32768})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Provider.Protocol != ProtocolAnthropic || profile.Provider.Endpoint != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("provider profile = %#v", profile.Provider)
	}
	if profile.Request.MaxAttempts != 2 || profile.Request.Timeout != 45*time.Second || profile.Request.MaxResponseBytes != 32768 {
		t.Fatalf("request profile = %#v", profile.Request)
	}
}

func TestRuntimeModelClientAnthropicEncodesImageAndDocumentParts(t *testing.T) {
	captured := make(chan []any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		messages := payload["messages"].([]any)
		captured <- messages[0].(map[string]any)["content"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"anthropic-media","role":"assistant","content":[{"type":"text","text":"accepted"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "anthropic-media", Type: "anthropic", Protocol: ProtocolAnthropic, Endpoint: server.URL},
		Model:    "claude-media", APIKey: "anthropic-key",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{
		Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{MIMEType: "image/png", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}}},
			{Type: agentruntime.ContentPartDocument, Media: &agentruntime.MediaContent{MIMEType: "text/plain", Filename: "notes.txt", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("notes")}}},
			{Type: agentruntime.ContentPartText, Text: "inspect"},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	blocks := <-captured
	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v", blocks)
	}
	image := blocks[0].(map[string]any)
	document := blocks[1].(map[string]any)
	if image["type"] != "image" || image["source"].(map[string]any)["type"] != "base64" ||
		document["type"] != "document" || document["title"] != "notes.txt" || document["source"].(map[string]any)["type"] != "text" ||
		blocks[2].(map[string]any)["text"] != "inspect" {
		t.Fatalf("encoded blocks = %#v", blocks)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{
		Role: "user", Parts: []agentruntime.ContentPart{{Type: agentruntime.ContentPartAudio, Media: &agentruntime.MediaContent{MIMEType: "audio/wav", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("RIFFxxxxWAVE")}}}},
	}}})
	if err == nil {
		t.Fatal("Anthropic audio input unexpectedly succeeded")
	}
}

func TestRuntimeModelClientAnthropicSupportsToolUseAndResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["model"] != "claude-test" || numberFromAny(payload["max_tokens"]) <= 0 || payload["system"] != "system prompt" {
			t.Errorf("request metadata = %#v", payload)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 3 {
			t.Errorf("messages = %#v", payload["messages"])
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assistant := messages[1].(map[string]any)
		assistantBlocks := assistant["content"].([]any)
		toolUse := assistantBlocks[0].(map[string]any)
		if assistant["role"] != "assistant" || toolUse["type"] != "tool_use" || toolUse["id"] != "call-1" || toolUse["name"] != "task_create" {
			t.Errorf("assistant tool use = %#v", assistant)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		toolResultMessage := messages[2].(map[string]any)
		resultBlocks := toolResultMessage["content"].([]any)
		toolResult := resultBlocks[0].(map[string]any)
		if toolResultMessage["role"] != "user" || toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call-1" {
			t.Errorf("tool result = %#v", toolResultMessage)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tools, ok := payload["tools"].([]any)
		if !ok || len(tools) != 1 || tools[0].(map[string]any)["name"] != "task_create" {
			t.Errorf("tools = %#v", payload["tools"])
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "anthropic-request-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "stop_reason": "tool_use",
			"content": []any{
				map[string]any{"type": "text", "text": "anthropic text"},
				map[string]any{"type": "tool_use", "id": "toolu_2", "name": "task_create", "input": map[string]any{"title": "from anthropic"}},
			},
			"usage": map[string]any{"input_tokens": 19, "output_tokens": 7, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 3},
		})
	}))
	defer server.Close()

	var audit AuditRecord
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "provider-anthropic", Type: "anthropic", Protocol: ProtocolAnthropic, Endpoint: server.URL + "/v1/messages"},
		Model:    "claude-test",
		APIKey:   "anthropic-key",
		Request:  RequestProfile{Timeout: time.Minute, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "create a task"},
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "task_create", Arguments: json.RawMessage([]byte("{\"title\":\"draft\"}"))}}},
			{Role: "tool", ToolCallID: "call-1", Content: "{\"ok\":true,\"taskId\":\"task-1\"}"},
		},
		Tools: []agentruntime.ToolSchema{{Name: "task_create", Description: "Create task", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.Message.Content != "anthropic text" || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].ID != "toolu_2" || response.Message.ToolCalls[0].Name != "task_create" {
		t.Fatalf("response = %#v", response)
	}
	if audit.Protocol != ProtocolAnthropic || audit.RequestID != "msg_1" ||
		audit.PromptTokens != 19 || audit.CompletionTokens != 7 || audit.CacheReadTokens != 2 ||
		audit.CacheWriteTokens != 3 || audit.TotalTokens != 31 {
		t.Fatalf("audit = %#v", audit)
	}
}

func numberFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}
