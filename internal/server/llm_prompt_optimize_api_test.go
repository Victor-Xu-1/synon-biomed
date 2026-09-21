package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestLLMPromptOptimizeRewritesDraftThroughActiveProvider(t *testing.T) {
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
				"message":       map[string]any{"role": "assistant", "content": "优化后的提示词"},
			}},
			"model": "optimize-model",
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
		"name": "Optimize Provider", "provider": "openai",
		"baseUrl": upstream.URL + "/v1", "model": "optimize-model", "apiKey": "provider-secret-must-not-leak",
	}, http.StatusOK)
	profileID := created["profile"].(map[string]any)["id"].(string)

	result := requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "  帮我写一封请假条  ",
	}, http.StatusOK)
	projection := result["result"].(map[string]any)
	if projection["text"] != "优化后的提示词" || projection["model"] != "optimize-model" {
		t.Fatalf("optimize result = %#v", projection)
	}
	select {
	case requestError := <-requestErrors:
		t.Fatalf("provider request = %s", requestError)
	default:
	}
	requestBody := <-requestBodies
	var messages []map[string]any
	if err := json.Unmarshal(requestBody["messages"], &messages); err != nil {
		t.Fatalf("provider messages = %s", requestBody["messages"])
	}
	if len(messages) != 2 || messages[0]["role"] != "system" || messages[1]["content"] != "帮我写一封请假条" {
		t.Fatalf("provider messages = %#v", messages)
	}

	// An explicit profile id must override the active provider selection.
	override := requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "second draft", "profileId": profileID,
	}, http.StatusOK)
	if override["result"].(map[string]any)["text"] != "优化后的提示词" {
		t.Fatalf("explicit profile result = %#v", override["result"])
	}
}

func TestLLMPromptOptimizeRejectsInvalidRequests(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: root}).Handler()

	requestLLMProviders(t, app, http.MethodGet, "/api/llm/optimize-prompt", nil, http.StatusMethodNotAllowed)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": "   "}, http.StatusBadRequest)
	// No enabled provider configured yet.
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": "草稿"}, http.StatusNotFound)

	long := make([]byte, 8001)
	for index := range long {
		long[index] = 'a'
	}
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": string(long)}, http.StatusBadRequest)
}
