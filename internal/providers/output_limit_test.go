package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"synon-go/internal/agentruntime"
)

// A syntactically complete JSON envelope does not make a token-limited tool
// call executable. Some providers even ignore stream=true and return JSON.
func TestOutputLimitJSONNeverReturnsExecutableCalls(t *testing.T) {
	fixtures := []struct{ protocol, body string }{
		{ProtocolOpenAICompatible, `{"id":"r1","choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"text\":\"partial\"}"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":40,"total_tokens":140}}`},
		{ProtocolOpenAIResponses, `{"id":"r1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"c1","name":"write_file","arguments":"{\"text\":\"partial\"}"}],"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140}}`},
		{ProtocolAnthropic, `{"id":"r1","role":"assistant","stop_reason":"max_tokens","content":[{"type":"tool_use","id":"c1","name":"write_file","input":{"text":"partial"}}],"usage":{"input_tokens":100,"output_tokens":40}}`},
		{ProtocolGemini, `{"responseId":"r1","candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"functionCall":{"name":"write_file","args":{"text":"partial"}}}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":40,"totalTokenCount":140}}`},
	}
	for _, fixture := range fixtures {
		for _, stream := range []bool{false, true} {
			name := fixture.protocol + "/json"
			if stream {
				name += "-for-stream"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(fixture.body))
				}))
				defer server.Close()
				client, err := NewRuntimeModelClient(ModelProfile{
					Provider: ProviderProfile{Protocol: fixture.protocol, Endpoint: server.URL + "/model:generateContent"},
					Model:    "test-model", Request: RequestProfile{MaxAttempts: 3},
				}, server.Client(), nil)
				if err != nil {
					t.Fatal(err)
				}
				request := agentruntime.ModelRequest{MaxTokens: 40, Messages: []agentruntime.Message{{Role: "user", Content: "write the result"}}}
				var response agentruntime.ModelResponse
				if stream {
					response, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), request, nil)
				} else {
					response, err = client.Complete(context.Background(), request)
				}
				if !IsProviderOutputTokenLimit(err) || len(response.Message.ToolCalls) != 0 || calls.Load() != 1 {
					t.Fatalf("truncated response escaped: response=%#v err=%v calls=%d", response, err, calls.Load())
				}
				details, ok := ProviderOutputLimitDetails(err)
				if !ok || details.RequestedTokens != 40 || details.InputTokens != 100 || details.OutputTokens != 40 || details.RequestID != "r1" {
					t.Fatalf("truncation lost generation evidence: %#v ok=%v", details, ok)
				}
			})
		}
	}
}

func TestOutputLimitStreamsPreserveTerminalUsage(t *testing.T) {
	fixtures := []struct{ protocol, stream string }{
		{ProtocolOpenAICompatible, "data: {\"id\":\"r1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\"}}]},\"finish_reason\":\"length\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140}}\n\ndata: [DONE]\n\n"},
		{ProtocolAnthropic, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"r1\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":100}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"write_file\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":40}}\n\n"},
		{ProtocolGemini, "data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"write_file\",\"args\":{}}}]}}]}\n\ndata: {\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":40,\"totalTokenCount\":140}}\n\n"},
		{ProtocolOpenAIResponses, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item1\",\"call_id\":\"c1\",\"name\":\"write_file\"}}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":40,\"total_tokens\":140}}}\n\n"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.protocol, func(t *testing.T) {
			var attempts atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(fixture.stream))
			}))
			defer api.Close()
			var audit AuditRecord
			client, err := NewRuntimeModelClient(ModelProfile{Provider: ProviderProfile{Protocol: fixture.protocol, Endpoint: api.URL + "/model:generateContent"}, Model: "test", Request: RequestProfile{MaxAttempts: 3}}, api.Client(), func(record AuditRecord) { audit = record })
			if err != nil {
				t.Fatal(err)
			}
			var content strings.Builder
			response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{MaxTokens: 40}, func(event agentruntime.ModelStreamEvent) error { content.WriteString(event.ContentDelta); return nil })
			details, ok := ProviderOutputLimitDetails(err)
			if !ok || details.InputTokens != 100 || details.OutputTokens != 40 || details.RequestedTokens != 40 || details.RequestID != "r1" || attempts.Load() != 1 || len(response.Message.ToolCalls) != 0 || content.Len() != 0 || IsContinuationSafeResponseTruncation(err) {
				t.Fatalf("invalid tool interruption: details=%#v ok=%v err=%v attempts=%d response=%#v content=%q", details, ok, err, attempts.Load(), response, content.String())
			}
			if !audit.OutputTokenLimited || audit.RequestedOutputTokens != 40 || audit.PromptTokens != 100 || audit.CompletionTokens != 40 {
				t.Fatalf("audit lost terminal usage: %#v", audit)
			}
		})
	}
}

