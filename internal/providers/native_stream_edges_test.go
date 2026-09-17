package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestNativeProviderStreamsFallbackToJSON(t *testing.T) {
	tests := []struct {
		name, protocol, providerType, path, body, want string
	}{
		{"anthropic", ProtocolAnthropic, "anthropic", "/v1/messages", "{\"id\":\"anth-json\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"anthropic json\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}", "anthropic json"},
		{"gemini", ProtocolGemini, "gemini", "/v1beta/models/gem:generateContent", "{\"responseId\":\"gem-json\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"gemini json\"}]}}]}", "gemini json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewRuntimeModelClient(ModelProfile{
				Provider: ProviderProfile{ID: test.name, Type: test.providerType, Protocol: test.protocol, Endpoint: server.URL + test.path},
				Model:    "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1},
			}, server.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
				Messages: []agentruntime.Message{{Role: "user", Content: "hello"}},
			}, nil)
			if err != nil || response.Message.Content != test.want {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}
}

func TestAnthropicStreamConsumerErrorStopsWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"stop-1\",\"role\":\"assistant\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"stop\"}}\n\n"))
	}))
	defer server.Close()
	wantErr := errors.New("consumer stopped")
	var audit AuditRecord
	client := newNativeStreamingTestClient(t, server, ProtocolAnthropic, "anthropic", "/v1/messages", 3, 4096, func(record AuditRecord) { audit = record })
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "stop"}},
	}, func(agentruntime.ModelStreamEvent) error { return wantErr })
	if !errors.Is(err, wantErr) || attempts.Load() != 1 || audit.Attempt != 1 || !strings.Contains(audit.Error, wantErr.Error()) {
		t.Fatalf("attempts=%d audit=%#v err=%v", attempts.Load(), audit, err)
	}
}

func TestGeminiStreamRetriesTransientFailureBeforeDelta(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-retry\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n"))
	}))
	defer server.Close()
	client := newNativeStreamingTestClient(t, server, ProtocolGemini, "gemini", "/v1beta/models/gem:generateContent", 2, 4096, nil)
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "retry"}},
	}, nil)
	if err != nil || attempts.Load() != 2 || response.Message.Content != "ok" {
		t.Fatalf("attempts=%d response=%#v err=%v", attempts.Load(), response, err)
	}
}

func TestNativeStreamConfiguredLimitDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + strings.Repeat("x", 512) + "\"}}\n\n"))
	}))
	defer server.Close()
	client := newNativeStreamingTestClient(t, server, ProtocolAnthropic, "anthropic", "/v1/messages", 3, 128, nil)
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "large"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTooLarge) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestReadNativeSSEIgnoresProtocolFramingInSemanticLimit(t *testing.T) {
	var payload strings.Builder
	for range 64 {
		payload.WriteString(": keepalive padding that is not model output\n\n")
	}
	payload.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	payload.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	var dispatched int
	err := readNativeSSE(strings.NewReader(payload.String()), 1024, func(string, []byte) error {
		dispatched++
		return nil
	})
	if err != nil {
		t.Fatalf("protocol framing counted toward semantic limit: %v", err)
	}
	if dispatched != 2 {
		t.Fatalf("dispatched events = %d, want 2", dispatched)
	}
}

func TestReadNativeSSEEnforcesDecodedSemanticLimitForAnthropicAndGemini(t *testing.T) {
	tests := []struct{ name, event string }{
		{name: "anthropic", event: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + strings.Repeat("a", 100) + "\"}}\n\n"},
		{name: "gemini", event: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"" + strings.Repeat("g", 100) + "\"}]}}]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := strings.Repeat(test.event, 11)
			err := readNativeSSE(strings.NewReader(payload), 1024, func(string, []byte) error { return nil })
			if !errors.Is(err, errProviderResponseTooLarge) || !strings.Contains(err.Error(), "decoded response exceeded") {
				t.Fatalf("semantic limit error = %v", err)
			}
		})
	}
}

