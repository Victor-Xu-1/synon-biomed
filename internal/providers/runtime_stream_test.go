package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestRuntimeModelClientOpenAIChatStreamsTextToolsAndUsage(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model       string  `json:"model"`
			Stream      bool    `json:"stream"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
			ToolChoice  struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_choice"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Model != "gpt-stream" || !payload.Stream || !payload.StreamOptions.IncludeUsage || payload.Temperature != 0.45 || payload.MaxTokens != 8192 ||
			payload.ToolChoice.Type != "function" || payload.ToolChoice.Function.Name != "lookup" {
			t.Errorf("payload = %#v", payload)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if attempt == 1 {
			w.Header().Set("x-request-id", "stream-retry-1")
			http.Error(w, "temporary stream failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "stream-ok-2")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		for _, event := range []string{
			`{"id":"chatcmpl-stream","choices":[{"delta":{"role":"assistant","content":"hel"}}]}`,
			`{"id":"chatcmpl-stream","choices":[{"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`{"id":"chatcmpl-stream","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`{"id":"chatcmpl-stream","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":5}}}`,
		} {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	audits := make(chan AuditRecord, 2)
	profileTemperature := 0.45
	profileMaxTokens := 8192
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "openai-stream", Type: "openai", Protocol: ProtocolOpenAICompatible,
			Endpoint: server.URL + "/v1/chat/completions",
		},
		Model: "gpt-stream", APIKey: "stream-key", Temperature: &profileTemperature, MaxTokens: &profileMaxTokens,
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 2, MaxResponseBytes: 64 * 1024},
	}, server.Client(), func(record AuditRecord) { audits <- record })
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := client.(agentruntime.StreamingModelClient)
	if !ok {
		t.Fatalf("client %T does not implement StreamingModelClient", client)
	}
	deltas := make([]string, 0)
	kinds := make([]agentruntime.ModelStreamEventKind, 0, 3)
	response, err := streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages:   []agentruntime.Message{{Role: "user", Content: "stream"}},
		Tools:      []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: map[string]any{"type": "tool", "name": "lookup"},
	}, func(event agentruntime.ModelStreamEvent) error {
		deltas = append(deltas, event.ContentDelta)
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || strings.Join(deltas, "") != "hello" {
		t.Fatalf("attempts=%d deltas=%#v", attempts.Load(), deltas)
	}
	if !reflect.DeepEqual(kinds, []agentruntime.ModelStreamEventKind{
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventContentDelta,
		agentruntime.ModelStreamEventToolCallBoundary,
	}) {
		t.Fatalf("stream event kinds = %#v", kinds)
	}
	if response.Message.Role != "assistant" || response.Message.Content != "hello" ||
		len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].ID != "call_1" ||
		response.Message.ToolCalls[0].Name != "lookup" ||
		string(response.Message.ToolCalls[0].Arguments) != `{"q":"x"}` {
		t.Fatalf("response = %#v", response)
	}
	first, second := <-audits, <-audits
	if first.HTTPStatus != http.StatusServiceUnavailable || first.RequestID != "stream-retry-1" ||
		!strings.Contains(first.Error, "temporary stream failure") {
		t.Fatalf("first audit = %#v", first)
	}
	if second.HTTPStatus != http.StatusOK || second.RequestID != "chatcmpl-stream" ||
		second.PromptTokens != 12 || second.CompletionTokens != 4 ||
		second.CacheReadTokens != 5 || second.TotalTokens != 16 || second.Error != "" {
		t.Fatalf("second audit = %#v", second)
	}
}

func TestRuntimeModelClientNativeStreamsEncodeNamedToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		protocol string
		path     string
		assert   func(*testing.T, map[string]any)
	}{
		{
			name: "openai responses", typeName: "openai", protocol: ProtocolOpenAIResponses, path: "/v1/responses",
			assert: func(t *testing.T, payload map[string]any) {
				choice, _ := payload["tool_choice"].(map[string]any)
				if choice["type"] != "function" || choice["name"] != "lookup" {
					t.Fatalf("tool_choice = %#v", choice)
				}
			},
		},
		{
			name: "anthropic", typeName: "anthropic", protocol: ProtocolAnthropic, path: "/v1/messages",
			assert: func(t *testing.T, payload map[string]any) {
				choice, _ := payload["tool_choice"].(map[string]any)
				if choice["type"] != "tool" || choice["name"] != "lookup" {
					t.Fatalf("tool_choice = %#v", choice)
				}
			},
		},
		{
			name: "gemini", typeName: "gemini", protocol: ProtocolGemini, path: "/v1beta/models/model:generateContent",
			assert: func(t *testing.T, payload map[string]any) {
				toolConfig, _ := payload["toolConfig"].(map[string]any)
				calling, _ := toolConfig["functionCallingConfig"].(map[string]any)
				names, _ := calling["allowedFunctionNames"].([]any)
				if calling["mode"] != "ANY" || len(names) != 1 || names[0] != "lookup" {
					t.Fatalf("toolConfig = %#v", toolConfig)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				captured <- payload
				http.Error(w, "capture complete", http.StatusBadRequest)
			}))
			defer server.Close()

			client, err := NewRuntimeModelClient(ModelProfile{
				Provider: ProviderProfile{
					ID: test.name, Type: test.typeName, Protocol: test.protocol, Endpoint: server.URL + test.path,
				},
				Model: "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
			}, server.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
				Messages:   []agentruntime.Message{{Role: "user", Content: "use the required tool"}},
				Tools:      []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
				ToolChoice: map[string]any{"type": "tool", "name": "lookup"},
			}, nil)
			if err == nil {
				t.Fatal("expected capture response to stop the stream")
			}
			test.assert(t, <-captured)
		})
	}
}

