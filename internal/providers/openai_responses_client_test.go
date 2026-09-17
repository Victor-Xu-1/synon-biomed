package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestBuildModelProfileNormalizesOpenAIResponsesEndpoint(t *testing.T) {
	for _, test := range []struct {
		providerType string
		baseURL      string
	}{
		{providerType: "openai-responses", baseURL: "https://api.openai.com/v1"},
		{providerType: "openai", baseURL: "https://api.openai.com/v1/responses"},
	} {
		profile, err := buildModelProfile(workspace.ModelProvider{
			ID: "provider-responses", UserID: "user-1", Type: test.providerType,
			BaseURL: test.baseURL, Model: "gpt-5.4", Enabled: true,
		}, nil, "user-1", ResolutionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if profile.Provider.Protocol != "openai-responses" {
			t.Fatalf("provider protocol = %q", profile.Provider.Protocol)
		}
		if profile.Provider.Endpoint != "https://api.openai.com/v1/responses" {
			t.Fatalf("provider endpoint = %q", profile.Provider.Endpoint)
		}
	}
}

func TestOpenAIResponsesInvalidArgumentsRemainModelRepairable(t *testing.T) {
	body := []byte(`{
		"id":"response-invalid",
		"output":[{"type":"function_call","call_id":"call-invalid","name":"lookup","arguments":"{not-json"}]
	}`)
	response, _, _, err := (&runtimeModelClient{}).openAIResponsesResponseDecoder(body, nil)
	if err != nil || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	call := response.Message.ToolCalls[0]
	if string(call.Arguments) != `{}` || !strings.Contains(call.ProviderProtocolDiagnostic, "invalid JSON object arguments") {
		t.Fatalf("call=%#v", call)
	}
}

func TestRuntimeModelClientOpenAIResponsesEncodesInlineMediaParts(t *testing.T) {
	captured := make(chan []map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- body.Input
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_media","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"accepted"}]}]}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "responses-media", Type: "openai-responses", Protocol: ProtocolOpenAIResponses, Endpoint: server.URL},
		Model:    "gpt-media", APIKey: "media-key",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{
		Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "inspect"},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{MIMEType: "image/png", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}}},
			{Type: agentruntime.ContentPartDocument, Media: &agentruntime.MediaContent{MIMEType: "text/plain", Filename: "notes.txt", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("notes")}}},
			{Type: agentruntime.ContentPartAudio, Media: &agentruntime.MediaContent{MIMEType: "audio/wav", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("RIFFxxxxWAVE")}}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	input := <-captured
	if len(input) != 1 {
		t.Fatalf("input = %#v", input)
	}
	content, ok := input[0]["content"].([]any)
	if !ok || len(content) != 4 {
		t.Fatalf("content = %#v", input[0]["content"])
	}
	image := content[1].(map[string]any)
	file := content[2].(map[string]any)
	audio := content[3].(map[string]any)
	if image["type"] != "input_image" || !strings.HasPrefix(image["image_url"].(string), "data:image/png;base64,") ||
		file["type"] != "input_file" || file["filename"] != "notes.txt" || !strings.HasPrefix(file["file_data"].(string), "data:text/plain;base64,") ||
		audio["type"] != "input_audio" || audio["input_audio"].(map[string]any)["format"] != "wav" {
		t.Fatalf("encoded media = %#v", content)
	}
}

func TestRuntimeModelClientOpenAIResponsesSupportsFunctionCallsAndOutputs(t *testing.T) {
	type inputItem struct {
		Type      string `json:"type"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Output    string `json:"output"`
	}
	type tool struct {
		Type        string         `json:"type"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type capture struct {
		Method, Path, Authorization, RequestID, Model string
		Store                                         *bool
		Input                                         []inputItem
		Tools                                         []tool
		Metadata                                      map[string]any
	}
	captured := make(chan capture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string         `json:"model"`
			Store    *bool          `json:"store"`
			Input    []inputItem    `json:"input"`
			Tools    []tool         `json:"tools"`
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capture{
			Method: r.Method, Path: r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			RequestID:     r.Header.Get("X-Request-ID"),
			Model:         body.Model, Store: body.Store, Input: body.Input,
			Tools: body.Tools, Metadata: body.Metadata,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_responses_1")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"output":[
				{"type":"message","role":"assistant","content":[
					{"type":"output_text","text":"Found it."},
					{"type":"output_text","text":"Second result."}
				]},
				{"type":"function_call","call_id":"call_next","name":"persist","arguments":"{\"value\":2}"}
			],
			"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":3},"output_tokens":7,"total_tokens":27}
		}`))
	}))
	defer server.Close()

	audits := make(chan AuditRecord, 1)
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "provider-responses", Type: "openai", Protocol: "openai-responses",
			Endpoint: server.URL + "/v1/responses",
		},
		Model: "gpt-5.4", APIKey: "responses-secret",
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}, server.Client(), func(record AuditRecord) { audits <- record })
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "Use tools carefully."},
			{Role: "user", Content: "Find abc."},
			{Role: "assistant", Content: "Working.", ToolCalls: []agentruntime.ToolCall{{
				ID: "call_lookup", Name: "lookup", Arguments: json.RawMessage(`{"query":"abc"}`),
			}}},
			{Role: "tool", ToolCallID: "call_lookup", Content: `{"items":[1]}`},
		},
		Tools: []agentruntime.ToolSchema{{
			Name: "lookup", Description: "Lookup an item",
			Parameters: map[string]any{"type": "object"},
		}},
		Metadata: map[string]any{"session": "session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := <-captured
	if request.Method != http.MethodPost || request.Path != "/v1/responses" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Authorization != "Bearer responses-secret" || !strings.HasPrefix(request.RequestID, "synon-") {
		t.Fatalf("request headers = %#v", request)
	}
	if request.Model != "gpt-5.4" || request.Store == nil || *request.Store {
		t.Fatalf("model/store = %q/%v", request.Model, request.Store)
	}
	if len(request.Input) != 5 ||
		request.Input[0].Type != "message" || request.Input[0].Role != "system" ||
		request.Input[3].Type != "function_call" || request.Input[3].CallID != "call_lookup" ||
		request.Input[4].Type != "function_call_output" || request.Input[4].Output != `{"items":[1]}` {
		t.Fatalf("input = %#v", request.Input)
	}
	if len(request.Tools) != 1 || request.Tools[0].Type != "function" ||
		request.Tools[0].Name != "lookup" || request.Tools[0].Parameters["type"] != "object" {
		t.Fatalf("tools = %#v", request.Tools)
	}
	if request.Metadata["session"] != "session-1" {
		t.Fatalf("metadata = %#v", request.Metadata)
	}
	if response.Message.Role != "assistant" ||
		response.Message.Content != "Found it.\n\nSecond result." ||
		len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].ID != "call_next" ||
		response.Message.ToolCalls[0].Name != "persist" ||
		string(response.Message.ToolCalls[0].Arguments) != `{"value":2}` {
		t.Fatalf("response = %#v", response)
	}
	audit := <-audits
	if audit.Protocol != "openai-responses" || audit.RequestID != "resp_1" ||
		audit.PromptTokens != 20 || audit.CompletionTokens != 7 ||
		audit.CacheReadTokens != 3 || audit.TotalTokens != 27 || audit.Error != "" {
		t.Fatalf("audit = %#v", audit)
	}
}
