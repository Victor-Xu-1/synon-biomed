package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	secretstore "synon-go/internal/persistence/secrets"
	settingsstore "synon-go/internal/persistence/settings"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRuntimeModelClientOpenAIChatEncodesMediaContentParts(t *testing.T) {
	captured := make(chan []any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		messages := payload["messages"].([]any)
		captured <- messages[0].(map[string]any)["content"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"accepted"}}]}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "media", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "media-model", APIKey: "media-key",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{
		Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "inspect"},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{MIMEType: "image/png", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}}},
			{Type: agentruntime.ContentPartDocument, Media: &agentruntime.MediaContent{MIMEType: "text/plain", Filename: "notes.txt", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("notes")}}},
			{Type: agentruntime.ContentPartAudio, Media: &agentruntime.MediaContent{MIMEType: "audio/wav", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("RIFFxxxxWAVE")}}},
		},
	}}})
	if err != nil || response.Message.Content != "accepted" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	parts := <-captured
	if len(parts) != 4 || parts[0].(map[string]any)["type"] != "text" ||
		parts[1].(map[string]any)["type"] != "image_url" ||
		parts[2].(map[string]any)["type"] != "file" ||
		parts[3].(map[string]any)["type"] != "input_audio" {
		t.Fatalf("encoded parts = %#v", parts)
	}
}

func TestRuntimeModelClientOpenAIChatEncodesNamedToolChoice(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- payload["tool_choice"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"accepted"}}]}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "choice", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "choice-model", APIKey: "choice-key",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages:   []agentruntime.Message{{Role: "user", Content: "download"}},
		Tools:      []agentruntime.ToolSchema{{Name: "download_public_scientific_file"}},
		ToolChoice: map[string]any{"type": "tool", "name": "download_public_scientific_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "function", "function": map[string]any{"name": "download_public_scientific_file"}}
	if got := <-captured; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool_choice=%#v want=%#v", got, want)
	}
}

func TestRuntimeModelClientArkThinkingChatUsesAutoToolChoice(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"accepted"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "ark", Type: "volcengine-ark", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages:   []agentruntime.Message{{Role: "user", Content: "use ask_user"}},
		Tools:      []agentruntime.ToolSchema{{Name: "ask_user"}},
		ToolChoice: map[string]any{"type": "tool", "name": "ask_user"},
	})
	if err != nil || response.Message.Content != "accepted" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if got := (<-captured)["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice=%#v want auto", got)
	}
}

func TestRuntimeModelClientArkCanDisableReasoningForPresentationTurn(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"自然的专业说明"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "ark", Type: "volcengine-ark", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "ark-code-latest",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "说明下一步科学判断"}},
		ToolChoice:    "none",
		ReasoningMode: agentruntime.ReasoningModeDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	thinking, _ := (<-captured)["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v want disabled", thinking)
	}
}