func TestRuntimeModelClientArkThinkingChatStreamUsesAutoToolChoice(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"ark-stream\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"accepted\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "ark", Type: "volcengine-ark", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages:   []agentruntime.Message{{Role: "user", Content: "use ask_user"}},
		Tools:      []agentruntime.ToolSchema{{Name: "ask_user"}},
		ToolChoice: map[string]any{"type": "tool", "name": "ask_user"},
	}, nil)
	if err != nil || response.Message.Content != "accepted" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if got := (<-captured)["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice=%#v want auto", got)
	}
}

func TestRuntimeModelClientArkStreamCanDisableReasoningForPresentationTurn(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"ark-presentation\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"自然说明\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "ark", Type: "volcengine-ark", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "说明下一步科学判断"}},
		ToolChoice:    "none",
		ReasoningMode: agentruntime.ReasoningModeDisabled,
	}, nil)
	if err != nil || response.Message.Content != "自然说明" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	thinking, _ := (<-captured)["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v want disabled", thinking)
	}
}

func TestRuntimeModelClientOpenAIChatDoesNotFallbackForToolGeneratedMedia(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !bytes.Contains(body, []byte(`"image_url"`)) {
			t.Errorf("request did not contain tool media: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"Model only support text input","type":"BadRequest"}}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "text-only-tool-media", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "tool", ToolCallID: "read-image", Content: `{"ok":true,"review_evidence_receipt":{"receiptId":"review-read-1"}}`},
			{Role: "user", Parts: []agentruntime.ContentPart{
				{Type: agentruntime.ContentPartText, Text: "Visual content returned by file inspection tools."},
				{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{
					MIMEType: "image/png", Filename: "reference.png",
					Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("png-bytes")},
				}},
			}},
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "only support text input") || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestRuntimeModelClientDoesNotStripDirectUserMediaForTextOnlyEndpoint(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Model only support text input"}}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "text-only-user-media", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "inspect my uploaded image"},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{
				MIMEType: "image/png", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("png-bytes")},
			}},
		}}},
	}, nil)
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestRuntimeModelClientOpenAIChatStopsWhenDeltaConsumerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat-stop\",\"choices\":[{\"delta\":{\"content\":\"chunk\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var audit AuditRecord
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "openai-stream", Type: "openai", Protocol: ProtocolOpenAICompatible,
			Endpoint: server.URL + "/v1/chat/completions",
		},
		Model:   "gpt-stream",
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 2},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	streaming := client.(agentruntime.StreamingModelClient)
	wantErr := errors.New("consumer stopped")
	_, err = streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "stream"}},
	}, func(agentruntime.ModelStreamEvent) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if !strings.Contains(audit.Error, wantErr.Error()) || audit.Attempt != 1 {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRuntimeModelClientOpenAIChatRoundTripsMiMoReasoningAcrossToolRound(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload["max_completion_tokens"] != float64(32768) {
			t.Errorf("max_completion_tokens = %#v", payload["max_completion_tokens"])
		}
		if _, exists := payload["max_tokens"]; exists {
			t.Errorf("MiMo request unexpectedly contained max_tokens: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte("data: {\"id\":\"mimo-1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"private \"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"mimo-1\",\"choices\":[{\"delta\":{\"reasoning_content\":\"trace\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}]}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) < 3 {
			t.Errorf("second request messages = %#v", payload["messages"])
			http.Error(w, "missing tool history", http.StatusBadRequest)
			return
		}
		assistant, _ := messages[1].(map[string]any)
		if assistant["reasoning_content"] != "private trace" {
			t.Errorf("assistant reasoning_content = %#v", assistant["reasoning_content"])
			http.Error(w, "missing reasoning_content", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"mimo-2\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"done\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	maxTokens := 32768
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID:       "mimo",
			Type:     "custom",
			Protocol: ProtocolOpenAICompatible,
			BaseURL:  "https://token-plan-cn.xiaomimimo.com/v1",
			Endpoint: server.URL + "/v1/chat/completions",
		},
		Model:     "mimo-v2.5",
		MaxTokens: &maxTokens,
		Request:   RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deltas := make([]string, 0, 1)
	reasoningActivities := 0
	engine := agentruntime.Engine{
		Model: client,
		Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
			return agentruntime.ToolResult{Value: map[string]any{"result": "ok"}}, nil
		}),
		OnModelDelta: func(event agentruntime.ModelStreamEvent) error {
			if event.ReasoningActive {
				reasoningActivities++
			}
			deltas = append(deltas, event.ContentDelta)
			return nil
		},
	}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "use a tool"}},
		Tools:         []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || result.FinalMessage.Content != "done" {
		t.Fatalf("attempts=%d result=%#v", attempts.Load(), result)
	}
	if len(result.Messages) < 2 || result.Messages[1].ReasoningContent != "private trace" {
		t.Fatalf("reasoning was not retained in engine history: %#v", result.Messages)
	}
	if strings.Join(deltas, "") != "done" || reasoningActivities != 2 {
		t.Fatalf("reasoning activity=%d visible deltas=%#v", reasoningActivities, deltas)
	}
}

