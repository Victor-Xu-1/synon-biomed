package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestBuildModelProfileNormalizesAzureOpenAIEndpoints(t *testing.T) {
	tests := []struct {
		name, providerType, baseURL, protocol, endpoint string
	}{
		{
			name: "chat-v1", providerType: "azure-openai",
			baseURL:  "https://resource.openai.azure.com/openai/v1",
			protocol: "azure-openai",
			endpoint: "https://resource.openai.azure.com/openai/v1/chat/completions",
		},
		{
			name: "responses-v1", providerType: "azure-openai-responses",
			baseURL:  "https://resource.openai.azure.com/openai/v1",
			protocol: "azure-openai-responses",
			endpoint: "https://resource.openai.azure.com/openai/v1/responses",
		},
		{
			name: "legacy-deployment", providerType: "azure-openai",
			baseURL:  "https://resource.openai.azure.com/openai/deployments/deployment-a?api-version=2024-10-21",
			protocol: "azure-openai",
			endpoint: "https://resource.openai.azure.com/openai/deployments/deployment-a/chat/completions?api-version=2024-10-21",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := buildModelProfile(workspace.ModelProvider{
				ID: "azure-provider", UserID: "user-1", Type: test.providerType,
				BaseURL: test.baseURL, Model: "deployment-a", Enabled: true,
			}, nil, "user-1", ResolutionInput{})
			if err != nil {
				t.Fatal(err)
			}
			if profile.Provider.Protocol != test.protocol || profile.Provider.Endpoint != test.endpoint {
				t.Fatalf("provider = %#v", profile.Provider)
			}
		})
	}
}

func TestRuntimeModelClientAzureOpenAIUsesAPIKeyForChatAndResponses(t *testing.T) {
	type capture struct {
		Path, APIKey, Authorization, Model string
	}
	captured := make(chan capture, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capture{
			Path: r.URL.Path, APIKey: r.Header.Get("api-key"),
			Authorization: r.Header.Get("Authorization"), Model: body.Model,
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chat_azure","choices":[{"message":{"role":"assistant","content":"azure chat"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
		case "/openai/v1/responses":
			_, _ = w.Write([]byte(`{"id":"resp_azure","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"azure responses"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		protocol, path, want string
	}{
		{protocol: "azure-openai", path: "/openai/v1/chat/completions", want: "azure chat"},
		{protocol: "azure-openai-responses", path: "/openai/v1/responses", want: "azure responses"},
	}
	for _, test := range tests {
		client, err := NewRuntimeModelClient(ModelProfile{
			Provider: ProviderProfile{
				ID: "azure-provider", Type: "azure-openai",
				Protocol: test.protocol, Endpoint: server.URL + test.path,
			},
			Model: "deployment-a", APIKey: "azure-key",
			Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1},
		}, server.Client(), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "hello"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.Message.Content != test.want {
			t.Fatalf("response = %#v", response)
		}
		request := <-captured
		if request.Path != test.path || request.APIKey != "azure-key" ||
			request.Authorization != "" || request.Model != "deployment-a" {
			t.Fatalf("request = %#v", request)
		}
	}
}

func TestRuntimeModelClientAzureOpenAIEntraUsesBearerToken(t *testing.T) {
	captured := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_entra","choices":[{"message":{"role":"assistant","content":"entra"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "azure-entra", Type: "azure-openai-entra", Protocol: "azure-openai",
			Endpoint: server.URL + "/openai/v1/chat/completions",
		},
		Model: "deployment-a", APIKey: "entra-token",
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	headers := <-captured
	if headers.Get("Authorization") != "Bearer entra-token" || headers.Get("api-key") != "" {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestRuntimeModelClientAzureOpenAIChatStreamingUsesRegisteredAdapter(t *testing.T) {
	captured := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-azure-stream\",\"choices\":[{\"delta\":{\"content\":\"azure stream\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{
			ID: "azure-provider", Type: "azure-openai", Protocol: ProtocolAzureOpenAI,
			Endpoint: server.URL + "/openai/v1/chat/completions",
		},
		Model: "deployment-a", APIKey: "azure-key",
		Request: RequestProfile{Timeout: 2 * time.Second, MaxAttempts: 1},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := client.(agentruntime.StreamingModelClient)
	if !ok {
		t.Fatalf("client type = %T, want agentruntime.StreamingModelClient", client)
	}
	response, err := streaming.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Content != "azure stream" {
		t.Fatalf("response = %#v", response)
	}
	headers := <-captured
	if headers.Get("api-key") != "azure-key" || headers.Get("Authorization") != "" {
		t.Fatalf("headers = %#v", headers)
	}
}
