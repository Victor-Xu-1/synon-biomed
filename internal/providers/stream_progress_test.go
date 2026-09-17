package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestProductiveProviderStreamsOutliveFormerTotalDeadline(t *testing.T) {
	const chunks = 30
	const interval = 20 * time.Millisecond
	const idle = 100 * time.Millisecond
	expected := strings.Repeat("x", chunks)
	tests := []struct {
		name, protocol, initial, delta, ending, output string
		tool                                           bool
	}{
		{name: "chat text", protocol: ProtocolOpenAICompatible,
			delta:  "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
			ending: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output: expected},
		{name: "chat reasoning", protocol: ProtocolOpenAICompatible,
			delta:  "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"x\"}}]}\n\n",
			ending: "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", output: "done"},
		{name: "chat tool arguments", protocol: ProtocolOpenAICompatible,
			initial: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"value\\\":\\\"\"}}]}}]}\n\n",
			delta:   "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"x\"}}]}}]}\n\n",
			ending:  "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", tool: true},
		{name: "anthropic text", protocol: ProtocolAnthropic,
			initial: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"role\":\"assistant\"}}\n\n",
			delta:   "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n",
			ending:  "data: {\"type\":\"message_stop\"}\n\n", output: expected},
		{name: "anthropic reasoning", protocol: ProtocolAnthropic,
			initial: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"role\":\"assistant\"}}\n\n",
			delta:   "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"x\"}}\n\n",
			ending:  "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", output: "done"},
		{name: "gemini text", protocol: ProtocolGemini,
			delta:  "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"x\"}]}}]}\n\n",
			ending: "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n", output: expected},
		{name: "responses text", protocol: ProtocolOpenAIResponses,
			delta:  "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n",
			ending: fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", expected), output: expected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.initial))
				w.(http.Flusher).Flush()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for i := 0; i < chunks; i++ {
					select {
					case <-request.Context().Done():
						return
					case <-ticker.C:
						_, _ = w.Write([]byte(test.delta))
						w.(http.Flusher).Flush()
					}
				}
				_, _ = w.Write([]byte(test.ending))
			}))
			defer server.Close()
			client, err := NewRuntimeModelClient(ModelProfile{
				Provider: ProviderProfile{ID: "progress", Protocol: test.protocol, Endpoint: server.URL + "/v1beta/models/model:generateContent"},
				Model:    "model", Request: RequestProfile{Timeout: idle, MaxAttempts: 1, MaxResponseBytes: 64 << 10},
			}, server.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var visible strings.Builder
			started := time.Now()
			result, err := client.(agentruntime.StreamingModelClient).CompleteStream(ctx, agentruntime.ModelRequest{
				Messages: []agentruntime.Message{{Role: "user", Content: "Complete this task."}},
			}, func(event agentruntime.ModelStreamEvent) error { visible.WriteString(event.ContentDelta); return nil })
			if err != nil {
				t.Fatalf("productive stream terminated: %v", err)
			}
			if time.Since(started) <= 4*idle {
				t.Fatal("stream did not cross the former deadline")
			}
			if result.Message.Content != test.output || visible.String() != test.output {
				t.Fatalf("content=%q visible=%q want=%q", result.Message.Content, visible.String(), test.output)
			}
			if test.tool && (len(result.Message.ToolCalls) != 1 || string(result.Message.ToolCalls[0].Arguments) != fmt.Sprintf("{\"value\":%q}", expected)) {
				t.Fatalf("tool arguments were truncated: %#v", result.Message.ToolCalls)
			}
		})
	}
}
