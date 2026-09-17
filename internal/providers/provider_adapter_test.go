package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestDefaultProviderAdapterWiring(t *testing.T) {
	tests := []struct {
		name, protocol, providerType, baseURL, model, endpoint string
		capabilities                                           []string
		headers                                                http.Header
	}{
		{
			name: "OpenAI chat", protocol: ProtocolOpenAICompatible, providerType: "openai",
			baseURL: "https://api.openai.com/v1", endpoint: "https://api.openai.com/v1/chat/completions",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "openai-compatible"},
			headers:      http.Header{"Authorization": []string{"Bearer test-key"}},
		},
		{
			name: "OpenAI responses", protocol: ProtocolOpenAIResponses, providerType: "openai-responses",
			baseURL: "https://api.openai.com/v1", endpoint: "https://api.openai.com/v1/responses",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "openai-responses"},
			headers:      http.Header{"Authorization": []string{"Bearer test-key"}},
		},
		{
			name: "Azure OpenAI chat", protocol: ProtocolAzureOpenAI, providerType: "azure-openai",
			baseURL: "https://resource.openai.azure.com/openai/v1", endpoint: "https://resource.openai.azure.com/openai/v1/chat/completions",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "azure-openai"},
			headers:      http.Header{"Api-Key": []string{"test-key"}},
		},
		{
			name: "Azure OpenAI responses", protocol: ProtocolAzureOpenAIResponses, providerType: "azure-openai-responses",
			baseURL: "https://resource.openai.azure.com/openai/v1", endpoint: "https://resource.openai.azure.com/openai/v1/responses",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "azure-openai", "openai-responses"},
			headers:      http.Header{"Api-Key": []string{"test-key"}},
		},
		{
			name: "Gemini", protocol: ProtocolGemini, providerType: "gemini",
			baseURL: "https://generativelanguage.googleapis.com", model: "gemini-2.5-flash",
			endpoint:     "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "google-gemini"},
			headers:      http.Header{"X-Goog-Api-Key": []string{"test-key"}},
		},
		{
			name: "Anthropic", protocol: ProtocolAnthropic, providerType: "anthropic",
			baseURL: "https://api.anthropic.com", endpoint: "https://api.anthropic.com/v1/messages",
			capabilities: []string{"chat", "tool-calls", "usage", "request-id", "anthropic-messages"},
			headers: http.Header{
				"X-Api-Key":         []string{"test-key"},
				"Anthropic-Version": []string{anthropicAPIVersion},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := defaultProviderAdapters.resolve(test.protocol)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := adapter.normalizeEndpoint(test.baseURL, test.model)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint != test.endpoint {
				t.Fatalf("endpoint = %q, want %q", endpoint, test.endpoint)
			}
			if capabilities := adapter.capabilities(); !reflect.DeepEqual(capabilities, test.capabilities) {
				t.Fatalf("capabilities = %#v, want %#v", capabilities, test.capabilities)
			}
			request := httptest.NewRequest(http.MethodPost, endpoint, nil)
			adapter.applyAuthHeaders(request, ModelProfile{
				Provider: ProviderProfile{Type: test.providerType}, APIKey: "test-key",
			})
			if !reflect.DeepEqual(request.Header, test.headers) {
				t.Fatalf("headers = %#v, want %#v", request.Header, test.headers)
			}
		})
	}
}

func TestUnknownProviderProtocolFailsEndpointAndCapabilityResolution(t *testing.T) {
	if _, err := normalizeProviderEndpoint("unknown-wire-format", "https://example.invalid", "model"); err == nil || !strings.Contains(err.Error(), `provider protocol "unknown-wire-format" is not registered`) {
		t.Fatalf("normalizeProviderEndpoint() error = %v", err)
	}
	if _, err := providerCapabilities("unknown-wire-format"); err == nil || !strings.Contains(err.Error(), `provider protocol "unknown-wire-format" is not registered`) {
		t.Fatalf("providerCapabilities() error = %v", err)
	}
}

