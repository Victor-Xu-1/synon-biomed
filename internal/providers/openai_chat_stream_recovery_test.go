package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestOpenAIChatToolArgumentTransportRecovery(t *testing.T) {
	for _, test := range []struct {
		name, content, streamArgs, replyArgs string
		requests                             int
	}{
		{"recover", "", `[{"part":"wrong root"}]`, `{"phases":[{"name":"Research"},{"name":"Compare"}]}`, 2},
		{"valid", "", `{"query":"original"}`, `{"query":"unused"}`, 1},
		{"published_progress", "Visible progress", `[{"part":"wrong root"}]`, `{"query":"unused"}`, 1},
		{"still_invalid", "", `[{"part":"wrong root"}]`, `[{"part":"still invalid"}]`, 2},
		{"transport_unavailable", "", `[{"part":"wrong root"}]`, "", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			var first map[string]any
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if r.Header.Get("Authorization") != "Bearer test-key" || body["model"] != "same-model" {
					t.Error("provider authority changed")
				}
				if n == 1 {
					first = body
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "delta": map[string]any{
						"content": test.content, "tool_calls": []any{map[string]any{"index": 0, "id": "initial-call", "type": "function", "function": map[string]any{"name": "inspect", "arguments": test.streamArgs}}},
					}}}})
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))
					return
				}
				if n > 2 {
					t.Error("unbounded transport recovery")
				}
				for _, key := range []string{"messages", "tools", "model", "tool_choice", "temperature"} {
					if !reflect.DeepEqual(body[key], first[key]) {
						t.Errorf("recovery changed %s", key)
					}
				}
				if body["stream"] == true {
					t.Error("recovery retried broken streaming transport")
				}
				if test.name == "transport_unavailable" {
					http.Error(w, "temporary", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "repaired-call", "type": "function", "function": map[string]any{"name": "inspect", "arguments": test.replyArgs}}}}}}})
			}))
			defer origin.Close()
			client, err := NewRuntimeModelClient(ModelProfile{Provider: ProviderProfile{ID: "test", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: origin.URL}, Model: "same-model", APIKey: "test-key", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024}}, origin.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Original unchanged task"}}, Tools: []agentruntime.ToolSchema{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if int(requests.Load()) != test.requests {
				t.Fatalf("requests=%d want=%d", requests.Load(), test.requests)
			}
			if test.name == "recover" && (len(response.Message.ToolCalls) != 1 || string(response.Message.ToolCalls[0].Arguments) != test.replyArgs || response.Message.ToolCalls[0].ProviderProtocolDiagnostic != "") {
				t.Fatalf("complete parameters not recovered: %#v", response.Message.ToolCalls)
			}
			if (test.name == "still_invalid" || test.name == "transport_unavailable") && !strings.Contains(response.Message.ToolCalls[0].ProviderProtocolDiagnostic, "invalid") {
				t.Fatal("invalid response lost its recoverable diagnostic")
			}
		})
	}
}

func TestOpenAIChatToolArgumentTransportRecoveryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &streamingRuntimeModelClient{}
	_, err := client.recoverOpenAIChatToolArguments(ctx, agentruntime.ModelRequest{}, agentruntime.ModelResponse{Message: agentruntime.Message{ToolCalls: []agentruntime.ToolCall{{ProviderProtocolDiagnostic: "invalid arguments"}}}})
	if err != context.Canceled {
		t.Fatalf("cancelled recovery continued: %v", err)
	}
}