func TestReadNativeSSESemanticBoundaryInGeminiTerminalEventIsNotRecoverable(t *testing.T) {
	first := strings.Repeat("a", 600*1024)
	terminal := strings.Repeat("b", 600*1024)
	payload := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"" + first + "\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"" + terminal + "\"}]}}]}\n\n"
	err := readNativeSSE(strings.NewReader(payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
	if !errors.Is(err, errProviderResponseTooLarge) || errors.Is(err, errProviderSemanticSegmentBoundary) {
		t.Fatalf("terminal semantic boundary = %v", err)
	}
}

func TestReadNativeSSESemanticBoundaryInToolEventIsNotRecoverable(t *testing.T) {
	first := strings.Repeat("a", 600*1024)
	arguments := strings.Repeat("b", 600*1024)
	payload := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"" + first + "\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"value\":\"" + arguments + "\"}}}]}}]}\n\n"
	err := readNativeSSE(strings.NewReader(payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
	if !errors.Is(err, errProviderResponseTooLarge) || errors.Is(err, errProviderSemanticSegmentBoundary) {
		t.Fatalf("tool semantic boundary = %v", err)
	}
}

func TestNativeSSEToolInputCountsTowardSemanticLimit(t *testing.T) {
	raw := []byte(`{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool-1","name":"lookup","input":{"query":"NEK7"}}}`)
	semantics := nativeSSEEventSemantics(raw)
	if semantics.TruncatedReason != "" || !semantics.HasToolCall || semantics.Bytes < int64(len(`tool-1lookup{"query":"NEK7"}`)) {
		t.Fatalf("semantics=%#v", semantics)
	}
}

func TestReadNativeSSERejectsOversizedSingleEvent(t *testing.T) {
	payload := "data: " + strings.Repeat("x", int(defaultMaxResponseBytes)+1) + "\n\n"
	err := readNativeSSE(strings.NewReader(payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
	if !errors.Is(err, errProviderResponseTooLarge) || !strings.Contains(err.Error(), "SSE event line exceeded") {
		t.Fatalf("oversized SSE event error = %v", err)
	}
}

func TestReadNativeSSERejectsProviderLengthTermination(t *testing.T) {
	tests := []struct {
		name, payload string
	}{
		{
			name:    "anthropic",
			payload: "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n",
		},
		{
			name:    "gemini",
			payload: "data: {\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := readNativeSSE(strings.NewReader(test.payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
			if !errors.Is(err, errProviderResponseTruncated) || !IsContinuationSafeResponseTruncation(err) {
				t.Fatalf("truncated stream error = %v", err)
			}
		})
	}
}

func TestReadNativeSSEClassifiesContentLengthTerminationAsContinuationSafe(t *testing.T) {
	tests := []struct {
		name, payload string
	}{
		{
			name: "anthropic",
			payload: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"durable prefix\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n",
		},
		{
			name: "gemini",
			payload: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"durable prefix\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := readNativeSSE(strings.NewReader(test.payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
			if !errors.Is(err, errProviderResponseTruncated) || !IsContinuationSafeResponseTruncation(err) {
				t.Fatalf("content truncation classification = %v", err)
			}
		})
	}
}

func TestReadNativeSSEToolLengthTerminationIsNotContinuationSafe(t *testing.T) {
	payload := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n"
	err := readNativeSSE(strings.NewReader(payload), defaultMaxResponseBytes, func(string, []byte) error { return nil })
	if !errors.Is(err, errProviderResponseTruncated) || IsContinuationSafeResponseTruncation(err) {
		t.Fatalf("tool truncation classification = %v", err)
	}
}

func TestGeminiStreamRequiresTerminalReason(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-incomplete\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"))
	}))
	defer server.Close()
	client := newNativeStreamingTestClient(t, server, ProtocolGemini, "gemini", "/v1beta/models/gem:generateContent", 3, 64*1024, nil)
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "stream"}},
	}, nil)
	if !errors.Is(err, errProviderStreamIncomplete) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestNativeStreamTokenLimitTerminationDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"gem-limited\",\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}\n\n"))
	}))
	defer server.Close()
	client := newNativeStreamingTestClient(t, server, ProtocolGemini, "gemini", "/v1beta/models/gem:generateContent", 3, 64*1024, nil)
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "stream"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTruncated) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func newNativeStreamingTestClient(t *testing.T, server *httptest.Server, protocol, providerType, path string, attempts int, limit int64, audit func(AuditRecord)) agentruntime.StreamingModelClient {
	t.Helper()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "native-edge", Type: providerType, Protocol: protocol, Endpoint: server.URL + path},
		Model:    "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: attempts, MaxResponseBytes: limit},
	}, server.Client(), audit)
	if err != nil {
		t.Fatal(err)
	}
	return client.(agentruntime.StreamingModelClient)
}