func TestOutputLimitEvidenceDoesNotInventUsageOrOverrideCancellation(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errProviderResponseTooLarge, errProviderStreamIncomplete, errors.New("finish reason length"), errors.Join(context.Canceled, responseOutputLimit("length", true)), errors.Join(context.DeadlineExceeded, responseOutputLimit("length", true))} {
		wrapped := withOutputLimitDetails(err, 40, "request", providerTokenUsage{CompletionTokens: 40})
		if wrapped != err {
			t.Fatalf("non-limit error changed: %v", err)
		}
		if _, ok := ProviderOutputLimitDetails(wrapped); ok {
			t.Fatalf("invented limit for %v", err)
		}
	}
	err := withOutputLimitDetails(responseOutputLimit("length", true), 0, "request", providerTokenUsage{})
	details, ok := ProviderOutputLimitDetails(err)
	if !ok || details.OutputTokens != 0 || details.InputTokens != 0 || details.RequestedTokens != 0 {
		t.Fatalf("invented usage: %#v", details)
	}
}

func TestOutputLimitJSONPrecedesIncompleteToolIdentityValidation(t *testing.T) {
	client := &runtimeModelClient{}
	fixtures := []struct {
		body   string
		decode func([]byte, http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error)
	}{
		{`{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"","function":{"name":"","arguments":"{"}}]}}]}`, client.openAIResponseDecoder},
		{`{"id":"r1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"","name":"","arguments":"{"}]}`, client.openAIResponsesResponseDecoder},
		{`{"id":"r1","stop_reason":"max_tokens","content":[{"type":"tool_use","id":"","name":"","input":{}}]}`, client.anthropicResponseDecoder},
		{`{"responseId":"r1","candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"functionCall":{"name":"","args":{}}}]}}]}`, client.geminiResponseDecoder},
	}
	for _, fixture := range fixtures {
		response, _, _, err := fixture.decode([]byte(fixture.body), nil)
		if !IsProviderOutputTokenLimit(err) || IsRetryableModelProtocolError(err) || len(response.Message.ToolCalls) != 0 {
			t.Fatalf("incomplete action misclassified: %#v %v", response, err)
		}
	}
}

func TestOutputLimitNativeTerminalChunkRetainsText(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"terminal\",\"candidates\":[{\"finishReason\":\"MAX_TOKENS\",\"content\":{\"parts\":[{\"text\":\"最后一段\\n\"}]}}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":40}}\n\n"))
	}))
	defer api.Close()
	client, err := NewRuntimeModelClient(ModelProfile{Provider: ProviderProfile{Protocol: ProtocolGemini, Endpoint: api.URL + "/model:generateContent"}, Model: "test"}, api.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error { content.WriteString(event.ContentDelta); return nil })
	details, ok := ProviderOutputLimitDetails(err)
	if !IsContinuationSafeResponseTruncation(err) || content.String() != "最后一段\n" || !ok || details.RequestID != "terminal" || details.OutputTokens != 40 || details.RequestedTokens != 0 {
		t.Fatalf("lost final chunk: content=%q details=%#v err=%v", content.String(), details, err)
	}
}