func TestBuildModelProfileUsesGeminiAdapterEndpoint(t *testing.T) {
	tests := []struct {
		name, baseURL, endpoint string
	}{
		{"official root", "https://generativelanguage.googleapis.com", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"},
		{"explicit version", "https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"},
		{"complete endpoint", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := BuildModelProfile(workspace.ModelProvider{
				ID: "gemini-provider", UserID: "user-1", Type: "gemini",
				BaseURL: test.baseURL, Model: "gemini-2.5-flash", Enabled: true,
			}, nil, "user-1", ResolutionInput{})
			if err != nil {
				t.Fatal(err)
			}
			if profile.Provider.Protocol != ProtocolGemini || profile.Provider.Endpoint != test.endpoint {
				t.Fatalf("provider = %#v", profile.Provider)
			}
		})
	}
}

func TestProviderAdapterRegistryRejectsInvalidRegistrations(t *testing.T) {
	adapter := providerAdapterFuncs{
		protocolName:    ProtocolOpenAICompatible,
		capabilityNames: []string{"chat"},
		normalize: func(baseURL string, _ string) (string, error) {
			return baseURL, nil
		},
		completeRequest: func(context.Context, *runtimeModelClient, agentruntime.ModelRequest, int) (agentruntime.ModelResponse, bool, error) {
			return agentruntime.ModelResponse{}, false, nil
		},
		completeStreamFunc: func(context.Context, *streamingRuntimeModelClient, agentruntime.ModelRequest, func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return agentruntime.ModelResponse{}, nil
		},
	}
	tests := []struct {
		name     string
		adapters []providerAdapter
		want     string
	}{
		{name: "nil adapter", adapters: []providerAdapter{nil}, want: "adapter is required"},
		{name: "missing protocol", adapters: []providerAdapter{providerAdapterFuncs{}}, want: "protocol is required"},
		{name: "incomplete adapter", adapters: []providerAdapter{providerAdapterFuncs{
			protocolName: "incomplete", capabilityNames: []string{"chat"},
			normalize: func(baseURL string, _ string) (string, error) { return baseURL, nil },
		}}, want: "completion implementation is required"},
		{name: "duplicate protocol", adapters: []providerAdapter{adapter, adapter}, want: "registered more than once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProviderAdapterRegistry(test.adapters...); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("newProviderAdapterRegistry() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRuntimeModelClientRejectsUnknownProtocolBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "unknown", Type: "unknown", Protocol: "unknown-wire-format", Endpoint: server.URL},
		Model:    "unknown-model",
	}, server.Client(), nil)
	if err == nil || !strings.Contains(err.Error(), `provider protocol "unknown-wire-format" is not registered`) {
		t.Fatalf("NewRuntimeModelClient() client = %#v, error = %v", client, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("network requests = %d, want 0", requests.Load())
	}
}

func TestDefaultProviderAdaptersResolveExplicitProtocolsConcurrently(t *testing.T) {
	protocols := []string{
		ProtocolOpenAICompatible,
		ProtocolOpenAIResponses,
		ProtocolAzureOpenAI,
		ProtocolAzureOpenAIResponses,
		ProtocolGemini,
		ProtocolAnthropic,
	}
	const repetitions = 25
	errCh := make(chan error, len(protocols)*repetitions)
	var wait sync.WaitGroup
	for repetition := 0; repetition < repetitions; repetition++ {
		for _, protocol := range protocols {
			protocol := protocol
			wait.Add(1)
			go func() {
				defer wait.Done()
				adapter, err := defaultProviderAdapters.resolve(protocol)
				if err != nil {
					errCh <- err
					return
				}
				if adapter.protocol() != protocol {
					errCh <- &protocolMismatchError{got: adapter.protocol(), want: protocol}
				}
			}()
		}
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestProviderAdapterCapabilitiesAreDefensiveCopies(t *testing.T) {
	adapter, err := defaultProviderAdapters.resolve(ProtocolAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	first := adapter.capabilities()
	if len(first) == 0 {
		t.Fatal("Anthropic adapter capabilities are empty")
	}
	first[0] = "mutated"
	second := adapter.capabilities()
	if second[0] == "mutated" {
		t.Fatal("adapter capabilities leaked mutable registry state")
	}
}

type protocolMismatchError struct {
	got  string
	want string
}

func (e *protocolMismatchError) Error() string {
	return "resolved protocol " + e.got + ", want " + e.want
}
