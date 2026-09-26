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

func TestRuntimeModelClientOpenAIResponsesStreamsTextToolsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model           string   `json:"model"`
			Stream          bool     `json:"stream"`
			Store           *bool    `json:"store"`
			Temperature     *float64 `json:"temperature"`
			MaxOutputTokens int      `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Model != "gpt-responses-stream" || !payload.Stream || payload.Store == nil || *payload.Store || payload.Temperature == nil || *payload.Temperature != 0.55 || payload.MaxOutputTokens != 12288 {
			http.Error(w, "invalid Responses stream request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		events := []struct {
			name, data string
		}{
			{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"response "}`},
			{"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"stream"}`},
			{"response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_persist","name":"persist","arguments":""}}`},
			{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"value\":2}"}`},
			{"response.completed", `{
				"type":"response.completed",
				"response":{
					"id":"resp_stream_1",
					"output":[
						{"type":"message","role":"assistant","content":[{"type":"output_text","text":"response stream"}]},
						{"id":"fc_1","type":"function_call","call_id":"call_persist","name":"persist","arguments":"{\"value\":2}"}
					],
					"usage":{"input_tokens":14,"input_tokens_details":{"cached_tokens":6},"output_tokens":5,"total_tokens":19}
				}
			}`},
		}
		for _, event := range events {
			_, _ = w.Write([]byte("event: " + event.name + "\n"))
			_, _ = w.Write([]byte("data: " + event.data + "\n\n"))
			flusher.Flush()
		}
	}))
	defer server.Close()

	var audit AuditRecord
	profileTemperature := 0.55
	profileMaxTokens := 12288
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "responses-stream", Type: "openai-responses",
			Protocol: ProtocolOpenAIResponses, Endpoint: server.URL + "/v1/responses",
		},
		Model: "gpt-responses-stream", APIKey: "responses-key", Temperature: &profileTemperature, MaxTokens: &profileMaxTokens,
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := client.(agentruntime.StreamingModelClient)
	if !ok {
		t.Fatalf("client %T does not implement StreamingModelClient", client)
	}
	deltas := make([]string, 0, 2)
	kinds := make([]agentruntime.ModelStreamEventKind, 0, 3)
	response, err := streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "stream Responses"}},
		Tools:    []agentruntime.ToolSchema{{Name: "persist", Parameters: map[string]any{"type": "object"}}},
	}, func(event agentruntime.ModelStreamEvent) error {
		deltas = append(deltas, event.ContentDelta)
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "response stream" {
		t.Fatalf("deltas = %#v", deltas)
	}
	if !reflect.DeepEqual(kinds, []agentruntime.ModelStreamEventKind{
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventToolCallBoundary,
	}) {
		t.Fatalf("stream event kinds = %#v", kinds)
	}
	if response.Message.Content != "response stream" || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].ID != "call_persist" ||
		response.Message.ToolCalls[0].Name != "persist" ||
		string(response.Message.ToolCalls[0].Arguments) != `{"value":2}` {
		t.Fatalf("response = %#v", response)
	}
	if audit.Protocol != ProtocolOpenAIResponses || audit.RequestID != "resp_stream_1" ||
		audit.PromptTokens != 14 || audit.CompletionTokens != 5 ||
		audit.CacheReadTokens != 6 || audit.TotalTokens != 19 || audit.Error != "" {
		t.Fatalf("audit = %#v", audit)
	}
	if response.Usage.InputTokens != 14 || response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 19 || response.Usage.CacheReadTokens != 6 {
		t.Fatalf("stream response dropped request usage: %+v", response.Usage)
	}
}
