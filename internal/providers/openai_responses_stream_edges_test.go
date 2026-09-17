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

func TestRuntimeModelClientAzureResponsesStreamFallsBackToJSONAndAppliesHeaders(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("api-key") != "azure-key" || r.Header.Get("Authorization") != "" ||
			r.Header.Get("X-Tenant") != "tenant-a" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("headers = %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ms-request-id", "azure-response-1")
		_, _ = w.Write([]byte(`{"id":"azure-json-id","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"json fallback"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	var audit AuditRecord
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "azure-responses", Type: "azure-openai-responses", Protocol: ProtocolAzureOpenAIResponses, Endpoint: server.URL},
		Model:    "deployment-a", APIKey: "azure-key",
		Request: RequestProfile{Timeout: time.Second, MaxAttempts: 2, MaxResponseBytes: 4096, Headers: map[string]string{"X-Tenant": "tenant-a"}},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || response.Message.Content != "json fallback" {
		t.Fatalf("requests=%d response=%#v", requests.Load(), response)
	}
	if audit.RequestID != "azure-json-id" || audit.Protocol != ProtocolAzureOpenAIResponses ||
		audit.PromptTokens != 3 || audit.CompletionTokens != 2 || audit.TotalTokens != 5 || audit.Error != "" {
		t.Fatalf("audit=%#v", audit)
	}
}

func TestRuntimeModelClientOpenAIResponsesRetriesOnlyBeforeDelta(t *testing.T) {
	t.Run("transient status before delta", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if attempts.Add(1) == 1 {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
			_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"retry-ok\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
		}))
		defer server.Close()
		client := newResponsesStreamingTestClient(t, server, 2, 4096, nil)
		response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "retry"}},
		}, func(agentruntime.ModelStreamEvent) error { return nil })
		if err != nil || attempts.Load() != 2 || response.Message.Content != "ok" {
			t.Fatalf("attempts=%d response=%#v err=%v", attempts.Load(), response, err)
		}
	})

	t.Run("consumer error after delta", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"stop\"}\n\n"))
		}))
		defer server.Close()
		wantErr := errors.New("consumer stopped")
		var audit AuditRecord
		client := newResponsesStreamingTestClient(t, server, 3, 4096, func(record AuditRecord) { audit = record })
		_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "stop"}},
		}, func(agentruntime.ModelStreamEvent) error { return wantErr })
		if !errors.Is(err, wantErr) || attempts.Load() != 1 || audit.Attempt != 1 ||
			!strings.Contains(audit.Error, wantErr.Error()) {
			t.Fatalf("attempts=%d audit=%#v err=%v", attempts.Load(), audit, err)
		}
	})
}

func TestRuntimeModelClientOpenAIResponsesStreamEnforcesConfiguredLimitWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 512) + "\"}\n\n"))
	}))
	defer server.Close()
	client := newResponsesStreamingTestClient(t, server, 3, 128, nil)
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "large"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTooLarge) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestReadOpenAIResponsesStreamCountsDecodedSemanticsWithoutCompletedSnapshotDuplication(t *testing.T) {
	var payload strings.Builder
	for range 64 {
		payload.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"padding\":\"protocol framing\"}\n\n")
	}
	payload.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	payload.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"semantic-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")

	decoder := (&runtimeModelClient{}).openAIResponsesResponseDecoder
	response, requestID, _, emitted, err := readOpenAIResponsesStream(strings.NewReader(payload.String()), 1024,
		func(agentruntime.ModelStreamEvent) error { return nil }, decoder)
	if err != nil {
		t.Fatalf("protocol framing or completed snapshot was double-counted: %v", err)
	}
	if response.Message.Content != "ok" || requestID != "semantic-1" || !emitted {
		t.Fatalf("response=%#v requestID=%q emitted=%v", response, requestID, emitted)
	}
}

func TestReadOpenAIResponsesStreamRejectsOversizedSingleEvent(t *testing.T) {
	payload := "data: " + strings.Repeat("x", int(defaultMaxResponseBytes)+1) + "\n\n"
	decoder := (&runtimeModelClient{}).openAIResponsesResponseDecoder
	_, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(payload), defaultMaxResponseBytes, nil, decoder)
	if !errors.Is(err, errProviderResponseTooLarge) || !strings.Contains(err.Error(), "SSE event line exceeded") {
		t.Fatalf("oversized SSE event error = %v", err)
	}
}

func TestReadOpenAIResponsesStreamRejectsTruncatedCompletedResponse(t *testing.T) {
	payload := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"truncated-1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}]}}\n\n"
	decoder := (&runtimeModelClient{}).openAIResponsesResponseDecoder
	_, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(payload), defaultMaxResponseBytes, nil, decoder)
	if !errors.Is(err, errProviderResponseTruncated) {
		t.Fatalf("truncated Responses stream error = %v", err)
	}
}

func TestReadOpenAIResponsesStreamRejectsConflictingCompletedToolCall(t *testing.T) {
	tests := []struct {
		name, completed string
	}{
		{
			name:      "call id",
			completed: `{"id":"resp","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_other","name":"lookup","arguments":"{\"q\":\"NEK7\"}"}]}`,
		},
		{
			name:      "name",
			completed: `{"id":"resp","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"other","arguments":"{\"q\":\"NEK7\"}"}]}`,
		},
		{
			name:      "arguments",
			completed: `{"id":"resp","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"NLRP3\"}"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := "event: response.output_item.added\n" +
				"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\"}}\n\n" +
				"event: response.function_call_arguments.delta\n" +
				"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\\\"NEK7\\\"}\"}\n\n" +
				"event: response.completed\n" + "data: {\"type\":\"response.completed\",\"response\":" + test.completed + "}\n\n"
			decoder := (&runtimeModelClient{}).openAIResponsesResponseDecoder
			if _, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(payload), defaultMaxResponseBytes, nil, decoder); err == nil {
				t.Fatal("conflicting completed tool call unexpectedly succeeded")
			}
		})
	}
}

func TestReadOpenAIResponsesStreamRejectsEventsAfterCompleted(t *testing.T) {
	payload := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n"
	decoder := (&runtimeModelClient{}).openAIResponsesResponseDecoder
	if _, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(payload), defaultMaxResponseBytes, nil, decoder); err == nil {
		t.Fatal("post-completion event unexpectedly succeeded")
	}
}

func newResponsesStreamingTestClient(t *testing.T, server *httptest.Server, attempts int, limit int64, audit func(AuditRecord)) agentruntime.StreamingModelClient {
	t.Helper()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "responses-edge", Type: "openai-responses", Protocol: ProtocolOpenAIResponses, Endpoint: server.URL},
		Model:    "gpt-responses", Request: RequestProfile{Timeout: time.Second, MaxAttempts: attempts, MaxResponseBytes: limit},
	}, server.Client(), audit)
	if err != nil {
		t.Fatal(err)
	}
	return client.(agentruntime.StreamingModelClient)
}