func TestResolveRunnerModelProfileUsesActiveSavedProvider(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Provider project"}); err != nil {
		t.Fatal(err)
	}
	settings := settingsstore.New(filepath.Join(root, "settings.json"))
	if _, err := settings.Set("model.activeProviderId", "provider-a"); err != nil {
		t.Fatal(err)
	}
	secrets := secretstore.New(root)
	if _, err := secrets.Create(secretstore.Secret{ID: "provider-a-key", UserID: "user-1", Provider: "openai", Value: "saved-key"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	temperature := 0.35
	maxTokens := 32768
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID:          "provider-a",
		UserID:      "user-1",
		Name:        "Saved provider",
		Type:        "openai",
		BaseURL:     "https://providers.example.test/v1",
		Model:       "gpt-4.1-mini",
		SecretRef:   "secret://provider-a-key",
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Enabled:     &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	resolution, err := ResolveRunnerModelProfile(settings, store, secrets, ResolutionInput{ProjectID: "project-1", RequestTimeout: 45 * time.Second, MaxAttempts: 3})
	if err != nil {
		t.Fatalf("ResolveRunnerModelProfile() error = %v", err)
	}
	if !resolution.Resolved || resolution.ProviderID != "provider-a" || resolution.UserID != "user-1" {
		t.Fatalf("resolution = %#v", resolution)
	}
	profile := resolution.ModelProfile
	if profile == nil {
		t.Fatal("expected resolved model profile")
	}
	if profile.APIKey != "saved-key" {
		t.Fatalf("profile API key = %q", profile.APIKey)
	}
	if profile.Provider.Protocol != ProtocolOpenAICompatible {
		t.Fatalf("provider protocol = %q", profile.Provider.Protocol)
	}
	if profile.Provider.Endpoint != "https://providers.example.test/v1/chat/completions" {
		t.Fatalf("provider endpoint = %q", profile.Provider.Endpoint)
	}
	if profile.Request.MaxAttempts != 3 || profile.Request.Timeout != 45*time.Second {
		t.Fatalf("request profile = %#v", profile.Request)
	}
	if profile.Temperature == nil || *profile.Temperature != temperature || profile.MaxTokens == nil || *profile.MaxTokens != maxTokens {
		t.Fatalf("generation controls = %#v", profile)
	}
}

func TestBuildModelProfilePreservesMissingMaxTokensAsProviderDefault(t *testing.T) {
	profile, err := buildModelProfile(workspace.ModelProvider{
		ID:      "mimo-default",
		UserID:  "user-1",
		Name:    "MiMo",
		Type:    "custom",
		BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
		Model:   "mimo-v2.5",
		Enabled: true,
	}, nil, "user-1", ResolutionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxTokens != nil {
		t.Fatalf("provider default max tokens = %#v, want nil", profile.MaxTokens)
	}

	explicit := 8192
	profile, err = buildModelProfile(workspace.ModelProvider{
		ID:        "mimo-explicit",
		UserID:    "user-1",
		Name:      "MiMo",
		Type:      "custom",
		BaseURL:   "https://token-plan-cn.xiaomimimo.com/v1",
		Model:     "mimo-v2.5",
		MaxTokens: &explicit,
		Enabled:   true,
	}, nil, "user-1", ResolutionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxTokens == nil || *profile.MaxTokens != explicit {
		t.Fatalf("explicit max tokens = %#v", profile.MaxTokens)
	}
}

func TestRuntimeModelClientAppliesProfileGenerationControlsAndRequestOverrides(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	profileTemperature := 0.35
	profileMaxTokens := 32768
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "generation", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "generation-model", Temperature: &profileTemperature, MaxTokens: &profileMaxTokens,
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "test"}}}
	if _, err := client.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	defaulted := <-requests
	if defaulted["temperature"] != profileTemperature || defaulted["max_tokens"] != float64(profileMaxTokens) {
		t.Fatalf("profile defaults payload = %#v", defaulted)
	}
	overrideTemperature := 0.7
	request.Temperature = &overrideTemperature
	request.MaxTokens = 1024
	if _, err := client.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	overridden := <-requests
	if overridden["temperature"] != overrideTemperature || overridden["max_tokens"] != float64(1024) {
		t.Fatalf("request override payload = %#v", overridden)
	}
}

func TestOpenAIMessageRoundTripsHiddenReasoningContent(t *testing.T) {
	decoded, err := runtimeMessageFromOpenAI(openAIMessage{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "private reasoning",
		ToolCalls: []openAIToolCall{{
			ID: "call-1", Type: "function",
			Function: openAIToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ReasoningContent != "private reasoning" {
		t.Fatalf("decoded reasoning = %q", decoded.ReasoningContent)
	}
	encoded, err := openAIMessagesFromRuntime([]agentruntime.Message{decoded})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 1 || encoded[0].ReasoningContent != "private reasoning" {
		t.Fatalf("encoded messages = %#v", encoded)
	}
}

func TestRuntimeModelClientOpenAIChatRejectsTruncatedNonStreamingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "truncated", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "gpt-test",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "answer"}},
	})
	if !errors.Is(err, errProviderResponseTruncated) || !IsContinuationSafeResponseTruncation(err) {
		t.Fatalf("Complete() error = %v", err)
	}
}

func TestResolveRunnerModelProfileRejectsUnavailableActiveProvider(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Provider project"}); err != nil {
		t.Fatal(err)
	}
	settings := settingsstore.New(filepath.Join(root, "settings.json"))
	disabled := false
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "disabled-provider", UserID: "user-1", Name: "Disabled", Type: "openai",
		BaseURL: "https://providers.example.test/v1", Model: "gpt-test", Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := settings.Set("model.activeProviderId", "disabled-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRunnerModelProfile(settings, store, secretstore.New(root), ResolutionInput{ProjectID: "project-1"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled active provider error = %v", err)
	}
	if _, err := settings.Set("model.activeProviderId", "missing-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRunnerModelProfile(settings, store, secretstore.New(root), ResolutionInput{ProjectID: "project-1"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing active provider error = %v", err)
	}
}