func TestRuntimeModelClientOpenAIChatStreamLimitDoesNotRetryWithoutVisibleDelta(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"" + strings.Repeat("x", 256) + "\"}}]}\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "bounded", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "reasoning-model",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 3, MaxResponseBytes: 128},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "large"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTooLarge) {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("oversized provider response attempts = %d, want 1", attempts.Load())
	}
}

func TestRuntimeModelClientOpenAIChatDoesNotCompleteTruncatedReasoning(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"unfinished","tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":"length"}]}

`))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "truncated", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "reasoning-model",
		Request:  RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "reason"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTruncated) || !IsProviderOutputTokenLimit(err) || IsContinuationSafeResponseTruncation(err) {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("truncated provider response attempts = %d, want 1", attempts.Load())
	}
}

func TestRuntimeModelClientOpenAIChatClassifiesEmptyLengthAsContinuationSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "empty-truncation", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "bounded-model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue"}},
	}, nil)
	if !errors.Is(err, errProviderResponseTruncated) || !IsContinuationSafeResponseTruncation(err) {
		t.Fatalf("empty length classification = %v", err)
	}
}

func TestRuntimeModelClientOpenAIChatPreservesJSONLengthPrefixForContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"json-length","choices":[{"finish_reason":"length","message":{"role":"assistant","content":"durable prefix"}}]}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "json-truncation", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "bounded-model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		content.WriteString(event.ContentDelta)
		return nil
	})
	if !IsContinuationSafeResponseTruncation(err) || content.String() != "durable prefix" {
		t.Fatalf("JSON length result: content=%q err=%v", content.String(), err)
	}
}

func TestReadOpenAIChatStreamIgnoresProtocolFramingInSemanticLimit(t *testing.T) {
	payload := strings.Repeat(": keepalive\n\n", 256) +
		"data: {\"id\":\"bounded\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	emit := func(agentruntime.ModelStreamEvent) error { return nil }
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 1024, emit)
	if err != nil {
		t.Fatalf("protocol framing counted toward semantic limit: %v", err)
	}
	if response.Message.Content != "ok" {
		t.Fatalf("response = %#v", response)
	}
}

func TestReadOpenAIChatStreamHonorsExactSemanticLimit(t *testing.T) {
	accumulator := openAIChatStreamAccumulator{semanticLimit: 6}
	if err := accumulator.reserveSemanticBytes(6); err != nil {
		t.Fatalf("exact semantic limit rejected: %v", err)
	}
	if err := accumulator.reserveSemanticBytes(1); !errors.Is(err, errProviderResponseTooLarge) {
		t.Fatalf("semantic limit+1 error = %v", err)
	}
}

func TestReadOpenAIChatStreamRejectsOversizedSingleEvent(t *testing.T) {
	payload := "data: " + strings.Repeat("x", int(defaultMaxResponseBytes)+1) + "\n\n"
	if _, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), defaultMaxResponseBytes, nil); !errors.Is(err, errProviderResponseTooLarge) {
		t.Fatalf("oversized SSE event error = %v", err)
	}
}

func TestReadOpenAIChatStreamRequiresExplicitTermination(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "partial arguments",
			payload: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
		},
		{
			name:    "valid arguments without terminal marker",
			payload: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, _, err := readOpenAIChatStream(strings.NewReader(test.payload), 64*1024, nil)
			if !errors.Is(err, errProviderResponseTruncated) || !errors.Is(err, errProviderStreamIncomplete) {
				t.Fatalf("error = %v, want incomplete truncated response", err)
			}
			if strings.Contains(err.Error(), "invalid JSON arguments") {
				t.Fatalf("physical truncation was misclassified: %v", err)
			}
		})
	}
}

func TestReadOpenAIChatStreamAcceptsProtocolTerminalVariants(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "explicit finish reason at eof",
			payload: `data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}

`,
		},
		{
			name: "done marker",
			payload: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}

data: [DONE]

`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, _, _, _, err := readOpenAIChatStream(strings.NewReader(test.payload), 64*1024, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].ID != "call-1" || string(response.Message.ToolCalls[0].Arguments) != "{}" {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestReadOpenAIChatStreamAcceptsCumulativeFunctionArguments(t *testing.T) {
	payload := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"web","arguments":"{\"query\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"web_fetch","arguments":"{\"query\":\"osim"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"web_fetch","arguments":"{\"query\":\"osimertinib\"}"}}]}}]}

