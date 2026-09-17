package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestRuntimeModelClientAnthropicStreamsTextToolsUsageAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-key" ||
			r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Errorf("request=%s headers=%#v", r.URL.Path, r.Header)
		}
		var payload struct {
			Stream      bool     `json:"stream"`
			Temperature *float64 `json:"temperature"`
			MaxTokens   int      `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !payload.Stream || payload.Temperature == nil || *payload.Temperature != 0.4 || payload.MaxTokens != 16384 {
			t.Errorf("payload=%#v err=%v", payload, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream_1\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic \"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"stream\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"lookup\",\"input\":{}}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, event := range events {
			_, _ = w.Write([]byte(event))
		}
	}))
	defer server.Close()
	var audit AuditRecord
	profileTemperature := 0.4
	profileMaxTokens := 16384
	client, err := NewRuntimeModelClient(ModelProfile{Provider: ProviderProfile{ID: "anthropic-stream", Type: "anthropic", Protocol: ProtocolAnthropic, Endpoint: server.URL + "/v1/messages"}, Model: "claude-stream", APIKey: "anthropic-key", Temperature: &profileTemperature, MaxTokens: &profileMaxTokens, Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024}}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := client.(agentruntime.StreamingModelClient)
	if !ok {
		t.Fatalf("client %T is not streaming", client)
	}
	var deltas strings.Builder
	kinds := make([]agentruntime.ModelStreamEventKind, 0, 3)
	response, err := streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "use lookup"}}, Tools: []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}, func(event agentruntime.ModelStreamEvent) error {
		deltas.WriteString(event.ContentDelta)
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if deltas.String() != "anthropic stream" || response.Message.Content != "anthropic stream" || len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].ID != "toolu_1" || response.Message.ToolCalls[0].Name != "lookup" || string(response.Message.ToolCalls[0].Arguments) != "{\"q\":\"x\"}" {
		t.Fatalf("deltas=%q response=%#v", deltas.String(), response)
	}
	if !reflect.DeepEqual(kinds, []agentruntime.ModelStreamEventKind{
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventToolCallBoundary,
	}) {
		t.Fatalf("Anthropic stream event kinds = %#v", kinds)
	}
	if audit.RequestID != "msg_stream_1" || audit.PromptTokens != 10 || audit.CompletionTokens != 5 || audit.CacheReadTokens != 3 || audit.CacheWriteTokens != 2 || audit.TotalTokens != 20 || audit.Error != "" {
		t.Fatalf("audit=%#v", audit)
	}
}

func TestRuntimeModelClientGeminiStreamGenerateContentTextToolsUsageAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") || r.URL.Query().Get("alt") != "sse" || r.URL.Query().Get("tenant") != "alpha" || r.Header.Get("x-goog-api-key") != "gem-key" {
			t.Errorf("url=%s headers=%#v", r.URL.String(), r.Header)
		}
		var payload struct {
			GenerationConfig map[string]any `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.GenerationConfig["temperature"] != 0.6 || payload.GenerationConfig["maxOutputTokens"] != float64(24576) {
			t.Errorf("payload=%#v err=%v", payload, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-stream-1\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"private chain of thought\",\"thought\":true}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-stream-1\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"gemini \"}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-stream-1\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"stream\"},{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"q\":\"x\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":5,\"cachedContentTokenCount\":4,\"totalTokenCount\":17}}\n\n"))
	}))
	defer server.Close()
	var audit AuditRecord
	profileTemperature := 0.6
	profileMaxTokens := 24576
	client, err := NewRuntimeModelClient(ModelProfile{Provider: ProviderProfile{ID: "gemini-stream", Type: "gemini", Protocol: ProtocolGemini, Endpoint: server.URL + "/v1beta/models/gemini-test:generateContent?tenant=alpha"}, Model: "gemini-test", APIKey: "gem-key", Temperature: &profileTemperature, MaxTokens: &profileMaxTokens, Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024}}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := client.(agentruntime.StreamingModelClient)
	if !ok {
		t.Fatalf("client %T is not streaming", client)
	}
	var deltas strings.Builder
	kinds := make([]agentruntime.ModelStreamEventKind, 0, 4)
	privateReasoningObserved := false
	response, err := streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "use lookup"}}, Tools: []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}, func(event agentruntime.ModelStreamEvent) error {
		deltas.WriteString(event.ContentDelta)
		kinds = append(kinds, event.Kind)
		privateReasoningObserved = privateReasoningObserved || event.ReasoningActive
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if deltas.String() != "gemini stream" || response.Message.Content != "gemini stream" || len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != "lookup" || string(response.Message.ToolCalls[0].Arguments) != "{\"q\":\"x\"}" {
		t.Fatalf("deltas=%q response=%#v", deltas.String(), response)
	}
	if !reflect.DeepEqual(kinds, []agentruntime.ModelStreamEventKind{
		agentruntime.ModelStreamEventPrivateReasoning,
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventToolCallBoundary,
	}) {
		t.Fatalf("Gemini stream event kinds = %#v", kinds)
	}
	if !privateReasoningObserved {
		t.Fatal("Gemini thought part was not classified as private reasoning")
	}
	if audit.RequestID != "gem-stream-1" || audit.PromptTokens != 12 || audit.CompletionTokens != 5 || audit.CacheReadTokens != 4 || audit.TotalTokens != 17 || audit.Error != "" {
		t.Fatalf("audit=%#v", audit)
	}
}
