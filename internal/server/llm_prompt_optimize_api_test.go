package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
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
	conversationID := createPromptOptimizeConversation(t, store, "local", "prompt-active")

	created := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"name": "Optimize Provider", "provider": "openai",
		"baseUrl": upstream.URL + "/v1", "model": "optimize-model", "apiKey": "provider-secret-must-not-leak",
	}, http.StatusOK)
	profileID := created["profile"].(map[string]any)["id"].(string)

	result := requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "  帮我写一封请假条  ", "conversationId": conversationID,
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
		"text": "second draft", "profileId": profileID, "conversationId": conversationID,
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
	conversationID := createPromptOptimizeConversation(t, store, "local", "prompt-invalid")

	requestLLMProviders(t, app, http.MethodGet, "/api/llm/optimize-prompt", nil, http.StatusMethodNotAllowed)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": "   "}, http.StatusBadRequest)
	// No enabled provider configured yet.
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": "草稿"}, http.StatusBadRequest)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "草稿", "conversationId": conversationID,
	}, http.StatusNotFound)

	long := make([]byte, 8001)
	for index := range long {
		long[index] = 'a'
	}
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{"text": string(long)}, http.StatusBadRequest)
}

func TestLLMPromptOptimizeUsesOwnedConversationModelInsteadOfGlobalProvider(t *testing.T) {
	var callsA, callsB atomic.Int32
	newUpstream := func(text string, calls *atomic.Int32) *httptest.Server {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
				http.Error(w, "unexpected provider request", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": text},
				}},
				"model": "selected-model",
			})
		}))
		t.Cleanup(upstream.Close)
		return upstream
	}
	upstreamA := newUpstream("from global A", &callsA)
	upstreamB := newUpstream("from conversation B", &callsB)

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{Workspace: store, FileRoot: root, HTTPClient: upstreamA.Client()})
	app := server.Handler()
	conversationID := createPromptOptimizeConversation(t, store, "local", "prompt-selected")
	foreignConversationID := createPromptOptimizeConversation(t, store, "another-user", "prompt-foreign")

	createdA := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"name": "Global A", "provider": "openai", "baseUrl": upstreamA.URL + "/v1",
		"model": "model-a", "apiKey": "provider-secret-must-not-leak",
	}, http.StatusOK)
	createdB := requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"name": "Conversation B", "provider": "openai", "baseUrl": upstreamB.URL + "/v1",
		"model": "model-b", "apiKey": "provider-secret-must-not-leak",
	}, http.StatusOK)
	providerA := createdA["profile"].(map[string]any)["id"].(string)
	providerB := createdB["profile"].(map[string]any)["id"].(string)
	if _, err := server.settingsStore.Set(webConversationActiveProviderSetting, providerA); err != nil {
		t.Fatal(err)
	}
	setPromptOptimizeSelection(t, store, conversationID, "model-b")

	result := requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": conversationID,
	}, http.StatusOK)
	if result["result"].(map[string]any)["text"] != "from conversation B" || callsA.Load() != 0 || callsB.Load() != 1 {
		t.Fatalf("selected result=%#v upstream calls A=%d B=%d", result, callsA.Load(), callsB.Load())
	}

	// The same model name can exist on multiple providers. A provider-qualified
	// conversation selection must still choose B, not the global A.
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/providers", map[string]any{
		"id": providerB, "name": "Conversation B", "provider": "openai", "baseUrl": upstreamB.URL + "/v1",
		"model": "model-a", "copyApiKeyFrom": providerB,
	}, http.StatusOK)
	if _, err := server.settingsStore.Set(webConversationActiveProviderSetting, providerA); err != nil {
		t.Fatal(err)
	}
	setPromptOptimizeSelection(t, store, conversationID, "provider:"+providerB)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": conversationID,
	}, http.StatusOK)
	if callsA.Load() != 0 || callsB.Load() != 2 {
		t.Fatalf("provider-qualified selection called A=%d B=%d", callsA.Load(), callsB.Load())
	}

	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": foreignConversationID,
	}, http.StatusNotFound)
	setPromptOptimizeSelection(t, store, conversationID, "provider:missing-provider")
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": conversationID,
	}, http.StatusBadRequest)
	foreignProvider, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "foreign-provider", UserID: "another-user", Name: "Foreign", Type: "openai",
		BaseURL: upstreamB.URL + "/v1", Model: "foreign-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": conversationID, "profileId": foreignProvider.ID,
	}, http.StatusBadRequest)
	disabled := false
	if _, err := store.UpsertModelProvider(workspace.ModelProviderInput{
		ID: providerB, UserID: "local", Name: "Conversation B", Type: "openai",
		BaseURL: upstreamB.URL + "/v1", Model: "model-a", Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	setPromptOptimizeSelection(t, store, conversationID, "provider:"+providerB)
	requestLLMProviders(t, app, http.MethodPost, "/api/llm/optimize-prompt", map[string]any{
		"text": "draft", "conversationId": conversationID,
	}, http.StatusBadRequest)
	if callsA.Load() != 0 || callsB.Load() != 2 {
		t.Fatalf("invalid, disabled, or foreign selection called A=%d B=%d", callsA.Load(), callsB.Load())
	}
}

func setPromptOptimizeSelection(t *testing.T, store *workspace.Store, conversationID, selection string) {
	t.Helper()
	if _, err := store.SetFrameRuntimeMetadata(conversationID, workspace.FrameRuntimeMetadata{
		FrameID: conversationID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"conversation_overrides": map[string]any{"model": selection},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func createPromptOptimizeConversation(t *testing.T, store *workspace.Store, owner, suffix string) string {
	t.Helper()
	project, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "project-" + suffix, UserID: owner, Name: "Prompt optimization project",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "conversation-" + suffix, ProjectID: project.ID,
		AgentName: "GENERAL", Status: "processing", ConversationType: "agent", Name: "Prompt draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	return frame.ID
}