`
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 64*1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != "web_fetch" ||
		string(response.Message.ToolCalls[0].Arguments) != `{"query":"osimertinib"}` {
		t.Fatalf("response=%#v", response)
	}
}

func TestNormalizeOpenAIChatStreamArgumentsRecoversCompatibleSnapshots(t *testing.T) {
	tests := map[string]string{
		`{"query":"old"}{"query":"new"}`:                                   `{"query":"new"}`,
		`"{\"query\":\"encoded\"}"`:                                        `{"query":"encoded"}`,
		"{\"file_path\":\"report.md\",\"content\":\"line one\nline two\"}": `{"file_path":"report.md","content":"line one\nline two"}`,
		`prefix{"query":"stable"}"} trailing`:                              `{"query":"stable"}`,
		`{"outer":{"nested":true}} duplicated-tail`:                        `{"outer":{"nested":true}}`,
	}
	for input, want := range tests {
		got, ok := normalizeOpenAIChatStreamArguments(input)
		if !ok || got != want {
			t.Fatalf("normalize(%q)=%q ok=%t want=%q", input, got, ok, want)
		}
	}
	merged, growth := mergeOpenAIChatStreamFragment(`{"query":"osi`, `osimertinib"}`)
	if merged != `{"query":"osimertinib"}` || growth != len(`mertinib"}`) {
		t.Fatalf("overlap merge=%q growth=%d", merged, growth)
	}
	if got, ok := normalizeOpenAIChatStreamArguments(`{"query":"old"}{"query":"unfinished"`); ok {
		t.Fatalf("ambiguous trailing snapshot normalized to %q", got)
	}
	if got, ok := normalizeOpenAIChatStreamArguments(`["invalid","tool","shape"]`); ok {
		t.Fatalf("non-object tool arguments normalized to %q", got)
	}
}

func TestMergeOpenAIChatStreamFragmentNeverDeduplicatesTokenSizedDeltas(t *testing.T) {
	fragments := []string{`{"file_path":"`, `report.md`, `","content":"`, `line`, ` one`, `"}`}
	merged := ""
	for _, fragment := range fragments {
		merged, _ = mergeOpenAIChatStreamFragment(merged, fragment)
	}
	want := `{"file_path":"report.md","content":"line one"}`
	if merged != want {
		t.Fatalf("token delta merge=%q want=%q", merged, want)
	}
	if normalized, ok := normalizeOpenAIChatStreamArguments(merged); !ok || normalized != want {
		t.Fatalf("normalized token delta merge=%q ok=%t want=%q", normalized, ok, want)
	}
}

func TestMergeOpenAIChatStreamFragmentDoesNotReplaceArgumentsWithEmbeddedEmptyObject(t *testing.T) {
	// A Python dictionary literal can arrive as one valid-JSON-looking token
	// while it is actually inside the outer code string. Treating that token as
	// a cumulative argument snapshot discards everything streamed before it.
	fragments := []string{`{"code":"value = `, `{}`, `\nprint(value)"}`}
	merged := ""
	for _, fragment := range fragments {
		merged, _ = mergeOpenAIChatStreamFragment(merged, fragment)
	}
	want := `{"code":"value = {}\nprint(value)"}`
	if merged != want {
		t.Fatalf("embedded object merge=%q want=%q", merged, want)
	}
	if normalized, ok := normalizeOpenAIChatStreamArguments(merged); !ok || normalized != want {
		t.Fatalf("normalized embedded object merge=%q ok=%t want=%q", normalized, ok, want)
	}
}

func TestResolveOpenAIChatStreamArgumentsPreservesAmbiguousTrueDeltaOverlap(t *testing.T) {
	parts := []string{`{"content":"abc`, `abcxyz"}`}
	merged := ""
	for _, part := range parts {
		merged, _ = mergeOpenAIChatStreamFragment(merged, part)
	}
	want := `{"content":"abcabcxyz"}`
	got, ok := resolveOpenAIChatStreamArguments(parts, merged)
	if !ok || got != want {
		t.Fatalf("resolved=%q ok=%t want=%q merged=%q", got, ok, want, merged)
	}
}

func TestResolveOpenAIChatStreamArgumentsUsesLatestCumulativeSnapshot(t *testing.T) {
	parts := []string{`{"path":`, `{"path":"report.md","destination":`, `{"path":"report.md","destination":"snapshot"}`}
	merged := ""
	for _, part := range parts {
		merged, _ = mergeOpenAIChatStreamFragment(merged, part)
	}
	want := `{"path":"report.md","destination":"snapshot"}`
	got, ok := resolveOpenAIChatStreamArguments(parts, merged)
	if !ok || got != want {
		t.Fatalf("resolved=%q ok=%t want=%q merged=%q", got, ok, want, merged)
	}
}

func TestOpenAIChatStreamArgumentsDiagnosticDoesNotExposeValues(t *testing.T) {
	diagnostic := openAIChatStreamArgumentsDiagnostic(`{"secret":"do-not-log"`, 3)
	if strings.Contains(diagnostic, "secret") || strings.Contains(diagnostic, "do-not-log") {
		t.Fatalf("diagnostic exposed argument values: %s", diagnostic)
	}
	for _, marker := range []string{"bytes=22", "fragments=3", "start=object_open", "end=quote", "valid_json=false"} {
		if !strings.Contains(diagnostic, marker) {
			t.Fatalf("diagnostic %q missing %q", diagnostic, marker)
		}
	}
}

func TestReadOpenAIChatStreamRecoversCompletedArgumentsBeforeOverlappingTail(t *testing.T) {
	payload := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"web_fetch","arguments":"{\"query\":\"osimertinib\"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"} duplicated-tail"}}]}}]}

data: [DONE]

`
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 64*1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.ToolCalls) != 1 ||
		string(response.Message.ToolCalls[0].Arguments) != `{"query":"osimertinib"}` {
		t.Fatalf("response=%#v", response)
	}
}

func TestReadOpenAIChatStreamRejectsAmbiguousToolCallIdentity(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "missing tool index",
			payload: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}

data: [DONE]

`,
		},
		{
			name: "changed call id",
			payload: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-2","type":"function","function":{"arguments":"}"}}]}}]}

data: [DONE]

