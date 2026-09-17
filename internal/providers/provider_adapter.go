package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"synon-go/internal/agentruntime"
)

// providerAdapter is the only protocol-specific extension point used by the
// runtime client. Provider selection must resolve an explicit adapter before
// any request is created; unknown protocols never fall through to another
// wire format.
type providerAdapter interface {
	protocol() string
	validate() error
	capabilities() []string
	normalizeEndpoint(baseURL string, model string) (string, error)
	applyAuthHeaders(request *http.Request, profile ModelProfile)
	complete(context.Context, *runtimeModelClient, agentruntime.ModelRequest, int) (agentruntime.ModelResponse, bool, error)
	completeStream(context.Context, *streamingRuntimeModelClient, agentruntime.ModelRequest, func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error)
}

type providerAdapterFuncs struct {
	protocolName       string
	capabilityNames    []string
	normalize          func(string, string) (string, error)
	auth               func(*http.Request, ModelProfile)
	completeRequest    func(context.Context, *runtimeModelClient, agentruntime.ModelRequest, int) (agentruntime.ModelResponse, bool, error)
	completeStreamFunc func(context.Context, *streamingRuntimeModelClient, agentruntime.ModelRequest, func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error)
}

func (a providerAdapterFuncs) protocol() string { return a.protocolName }

func (a providerAdapterFuncs) validate() error {
	protocol := strings.TrimSpace(a.protocolName)
	if a.normalize == nil {
		return fmt.Errorf("provider adapter %q endpoint normalizer is required", protocol)
	}
	if a.completeRequest == nil {
		return fmt.Errorf("provider adapter %q completion implementation is required", protocol)
	}
	if a.completeStreamFunc == nil {
		return fmt.Errorf("provider adapter %q streaming implementation is required", protocol)
	}
	if len(a.capabilityNames) == 0 {
		return fmt.Errorf("provider adapter %q capabilities are required", protocol)
	}
	return nil
}

func (a providerAdapterFuncs) capabilities() []string {
	return append([]string(nil), a.capabilityNames...)
}

func (a providerAdapterFuncs) normalizeEndpoint(baseURL string, model string) (string, error) {
	if a.normalize == nil {
		return "", fmt.Errorf("provider protocol %q has no endpoint normalizer", a.protocolName)
	}
	return a.normalize(baseURL, model)
}

func (a providerAdapterFuncs) applyAuthHeaders(request *http.Request, profile ModelProfile) {
	if a.auth != nil {
		a.auth(request, profile)
	}
}

func (a providerAdapterFuncs) complete(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
	if a.completeRequest == nil {
		return agentruntime.ModelResponse{}, false, fmt.Errorf("provider protocol %q does not support completion", a.protocolName)
	}
	return a.completeRequest(ctx, client, request, attempt)
}

func (a providerAdapterFuncs) completeStream(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	if a.completeStreamFunc == nil {
		return agentruntime.ModelResponse{}, fmt.Errorf("provider protocol %q does not support streaming", a.protocolName)
	}
	return a.completeStreamFunc(ctx, client, request, emit)
}

type providerAdapterRegistry struct {
	byProtocol map[string]providerAdapter
}

func newProviderAdapterRegistry(adapters ...providerAdapter) (*providerAdapterRegistry, error) {
	registry := &providerAdapterRegistry{byProtocol: make(map[string]providerAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return nil, errors.New("provider adapter is required")
		}
		protocol := strings.ToLower(strings.TrimSpace(adapter.protocol()))
		if protocol == "" {
			return nil, errors.New("provider adapter protocol is required")
		}
		if _, exists := registry.byProtocol[protocol]; exists {
			return nil, fmt.Errorf("provider adapter protocol %q is registered more than once", protocol)
		}
		if err := adapter.validate(); err != nil {
			return nil, err
		}
		registry.byProtocol[protocol] = adapter
	}
	return registry, nil
}

