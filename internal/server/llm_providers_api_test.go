package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestLLMProvidersAPIRestoresTemplatesAndNativeProfileLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: root}).Handler()

	initial := requestLLMProviders(t, app, http.MethodGet, "/api/llm/providers", nil, http.StatusOK)
	if templates, ok := initial["templates"].([]any); !ok || len(templates) < 20 {
		t.Fatalf("provider templates = %#v", initial["templates"])
	}

	created := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"name": "Production DeepSeek", "provider": "deepseek",
		"baseUrl": "https://api.deepseek.com/v1", "model": "deepseek-chat",
		"apiKey": "provider-secret-must-not-leak", "temperature": 0.2, "maxTokens": 2048,
	}, http.StatusOK)
	profile := created["profile"].(map[string]any)
	profileID := profile["id"].(string)
	if profile["provider"] != "deepseek" || profile["hasApiKey"] != true || profile["temperature"] != 0.2 || profile["maxTokens"] != float64(2048) {
		t.Fatalf("created profile = %#v", profile)
	}

	listed := requestLLMProviders(t, app, http.MethodGet, "/api/llm/providers", nil, http.StatusOK)
	if listed["activeProfileId"] != profileID || len(listed["profiles"].([]any)) != 1 {
		t.Fatalf("listed profiles = %#v", listed)
	}
	listedProfile := listed["profiles"].([]any)[0].(map[string]any)
	if listedProfile["temperature"] != 0.2 || listedProfile["maxTokens"] != float64(2048) {
		t.Fatalf("listed generation controls = %#v", listedProfile)
	}

	update := map[string]any{
		"id": profileID, "name": "Production DeepSeek", "provider": "deepseek",
		"baseUrl": "https://api.deepseek.com/v1", "model": "deepseek-chat",
	}
	unchanged := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", update, http.StatusOK)
	if unchanged["profile"].(map[string]any)["maxTokens"] != float64(2048) {
		t.Fatal("omitting maxTokens must preserve an existing explicit limit")
	}
	update["maxTokens"] = nil
	cleared := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", update, http.StatusOK)
	clearedProfile := cleared["profile"].(map[string]any)
	if clearedProfile["maxTokens"] != nil || clearedProfile["temperature"] != 0.2 || clearedProfile["hasApiKey"] != true {
		t.Fatalf("explicit null must clear only the token limit, retaining temperature and credential: %#v", clearedProfile)
	}
	reloaded := requestLLMProviders(t, app, http.MethodGet, "/api/llm/providers", nil, http.StatusOK)
	if reloaded["profiles"].([]any)[0].(map[string]any)["maxTokens"] != nil {
		t.Fatal("cleared token limit was not persisted")
	}

	removed := requestLLMProviders(t, app, http.MethodDelete, "/api/llm/providers/"+profileID, nil, http.StatusOK)
	if len(removed["profiles"].([]any)) != 0 {
		t.Fatalf("profiles after delete = %#v", removed["profiles"])
	}
}

func TestLLMProviderTestUsesProviderDefaultOutputBudgetAndRequiresText(t *testing.T) {
	requestBodies := make(chan map[string]json.RawMessage, 1)
	requestErrors := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			requestErrors <- r.Method + " " + r.URL.Path
			http.Error(w, "unexpected provider request", http.StatusBadRequest)
			return
		}
		var input map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			requestErrors <- err.Error()
			http.Error(w, "invalid provider request", http.StatusBadRequest)
			return
		}
		requestBodies <- input
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"role": "assistant", "reasoning_content": "private reasoning", "content": "Provider reachable.",
				},
			}},
			"model": "reasoning-provider",
		})
	}))
	t.Cleanup(upstream.Close)

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: root, HTTPClient: upstream.Client()}).Handler()
	created := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"id": "reasoning-provider", "name": "Reasoning Provider", "provider": "openai",
		"baseUrl": upstream.URL + "/v1", "model": "reasoning-provider", "apiKey": "provider-secret-must-not-leak",
		"maxTokens": 2048,
	}, http.StatusOK)
	profileID := created["profile"].(map[string]any)["id"].(string)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"id": profileID, "name": "Reasoning Provider", "provider": "openai",
		"baseUrl": upstream.URL + "/v1", "model": "reasoning-provider", "maxTokens": nil,
	}, http.StatusOK)
	result := requestLLMProviders(t, app, http.MethodPost, "/api/llm/test", map[string]any{
		"profileId": profileID,
	}, http.StatusOK)
	projection := result["result"].(map[string]any)
	if projection["text"] != "Provider reachable." || projection["model"] != "reasoning-provider" {
		t.Fatalf("provider test result = %#v", projection)
	}
	select {
	case requestError := <-requestErrors:
		t.Fatalf("provider request = %s", requestError)
	default:
	}
	requestBody := <-requestBodies
	if _, present := requestBody["max_tokens"]; present {
		t.Fatalf("provider request unexpectedly included max_tokens: %s (profile=%#v)", requestBody["max_tokens"], created["profile"])
	}
}

func TestLLMProviderTestRejectsSuccessfulResponseWithoutFinalText(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		message      map[string]any
		wantError    string
	}{
		{name: "empty", finishReason: "stop", message: map[string]any{"role": "assistant", "content": ""}, wantError: "model provider returned an empty response"},
		{name: "whitespace", finishReason: "stop", message: map[string]any{"role": "assistant", "content": " \n\t"}, wantError: "model provider returned an empty response"},
		{name: "null", finishReason: "stop", message: map[string]any{"role": "assistant", "content": nil}, wantError: "model provider returned an empty response"},
		{name: "missing", finishReason: "stop", message: map[string]any{"role": "assistant"}, wantError: "model provider returned an empty response"},
		{name: "reasoning_only", finishReason: "stop", message: map[string]any{"role": "assistant", "reasoning_content": "private reasoning"}, wantError: "model provider returned an empty response"},
		{name: "length_after_reasoning", finishReason: "length", message: map[string]any{"role": "assistant", "reasoning_content": "private reasoning", "content": ""}, wantError: "provider response truncated: finish reason length"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"finish_reason": test.finishReason, "message": test.message}},
					"model":   "empty-provider",
				})
			}))
			t.Cleanup(upstream.Close)

			root := t.TempDir()
			store, err := workspace.Open(filepath.Join(root, "workspace.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			app := New(Options{Workspace: store, FileRoot: root, HTTPClient: upstream.Client()}).Handler()
			created := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
				"id": "empty-provider", "name": "Empty Provider", "provider": "openai",
				"baseUrl": upstream.URL + "/v1", "model": "empty-provider", "apiKey": "provider-secret-must-not-leak",
			}, http.StatusOK)
			profileID := created["profile"].(map[string]any)["id"].(string)
			failed := requestLLMProviders(t, app, http.MethodPost, "/api/llm/test", map[string]any{
				"profileId": profileID,
			}, http.StatusBadGateway)
			if failed["ok"] != false || failed["error"] != test.wantError {
				t.Fatalf("empty provider response = %#v", failed)
			}
		})
	}
}

func requestLLMProviders(t *testing.T, app http.Handler, method, path string, body any, wantStatus int) map[string]any {
	t.Helper()
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("provider-secret-must-not-leak")) && method != http.MethodPost {
			t.Fatal("test secret escaped into a non-write request")
		}
		requestBody = bytes.NewReader(raw)
	}
	request := newLoopbackTestRequest(method, path, requestBody)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("provider-secret-must-not-leak")) {
		t.Fatal("model provider response leaked the API key")
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return payload
}