`,
		},
		{
			name: "second choice",
			payload: `data: {"choices":[{"index":0,"delta":{"content":"first"}},{"index":1,"delta":{"content":"second"}}]}

data: [DONE]

`,
		},
		{
			name: "duplicate choice index",
			payload: `data: {"choices":[{"index":0,"delta":{"content":"first"}},{"index":0,"delta":{"content":"duplicate"}}]}

data: [DONE]

`,
		},
		{
			name: "changed request id",
			payload: `data: {"id":"request-1","choices":[{"index":0,"delta":{"content":"first"}}]}

data: {"id":"request-2","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}

data: [DONE]

`,
		},
		{
			name: "changed role",
			payload: `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"first"}}]}

data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"user"}}]}

data: [DONE]

`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, _, err := readOpenAIChatStream(strings.NewReader(test.payload), 64*1024, nil); err == nil {
				t.Fatal("ambiguous provider stream unexpectedly succeeded")
			}
		})
	}
}

func TestReadOpenAIChatStreamDoesNotEchoUnknownFinishReason(t *testing.T) {
	const secretReason = "private-token-shaped-reason"
	payload := `data: {"choices":[{"index":0,"finish_reason":"` + secretReason + `","delta":{}}]}

data: [DONE]

`
	_, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), defaultMaxResponseBytes, nil)
	if err == nil || strings.Contains(err.Error(), secretReason) {
		t.Fatalf("error leaked provider-controlled finish reason: %v", err)
	}
}

func TestReadOpenAIChatStreamReassemblesInterleavedToolCalls(t *testing.T) {
	payload := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-0","type":"function","function":{"name":"look","arguments":"{\"q\":"}},{"index":1,"id":"call-1","type":"function","function":{"name":"save","arguments":"{\"v\":"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}},{"index":0,"function":{"name":"up","arguments":"\"NEK7\"}"}}]}}]}

data: [DONE]

`
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 64*1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.ToolCalls) != 2 || response.Message.ToolCalls[0].Name != "lookup" ||
		string(response.Message.ToolCalls[0].Arguments) != `{"q":"NEK7"}` ||
		response.Message.ToolCalls[1].Name != "save" || string(response.Message.ToolCalls[1].Arguments) != `{"v":2}` {
		t.Fatalf("tool calls = %#v", response.Message.ToolCalls)
	}
}

func TestRuntimeModelClientOpenAIChatRetriesIncompleteStreamBeforeVisibleOutput(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"unfinished","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "retry-incomplete", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 2, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "retry"}},
	}, nil)
	if err != nil || response.Message.Content != "recovered" || attempts.Load() != 2 {
		t.Fatalf("response=%#v attempts=%d err=%v", response, attempts.Load(), err)
	}
}

func TestRuntimeModelClientOpenAIChatDoesNotReplayVisibleContentAfterIncompleteStream(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"visible"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "no-replay", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "do not replay"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		visible.WriteString(event.ContentDelta)
		return nil
	})
	if !errors.Is(err, errProviderStreamIncomplete) || !IsRecoverableStreamInterruption(err) ||
		attempts.Load() != 1 || visible.String() != "visible" {
		t.Fatalf("attempts=%d visible=%q err=%v", attempts.Load(), visible.String(), err)
	}
}

type openAIChatRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn openAIChatRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type openAIChatInterruptedBody struct {
	reader *strings.Reader
}

func (body *openAIChatInterruptedBody) Read(buffer []byte) (int, error) {
	read, err := body.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		return read, io.ErrUnexpectedEOF
	}
	return read, err
}

func (*openAIChatInterruptedBody) Close() error { return nil }

func TestRuntimeModelClientOpenAIChatClassifiesReadErrorAfterVisibleOutput(t *testing.T) {
	const payload = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"}}]}\n\n"
	var attempts atomic.Int32
	httpClient := &http.Client{Transport: openAIChatRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &openAIChatInterruptedBody{reader: strings.NewReader(payload)},
		}, nil
	})}
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "read-interruption", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: "https://provider.invalid/chat"},
		Model:    "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, httpClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "read"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		visible.WriteString(event.ContentDelta)
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !IsRecoverableStreamInterruption(err) ||
		attempts.Load() != 1 || visible.String() != "visible" {
		t.Fatalf("attempts=%d visible=%q err=%v", attempts.Load(), visible.String(), err)
	}
}

func TestRuntimeModelClientOpenAIChatIdleTimeoutAfterVisibleOutputIsRecoverable(t *testing.T) {
	requestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer close(requestDone)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "idle-interruption", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 80 * time.Millisecond, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "idle"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		visible.WriteString(event.ContentDelta)
		return nil
	})
	if !IsRecoverableStreamInterruption(err) || visible.String() != "visible" {
		t.Fatalf("visible=%q err=%v", visible.String(), err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not cancel the provider request")
	}
}

func TestRuntimeModelClientOpenAIChatHeartbeatWithoutOutputTimesOut(t *testing.T) {
	requestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer close(requestDone)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-request.Context().Done():
				return
			case <-ticker.C:
				_, _ = w.Write([]byte(": keepalive\n\n"))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "total-interruption", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 50 * time.Millisecond, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "total"}},
	}, func(agentruntime.ModelStreamEvent) error { return nil })
	if !IsRecoverableStreamInterruption(err) || !errors.Is(err, errOpenAIChatStreamIdleTimeout) {
		t.Fatalf("continued-activity timeout error = %v", err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("no-progress timeout did not cancel the provider request")
	}
}