func TestResolveRunnerModelProfileRejectsCrossUserSecret(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Provider project"}); err != nil {
		t.Fatal(err)
	}
	settings := settingsstore.New(filepath.Join(root, "settings.json"))
	if _, err := settings.Set("model.activeProviderId", "provider-a"); err != nil {
		t.Fatal(err)
	}
	secrets := secretstore.New(root)
	if _, err := secrets.Create(secretstore.Secret{ID: "provider-key", UserID: "user-2", Provider: "openai", Value: "other-user-key"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-a", UserID: "user-1", Name: "Saved provider", Type: "openai",
		BaseURL: "https://providers.example.test/v1", Model: "gpt-test", SecretRef: "secret://provider-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveRunnerModelProfile(settings, store, secrets, ResolutionInput{ProjectID: "project-1"}); err == nil || !strings.Contains(err.Error(), "secret provider-key not found") {
		t.Fatalf("cross-user secret resolution error = %v", err)
	}
}

func TestResolveRunnerModelProfileRejectsMissingProjectOwnership(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := settingsstore.New(filepath.Join(root, "settings.json"))
	if _, err := settings.Set("model.activeProviderId", "provider-a"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-a", UserID: secretstore.DefaultUserID, Name: "Default provider", Type: "openai",
		BaseURL: "https://providers.example.test/v1", Model: "gpt-test", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveRunnerModelProfile(settings, store, secretstore.New(root), ResolutionInput{ProjectID: "missing-project"}); err == nil || !strings.Contains(err.Error(), "project missing-project not found") {
		t.Fatalf("missing project ownership error = %v", err)
	}
}

func TestResolveRunnerModelProfileRequiresSecretStoreForSecretRef(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Provider project"}); err != nil {
		t.Fatal(err)
	}
	settings := settingsstore.New(filepath.Join(root, "settings.json"))
	if _, err := settings.Set("model.activeProviderId", "provider-a"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-a", UserID: "user-1", Name: "Saved provider", Type: "openai",
		BaseURL: "https://providers.example.test/v1", Model: "gpt-test",
		SecretRef: "secret://provider-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveRunnerModelProfile(settings, store, nil, ResolutionInput{ProjectID: "project-1"}); err == nil || !strings.Contains(err.Error(), "secret store is required") {
		t.Fatalf("missing secret store error = %v", err)
	}
}