func mustProviderAdapterRegistry(adapters ...providerAdapter) *providerAdapterRegistry {
	registry, err := newProviderAdapterRegistry(adapters...)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *providerAdapterRegistry) resolve(protocol string) (providerAdapter, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return nil, errors.New("model provider protocol is required")
	}
	if r == nil {
		return nil, errors.New("provider adapter registry is required")
	}
	adapter, found := r.byProtocol[protocol]
	if !found {
		return nil, fmt.Errorf("provider protocol %q is not registered", protocol)
	}
	return adapter, nil
}

func bearerAuth(request *http.Request, profile ModelProfile) {
	if key := strings.TrimSpace(profile.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
}

func azureAuth(request *http.Request, profile ModelProfile) {
	key := strings.TrimSpace(profile.APIKey)
	if key == "" {
		return
	}
	if azureOpenAIUsesBearerToken(profile.Provider.Type) {
		request.Header.Set("Authorization", "Bearer "+key)
		return
	}
	request.Header.Set("api-key", key)
}

func geminiAuth(request *http.Request, profile ModelProfile) {
	if key := strings.TrimSpace(profile.APIKey); key != "" {
		request.Header.Set("x-goog-api-key", key)
	}
}

func anthropicAuth(request *http.Request, profile ModelProfile) {
	if key := strings.TrimSpace(profile.APIKey); key != "" {
		request.Header.Set("x-api-key", key)
	}
	request.Header.Set("anthropic-version", anthropicAPIVersion)
}

func normalizeGeminiAdapterEndpoint(baseURL string, model string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("gemini model is required")
	}
	return normalizeGeminiEndpoint(baseURL, model)
}

var defaultProviderAdapters = mustProviderAdapterRegistry(
	providerAdapterFuncs{
		protocolName:    ProtocolOpenAICompatible,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "openai-compatible"},
		normalize:       func(baseURL string, _ string) (string, error) { return normalizeOpenAIEndpoint(baseURL) },
		auth:            bearerAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeOpenAICompatible(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeOpenAIChatStream(ctx, request, emit)
		},
	},
	providerAdapterFuncs{
		protocolName:    ProtocolOpenAIResponses,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "openai-responses"},
		normalize:       func(baseURL string, _ string) (string, error) { return normalizeOpenAIResponsesEndpoint(baseURL) },
		auth:            bearerAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeOpenAIResponses(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeOpenAIResponsesStream(ctx, request, emit)
		},
	},
	providerAdapterFuncs{
		protocolName:    ProtocolAzureOpenAI,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "azure-openai"},
		normalize:       func(baseURL string, _ string) (string, error) { return normalizeAzureOpenAIEndpoint(baseURL, false) },
		auth:            azureAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeOpenAICompatible(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeOpenAIChatStream(ctx, request, emit)
		},
	},
	providerAdapterFuncs{
		protocolName:    ProtocolAzureOpenAIResponses,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "azure-openai", "openai-responses"},
		normalize:       func(baseURL string, _ string) (string, error) { return normalizeAzureOpenAIEndpoint(baseURL, true) },
		auth:            azureAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeOpenAIResponses(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeOpenAIResponsesStream(ctx, request, emit)
		},
	},
	providerAdapterFuncs{
		protocolName:    ProtocolGemini,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "google-gemini"},
		normalize:       normalizeGeminiAdapterEndpoint,
		auth:            geminiAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeGemini(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeGeminiStream(ctx, request, emit)
		},
	},
	providerAdapterFuncs{
		protocolName:    ProtocolAnthropic,
		capabilityNames: []string{"chat", "tool-calls", "usage", "request-id", "anthropic-messages"},
		normalize:       func(baseURL string, _ string) (string, error) { return normalizeAnthropicEndpoint(baseURL) },
		auth:            anthropicAuth,
		completeRequest: func(ctx context.Context, client *runtimeModelClient, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
			return client.completeAnthropic(ctx, request, attempt)
		},
		completeStreamFunc: func(ctx context.Context, client *streamingRuntimeModelClient, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
			return client.completeAnthropicStream(ctx, request, emit)
		},
	},
)