func TestIsContinuationSafeStreamFailureRequiresAStreamTransportCause(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "idle timeout", err: errOpenAIChatStreamIdleTimeout, want: true},
		{name: "incomplete", err: fmt.Errorf("%w: %w", errProviderResponseTruncated, errProviderStreamIncomplete), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "first byte timeout", err: errOpenAIChatStreamFirstByteTimeout, want: false},
		{name: "response too large", err: errProviderResponseTooLarge, want: false},
		{name: "ordinary error", err: errors.New("provider rejected request"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsContinuationSafeStreamFailure(test.err); got != test.want {
				t.Fatalf("IsContinuationSafeStreamFailure(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestIsRetryableProviderTransportFailureUsesTypedCause(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare eof", err: io.EOF, want: true},
		{name: "wrapped eof", err: fmt.Errorf("post provider request: %w", io.EOF), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "caller cancelled", err: context.Canceled, want: false},
		{name: "caller deadline", err: context.DeadlineExceeded, want: false},
		{name: "first byte timeout", err: errOpenAIChatStreamFirstByteTimeout, want: false},
		{name: "provider response too large", err: errProviderResponseTooLarge, want: false},
		{name: "ordinary provider rejection", err: errors.New("provider rejected request"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetryableProviderTransportFailure(test.err); got != test.want {
				t.Fatalf("IsRetryableProviderTransportFailure(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestProviderEmptyResponseClassificationSurvivesWrapping(t *testing.T) {
	err := newProviderEmptyResponseError("OpenAI chat stream response has no output")
	if err.Error() != "OpenAI chat stream response has no output" {
		t.Fatalf("empty response message = %q", err)
	}
	if !IsProviderEmptyResponse(err) ||
		!IsProviderEmptyResponse(fmt.Errorf("complete model turn: %w", err)) {
		t.Fatalf("empty response classification was not preserved: %v", err)
	}
	if IsProviderEmptyResponse(errors.New("OpenAI chat stream response has no output")) {
		t.Fatal("plain text was classified as a typed empty provider response")
	}
}

func TestStreamingProtocolsPreserveTypedTransportFailureBeforeSemanticOutput(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		protocol string
		endpoint string
	}{
		{name: "openai chat", typeName: "openai", protocol: ProtocolOpenAICompatible, endpoint: "https://provider.invalid/v1/chat/completions"},
		{name: "openai responses", typeName: "openai-responses", protocol: ProtocolOpenAIResponses, endpoint: "https://provider.invalid/v1/responses"},
		{name: "azure openai chat", typeName: "azure-openai", protocol: ProtocolAzureOpenAI, endpoint: "https://provider.invalid/openai/chat/completions"},
		{name: "azure openai responses", typeName: "azure-openai-responses", protocol: ProtocolAzureOpenAIResponses, endpoint: "https://provider.invalid/openai/responses"},
		{name: "anthropic", typeName: "anthropic", protocol: ProtocolAnthropic, endpoint: "https://provider.invalid/v1/messages"},
		{name: "gemini", typeName: "gemini", protocol: ProtocolGemini, endpoint: "https://provider.invalid/v1beta/models/model:generateContent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			httpClient := &http.Client{Transport: openAIChatRoundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return nil, io.EOF
			})}
			client, err := NewRuntimeModelClient(ModelProfile{
				Provider: ProviderProfile{
					ID: test.name, Type: test.typeName, Protocol: test.protocol, Endpoint: test.endpoint,
				},
				Model: "model", APIKey: "test-key",
				Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
			}, httpClient, nil)
			if err != nil {
				t.Fatal(err)
			}
			emitted := false
			_, err = client.(agentruntime.StreamingModelClient).CompleteStream(
				context.Background(),
				agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "transport parity"}}},
				func(agentruntime.ModelStreamEvent) error { emitted = true; return nil },
			)
			if !IsRetryableProviderTransportFailure(err) || emitted || attempts.Load() != 1 {
				t.Fatalf("attempts=%d emitted=%t typed_transport=%t err=%v",
					attempts.Load(), emitted, IsRetryableProviderTransportFailure(err), err)
			}
		})
	}
}

func TestIsContinuationSafeResponseTruncationRequiresContentOnlyBoundary(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "content only", err: newProviderContentResponseTruncation("length"), want: true},
		{name: "ordinary truncation", err: fmt.Errorf("%w: finish reason length", errProviderResponseTruncated), want: false},
		{name: "incomplete stream", err: fmt.Errorf("%w: %w", newProviderContentResponseTruncation("length"), errProviderStreamIncomplete), want: false},
		{name: "response too large", err: fmt.Errorf("%w: %w", newProviderContentResponseTruncation("length"), errProviderResponseTooLarge), want: false},
		{name: "ordinary error", err: errors.New("provider rejected request"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsContinuationSafeResponseTruncation(test.err); got != test.want {
				t.Fatalf("IsContinuationSafeResponseTruncation(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestRuntimeModelClientOpenAIChatFirstByteTimeoutRetriesWithoutRecoverableClassification(t *testing.T) {
	var attempts atomic.Int32
	releaseHandlers := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		select {
		case <-request.Context().Done():
		case <-releaseHandlers:
		}
	}))
	defer server.Close()
	defer close(releaseHandlers)

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "first-byte", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 40 * time.Millisecond, MaxAttempts: 2, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "headers"}},
	}, nil)
	if !errors.Is(err, errOpenAIChatStreamFirstByteTimeout) || IsRecoverableStreamInterruption(err) || attempts.Load() != 2 {
		t.Fatalf("attempts=%d first-byte error=%v", attempts.Load(), err)
	}
}