func TestResolveRunnerModelProfileRequiresExplicitActiveProvider(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Provider project"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-a", UserID: "user-1", Name: "Saved provider", Type: "openai",
		BaseURL: "https://providers.example.test/v1", Model: "gpt-test", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	resolution, err := ResolveRunnerModelProfile(settingsstore.New(filepath.Join(root, "settings.json")), store, secretstore.New(root), ResolutionInput{ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Resolved || resolution.ModelProfile != nil {
		t.Fatalf("resolution without active provider = %#v", resolution)
	}
}

func TestBuildModelProfileRejectsUnsupportedProviderProtocols(t *testing.T) {
	tests := []struct {
		name     string
		provider workspace.ModelProvider
		want     string
	}{
		{
			name: "bedrock",
			provider: workspace.ModelProvider{
				ID: "bedrock-a", UserID: "user-1", Type: "bedrock",
				BaseURL: "https://bedrock-runtime.example.test/v1", Model: "claude-test", Enabled: true,
			},
			want: "provider protocol \"bedrock\" is not implemented",
		},
		{
			name: "credential-in-url",
			provider: workspace.ModelProvider{
				ID: "openai-query-secret", UserID: "user-1", Type: "openai",
				BaseURL: "https://api.openai.com/v1?api_key=do-not-store-here", Model: "gpt-test", Enabled: true,
			},
			want: "must not contain credentials",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildModelProfile(test.provider, nil, "user-1", ResolutionInput{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildModelProfile() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProviderEndpointNormalizationPreservesNonSecretQueryAfterPath(t *testing.T) {
	openAIEndpoint, err := normalizeOpenAIEndpoint("https://providers.example.test/v1?tenant=alpha")
	if err != nil {
		t.Fatal(err)
	}
	if openAIEndpoint != "https://providers.example.test/v1/chat/completions?tenant=alpha" {
		t.Fatalf("OpenAI endpoint = %q", openAIEndpoint)
	}
	geminiEndpoint, err := normalizeGeminiEndpoint("https://generativelanguage.googleapis.com/v1beta?tenant=alpha", "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	if geminiEndpoint != "https://generativelanguage.googleapis.com/v1beta/models/gemini-test:generateContent?tenant=alpha" {
		t.Fatalf("Gemini endpoint = %q", geminiEndpoint)
	}
}

func TestRuntimeModelClientOpenAICompatibleRetriesAndAuditsUsage(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer saved-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if strings.TrimSpace(r.Header.Get("X-Request-ID")) == "" {
			t.Fatal("missing outbound X-Request-ID")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "gpt-4.1-mini" {
			t.Fatalf("model = %#v", payload["model"])
		}
		if attempts == 1 {
			w.Header().Set("x-request-id", "req-retry-1")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary failure"}`))
			return
		}
		w.Header().Set("x-request-id", "req-ok-2")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-2",
			"choices":[{"message":{"role":"assistant","content":"saved provider won"}}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":4}}
		}`))
	}))
	defer server.Close()

	audits := make([]AuditRecord, 0, 2)
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "provider-a", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL + "/v1/chat/completions"},
		Model:    "gpt-4.1-mini",
		APIKey:   "saved-key",
		Request:  RequestProfile{Timeout: time.Minute, MaxAttempts: 2},
	}, server.Client(), func(record AuditRecord) {
		audits = append(audits, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.Message.Content != "saved provider won" {
		t.Fatalf("response = %#v", response)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if len(audits) != 2 {
		t.Fatalf("audits = %#v", audits)
	}
	if audits[0].HTTPStatus != http.StatusInternalServerError || !strings.Contains(audits[0].Error, "temporary failure") || audits[0].RequestID != "req-retry-1" {
		t.Fatalf("first audit = %#v", audits[0])
	}
	if audits[1].HTTPStatus != http.StatusOK || audits[1].RequestID != "chatcmpl-2" || audits[1].PromptTokens != 11 || audits[1].CompletionTokens != 7 || audits[1].CacheReadTokens != 4 || audits[1].TotalTokens != 18 {
		t.Fatalf("second audit = %#v", audits[1])
	}
}

func TestRuntimeModelClientRedactsAPIKeyFromProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer saved-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"provider echoed saved-key"}`))
	}))
	defer server.Close()

	var audit AuditRecord
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "provider-a", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "gpt-test",
		APIKey:   "saved-key",
		Request:  RequestProfile{Timeout: time.Minute, MaxAttempts: 1, MaxResponseBytes: 4096},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "hello"}}})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if strings.Contains(err.Error(), "saved-key") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("returned error leaked key: %v", err)
	}
	if strings.Contains(audit.Error, "saved-key") || !strings.Contains(audit.Error, "[REDACTED]") {
		t.Fatalf("audit error leaked key: %#v", audit)
	}
}

func TestRuntimeModelClientBoundsProviderResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + strings.Repeat("x", 256) + `"}}]}`))
	}))
	defer server.Close()

	var audit AuditRecord
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "provider-a", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "gpt-test",
		Request:  RequestProfile{Timeout: time.Minute, MaxAttempts: 1, MaxResponseBytes: 64},
	}, server.Client(), func(record AuditRecord) { audit = record })
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "response exceeded 64 bytes") {
		t.Fatalf("oversized response error = %v", err)
	}
	if audit.HTTPStatus != http.StatusOK || !strings.Contains(audit.Error, "response exceeded 64 bytes") {
		t.Fatalf("oversized response audit = %#v", audit)
	}
}