func TestRuntimeModelClientOpenAIChatParentCancellationIsNotRecoverable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "parent-cancel", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "cancel"}},
	}, func(agentruntime.ModelStreamEvent) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || IsRecoverableStreamInterruption(err) {
		t.Fatalf("parent cancellation error = %v", err)
	}
}

func TestRuntimeModelClientOpenAIChatDeterministicFailuresAreNotRecoverable(t *testing.T) {
	tests := []struct {
		name   string
		status int
		limit  int64
		body   string
	}{
		{name: "http 400", status: http.StatusBadRequest, body: `{"error":"invalid request"}`},
		{name: "protocol json", status: http.StatusOK, body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"}}]}\n\ndata: {not-json}\n\n"},
		{name: "content filter", status: http.StatusOK, body: "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"content_filter\",\"delta\":{\"content\":\"visible\"}}]}\n\ndata: [DONE]\n\n"},
		{name: "size limit", status: http.StatusOK, limit: 128, body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + strings.Repeat("x", 256) + "\"}}]}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.status == http.StatusOK {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			limit := test.limit
			if limit == 0 {
				limit = 64 * 1024
			}
			client, err := NewRuntimeModelClient(ModelProfile{
				Provider: ProviderProfile{ID: "deterministic", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
				Model:    "model", Request: RequestProfile{Timeout: time.Second, MaxAttempts: 1, MaxResponseBytes: limit},
			}, server.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
				Messages: []agentruntime.Message{{Role: "user", Content: "invalid"}},
			}, func(agentruntime.ModelStreamEvent) error { return nil })
			if err == nil || IsRecoverableStreamInterruption(err) {
				t.Fatalf("deterministic failure = %v", err)
			}
		})
	}
}

func TestOpenAIChatStreamTimeoutsHonorConfiguredIdleWindow(t *testing.T) {
	for _, window := range []time.Duration{80 * time.Millisecond, 90 * time.Second, time.Hour} {
		firstByte, idle := openAIChatStreamTimeouts(window)
		if firstByte != window || idle != window {
			t.Fatalf("configured=%s first-byte=%s idle=%s", window, firstByte, idle)
		}
	}
	firstByte, idle := openAIChatStreamTimeouts(0)
	if firstByte <= 0 || idle <= 0 {
		t.Fatal("default inactivity windows must be finite")
	}
}

type providerStreamParityCase struct {
	name     string
	typeName string
	protocol string
	path     string
	visible  string
}

func providerStreamParityCases() []providerStreamParityCase {
	return []providerStreamParityCase{
		{
			name: "anthropic", typeName: "anthropic", protocol: ProtocolAnthropic, path: "/v1/messages",
			visible: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"anthropic-partial\",\"role\":\"assistant\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"visible\"}}\n\n",
		},
		{
			name: "gemini", typeName: "gemini", protocol: ProtocolGemini, path: "/v1beta/models/gem:generateContent",
			visible: "data: {\"responseId\":\"gemini-partial\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible\"}]}}]}\n\n",
		},
		{
			name: "openai responses", typeName: "openai", protocol: ProtocolOpenAIResponses, path: "/v1/responses",
			visible: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n",
		},
	}
}

func newProviderStreamParityClient(t *testing.T, test providerStreamParityCase, endpoint string, timeout time.Duration, httpClient *http.Client) agentruntime.StreamingModelClient {
	t.Helper()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: test.name, Type: test.typeName, Protocol: test.protocol, Endpoint: endpoint + test.path,
		},
		Model: "model", Request: RequestProfile{Timeout: timeout, MaxAttempts: 3, MaxResponseBytes: 64 * 1024},
	}, httpClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client.(agentruntime.StreamingModelClient)
}

func TestNativeProviderStreamsClassifyRecoverableInterruptionsAfterVisibleOutput(t *testing.T) {
	modes := []struct {
		name    string
		timeout time.Duration
		cause   error
	}{
		{name: "eof", timeout: time.Second, cause: errProviderStreamIncomplete},
		{name: "read error", timeout: time.Second, cause: io.ErrUnexpectedEOF},
		{name: "idle timeout", timeout: 80 * time.Millisecond, cause: errOpenAIChatStreamIdleTimeout},
		{name: "heartbeat only", timeout: 50 * time.Millisecond, cause: errOpenAIChatStreamIdleTimeout},
	}
	for _, provider := range providerStreamParityCases() {
		for _, mode := range modes {
			t.Run(provider.name+"/"+mode.name, func(t *testing.T) {
				var attempts atomic.Int32
				var httpClient *http.Client
				endpoint := "https://provider.invalid"
				if mode.name == "read error" {
					httpClient = &http.Client{Transport: openAIChatRoundTripFunc(func(*http.Request) (*http.Response, error) {
						attempts.Add(1)
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
							Body:       &openAIChatInterruptedBody{reader: strings.NewReader(provider.visible)},
						}, nil
					})}
				} else {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
						attempts.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte(provider.visible))
						w.(http.Flusher).Flush()
						switch mode.name {
						case "idle timeout":
							<-request.Context().Done()
						case "heartbeat only":
							ticker := time.NewTicker(10 * time.Millisecond)
							defer ticker.Stop()
							for {
								select {
								case <-request.Context().Done():
									return
								case <-ticker.C:
									_, _ = w.Write([]byte(": keepalive\n\n"))
									w.(http.Flusher).Flush()
								}
							}
						}
					}))
					defer server.Close()
					endpoint = server.URL
					httpClient = server.Client()
				}
				client := newProviderStreamParityClient(t, provider, endpoint, mode.timeout, httpClient)
				var visible strings.Builder
				_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
					Messages: []agentruntime.Message{{Role: "user", Content: "recover"}},
				}, func(event agentruntime.ModelStreamEvent) error {
					visible.WriteString(event.ContentDelta)
					return nil
				})
				if !IsRecoverableStreamInterruption(err) || !errors.Is(err, mode.cause) ||
					attempts.Load() != 1 || visible.String() != "visible" {
					t.Fatalf("attempts=%d visible=%q err=%v", attempts.Load(), visible.String(), err)
				}
			})
		}
	}
}