func TestRuntimeModelClientGeminiSupportsFunctionCallsAndResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/models/gemini-2.5-pro:generateContent") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "gem-key" {
			t.Fatalf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		if r.URL.Query().Get("key") != "" {
			t.Fatalf("Gemini key leaked into query: %q", r.URL.RawQuery)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		contents, ok := payload["contents"].([]any)
		if !ok || len(contents) < 3 {
			t.Fatalf("contents = %#v", payload["contents"])
		}
		tools := payload["tools"].([]any)
		toolDecls := tools[0].(map[string]any)["functionDeclarations"].([]any)
		if len(toolDecls) != 1 || toolDecls[0].(map[string]any)["name"] != "task_create" {
			t.Fatalf("tool declarations = %#v", tools)
		}
		last := contents[len(contents)-1].(map[string]any)
		parts := last["parts"].([]any)
		functionResponse := parts[0].(map[string]any)["functionResponse"].(map[string]any)
		if functionResponse["name"] != "task_create" {
			t.Fatalf("function response = %#v", functionResponse)
		}
		w.Header().Set("x-request-id", "gem-req-1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"gem-req-1",
			"candidates":[{"content":{"parts":[
				{"text":"private chain of thought","thought":true},
				{"text":"gemini text"},
				{"functionCall":{"name":"task_create","args":{"title":"from gemini"}}}
			]}}],
			"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":5,"totalTokenCount":18}
		}`))
	}))
	defer server.Close()

	audits := make([]AuditRecord, 0, 1)
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "provider-g", Type: "gemini", Protocol: ProtocolGemini, Endpoint: server.URL + "/v1beta/models/gemini-2.5-pro:generateContent"},
		Model:    "gemini-2.5-pro",
		APIKey:   "gem-key",
		Request:  RequestProfile{Timeout: time.Minute, MaxAttempts: 1},
	}, server.Client(), func(record AuditRecord) {
		audits = append(audits, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "delegate this"},
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "task_create", Arguments: json.RawMessage(`{"title":"draft"}`)}}},
			{Role: "tool", ToolCallID: "call-1", Content: `{"ok":true,"taskId":"task-1"}`},
		},
		Tools: []agentruntime.ToolSchema{{Name: "task_create", Description: "Create task", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.Message.Content != "gemini text" || len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != "task_create" {
		t.Fatalf("response = %#v", response)
	}
	if len(audits) != 1 || audits[0].RequestID != "gem-req-1" || audits[0].PromptTokens != 13 || audits[0].CompletionTokens != 5 || audits[0].TotalTokens != 18 {
		t.Fatalf("audits = %#v", audits)
	}
	if strings.Contains(audits[0].Endpoint, "gem-key") {
		t.Fatalf("Gemini key leaked into audit endpoint: %#v", audits[0])
	}
}

func TestRuntimeModelClientGeminiEncodesInlineMedia(t *testing.T) {
	captured := make(chan []any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		contents := payload["contents"].([]any)
		captured <- contents[0].(map[string]any)["parts"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"accepted"}]}}]}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "gemini-media", Type: "gemini", Protocol: ProtocolGemini, Endpoint: server.URL + "/models/gemini:generateContent"},
		Model:    "gemini", APIKey: "gem-key",
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{
		Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "inspect"},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{MIMEType: "image/png", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}}},
			{Type: agentruntime.ContentPartAudio, Media: &agentruntime.MediaContent{MIMEType: "audio/wav", Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("RIFFxxxxWAVE")}}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	parts := <-captured
	if len(parts) != 3 || parts[0].(map[string]any)["text"] != "inspect" {
		t.Fatalf("parts = %#v", parts)
	}
	image := parts[1].(map[string]any)["inlineData"].(map[string]any)
	audio := parts[2].(map[string]any)["inlineData"].(map[string]any)
	if image["mimeType"] != "image/png" || image["data"] == "" || audio["mimeType"] != "audio/wav" || audio["data"] == "" {
		t.Fatalf("inline media = %#v", parts)
	}
}

func TestOpenAICompatibleInvalidArgumentsRemainModelRepairable(t *testing.T) {
	calls, err := runtimeToolCallsFromOpenAI([]openAIToolCall{{
		ID:       "call-invalid",
		Function: openAIToolCallFunction{Name: "lookup", Arguments: `{"query":`},
	}})
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%#v err=%v", calls, err)
	}
	if string(calls[0].Arguments) != `{}` ||
		!strings.Contains(calls[0].ProviderProtocolDiagnostic, "invalid JSON object arguments") {
		t.Fatalf("call=%#v", calls[0])
	}
}