func TestIncompleteOpenAIChatToolCallHasNoEngineOrGatewaySideEffects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "incomplete-tool", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var gatewayCalls atomic.Int32
	var modelResponses atomic.Int32
	engine := agentruntime.Engine{
		Model: client,
		Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
			gatewayCalls.Add(1)
			return agentruntime.ToolResult{Value: "unexpected"}, nil
		}),
		OnEvent: func(event agentruntime.Event) {
			if event.Type == agentruntime.EventModelResponse {
				modelResponses.Add(1)
			}
		},
	}
	_, err = engine.Run(context.Background(), agentruntime.RunRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "call a tool"}},
		Tools:    []agentruntime.ToolSchema{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
	})
	if !errors.Is(err, errProviderStreamIncomplete) || gatewayCalls.Load() != 0 || modelResponses.Load() != 0 {
		t.Fatalf("gateway=%d modelResponses=%d err=%v", gatewayCalls.Load(), modelResponses.Load(), err)
	}
}

func TestReadOpenAIChatStreamReturnsMalformedArgumentsForModelRepair(t *testing.T) {
	payload := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-invalid","type":"function","function":{"name":"lookup","arguments":"{\"query\":"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 64*1024, nil)
	if err != nil || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	call := response.Message.ToolCalls[0]
	if string(call.Arguments) != `{}` || !strings.Contains(call.ProviderProtocolDiagnostic, "invalid JSON object arguments") {
		t.Fatalf("call=%#v", call)
	}
}

func TestAnthropicStreamReturnsMalformedArgumentsForModelRepair(t *testing.T) {
	tool := &anthropicStreamTool{ID: "call-invalid", Name: "lookup"}
	tool.Arguments.WriteString(`{"query":`)
	accumulator := anthropicStreamAccumulator{
		role: "assistant", stopped: true, tools: map[int]*anthropicStreamTool{0: tool},
	}
	response, _, _, _, err := accumulator.response()
	if err != nil || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	call := response.Message.ToolCalls[0]
	if string(call.Arguments) != `{}` || !strings.Contains(call.ProviderProtocolDiagnostic, "invalid JSON object input") {
		t.Fatalf("call=%#v", call)
	}
}

func TestOpenAIChatVisibleContentWithIncompleteToolCallIsNotRecoverable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"accepted\"}}]}\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"query\":"}}]}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "unsafe-tool-interruption", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var accepted strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "do not recover a malformed tool call"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		accepted.WriteString(event.ContentDelta)
		return nil
	})
	if err == nil || IsRecoverableStreamInterruption(err) || accepted.String() != "accepted" {
		t.Fatalf("accepted=%q err=%v", accepted.String(), err)
	}
}

func TestOpenAIChatAggregateSemanticBoundaryAfterAcceptedContentIsRecoverable(t *testing.T) {
	const limit = defaultMaxResponseBytes
	first := strings.Repeat("a", 600*1024)
	second := strings.Repeat("b", 600*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + first + "\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + second + "\"}}]}\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "semantic-boundary", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: limit},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var accepted strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "cross the semantic boundary"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		accepted.WriteString(event.ContentDelta)
		return nil
	})
	if !errors.Is(err, errProviderResponseTooLarge) || !IsRecoverableStreamInterruption(err) || accepted.String() != first {
		t.Fatalf("accepted_bytes=%d want=%d err=%v", accepted.Len(), len(first), err)
	}
}

func TestOpenAIChatAggregateSemanticBoundaryInTerminalEventIsNotRecoverable(t *testing.T) {
	const limit = defaultMaxResponseBytes
	first := strings.Repeat("a", 600*1024)
	terminal := strings.Repeat("b", 600*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + first + "\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"" + terminal + "\"}}]}\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "terminal-semantic-boundary", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "model", Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: limit},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var accepted strings.Builder
	_, err = client.(agentruntime.StreamingModelClient).CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "do not continue past a terminal event"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		accepted.WriteString(event.ContentDelta)
		return nil
	})
	if !errors.Is(err, errProviderResponseTooLarge) || IsRecoverableStreamInterruption(err) || accepted.String() != first {
		t.Fatalf("accepted_bytes=%d want=%d err=%v", accepted.Len(), len(first), err)
	}
}
