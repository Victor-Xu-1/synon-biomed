package providers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	secretstore "synon-go/internal/persistence/secrets"
	settingsstore "synon-go/internal/persistence/settings"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	ProtocolOpenAICompatible     = "openai-compatible"
	ProtocolOpenAIResponses      = "openai-responses"
	ProtocolAzureOpenAI          = "azure-openai"
	ProtocolAzureOpenAIResponses = "azure-openai-responses"
	ProtocolGemini               = "gemini"
	ProtocolAnthropic            = "anthropic"
	defaultMaxResponseBytes      = int64(1024 * 1024)
)

type RequestProfile struct {
	Timeout          time.Duration     `json:"-"`
	MaxAttempts      int               `json:"maxAttempts"`
	MaxResponseBytes int64             `json:"maxResponseBytes"`
	Headers          map[string]string `json:"headers,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
}

type ProviderProfile struct {
	ID           string   `json:"id"`
	UserID       string   `json:"userId"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Protocol     string   `json:"protocol"`
	BaseURL      string   `json:"baseUrl"`
	Endpoint     string   `json:"endpoint"`
	SecretRef    string   `json:"secretRef,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type ModelProfile struct {
	Provider    ProviderProfile `json:"provider"`
	Model       string          `json:"model"`
	APIKey      string          `json:"-"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"maxTokens,omitempty"`
	Request     RequestProfile  `json:"request"`
}

type ResolutionInput struct {
	Context          context.Context
	ProjectID        string
	RequestTimeout   time.Duration
	MaxAttempts      int
	MaxResponseBytes int64
}

type Resolution struct {
	Resolved     bool          `json:"resolved"`
	UserID       string        `json:"userId"`
	Source       string        `json:"source,omitempty"`
	ProviderID   string        `json:"providerId,omitempty"`
	ModelProfile *ModelProfile `json:"modelProfile,omitempty"`
}

func ResolveRunnerModelProfile(settings *settingsstore.Store, workspaceStore *workspace.Store, secretStore *secretstore.Store, input ResolutionInput) (Resolution, error) {
	ctx := resolutionContext(input)
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	userID := secretstore.DefaultUserID
	if workspaceStore != nil && strings.TrimSpace(input.ProjectID) != "" {
		ownerID, found, err := workspaceStore.ProjectOwnerIDContext(ctx, strings.TrimSpace(input.ProjectID))
		if err != nil {
			return Resolution{}, err
		}
		if !found || strings.TrimSpace(ownerID) == "" {
			return Resolution{}, fmt.Errorf("project %s not found", strings.TrimSpace(input.ProjectID))
		}
		userID = strings.TrimSpace(ownerID)
	}
	result := Resolution{UserID: userID}
	if workspaceStore == nil {
		return result, nil
	}
	providers, err := workspaceStore.ListModelProvidersWithContext(ctx, userID)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	activeProviderID, err := readActiveProviderID(settings)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	selected, found, err := selectProvider(providers, activeProviderID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	profile, err := buildModelProfile(selected, secretStore, userID, input)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Resolved = true
	result.Source = "workspace-model-provider"
	result.ProviderID = selected.ID
	result.ModelProfile = &profile
	return result, nil
}

// ResolveUserModelProfile resolves an enabled saved provider for a specific
// model. The active provider wins when it exposes that model; otherwise the
// model must identify exactly one enabled provider owned by the project user.
func ResolveUserModelProfile(settings *settingsstore.Store, workspaceStore *workspace.Store, secretStore *secretstore.Store, userID string, model string, input ResolutionInput) (ModelProfile, error) {
	ctx := resolutionContext(input)
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	userID = strings.TrimSpace(userID)
	model = strings.TrimSpace(model)
	if workspaceStore == nil || userID == "" {
		return ModelProfile{}, errors.New("workspace model provider store and user are required")
	}
	providers, err := workspaceStore.ListModelProvidersWithContext(ctx, userID)
	if err != nil {
		return ModelProfile{}, err
	}
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	activeID, err := readActiveProviderID(settings)
	if err != nil {
		return ModelProfile{}, err
	}
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	if model == "" {
		selected, found, err := selectProvider(providers, activeID)
		if err != nil {
			return ModelProfile{}, err
		}
		if !found {
			return ModelProfile{}, errors.New("no active saved model provider is configured")
		}
		return buildModelProfile(selected, secretStore, userID, input)
	}
	var matches []workspace.ModelProvider
	for _, provider := range providers {
		if provider.Enabled && strings.TrimSpace(provider.Model) == model {
			if strings.TrimSpace(provider.ID) == activeID {
				return buildModelProfile(provider, secretStore, userID, input)
			}
			matches = append(matches, provider)
		}
	}
	if len(matches) == 0 {
		return ModelProfile{}, fmt.Errorf("model %q is not enabled for this user", model)
	}
	if len(matches) > 1 {
		return ModelProfile{}, fmt.Errorf("model %q is ambiguous across enabled providers", model)
	}
	return buildModelProfile(matches[0], secretStore, userID, input)
}

func ListEnabledUserModels(workspaceStore *workspace.Store, userID string) ([]string, error) {
	if workspaceStore == nil {
		return nil, errors.New("workspace model provider store is required")
	}
	providers, err := workspaceStore.ListModelProviders(strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	models := make([]string, 0, len(providers))
	for _, provider := range providers {
		model := strings.TrimSpace(provider.Model)
		if !provider.Enabled || model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}

func resolutionContext(input ResolutionInput) context.Context {
	if input.Context != nil {
		return input.Context
	}
	return context.Background()
}

func readActiveProviderID(settings *settingsstore.Store) (string, error) {
	if settings == nil {
		return "", nil
	}
	setting, found, err := settings.Get("model.activeProviderId")
	if err != nil || !found {
		return "", err
	}
	return strings.TrimSpace(stringValue(setting.Value)), nil
}

func selectProvider(providers []workspace.ModelProvider, activeProviderID string) (workspace.ModelProvider, bool, error) {
	activeProviderID = strings.TrimSpace(activeProviderID)
	if activeProviderID != "" {
		for _, provider := range providers {
			if strings.TrimSpace(provider.ID) != activeProviderID {
				continue
			}
			if !provider.Enabled {
				return workspace.ModelProvider{}, false, fmt.Errorf("model provider %s is disabled", activeProviderID)
			}
			return provider, true, nil
		}
		return workspace.ModelProvider{}, false, fmt.Errorf("model provider %s not found", activeProviderID)
	}
	return workspace.ModelProvider{}, false, nil
}

func buildModelProfile(provider workspace.ModelProvider, secretStore *secretstore.Store, userID string, input ResolutionInput) (ModelProfile, error) {
	if err := resolutionContext(input).Err(); err != nil {
		return ModelProfile{}, err
	}
	protocol, err := normalizeProviderProtocol(provider.Type, provider.BaseURL)
	if err != nil {
		return ModelProfile{}, err
	}
	endpoint, err := normalizeProviderEndpoint(protocol, provider.BaseURL, provider.Model)
	if err != nil {
		return ModelProfile{}, err
	}
	apiKey, err := resolveSecretRef(secretStore, userID, provider.SecretRef)
	if err != nil {
		return ModelProfile{}, err
	}
	if err := resolutionContext(input).Err(); err != nil {
		return ModelProfile{}, err
	}
	capabilities, err := providerCapabilities(protocol)
	if err != nil {
		return ModelProfile{}, err
	}
	maxAttempts := input.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	maxResponseBytes := input.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	return ModelProfile{
		Provider: ProviderProfile{
			ID:           strings.TrimSpace(provider.ID),
			UserID:       strings.TrimSpace(provider.UserID),
			Name:         strings.TrimSpace(provider.Name),
			Type:         strings.TrimSpace(provider.Type),
			Protocol:     protocol,
			BaseURL:      strings.TrimSpace(provider.BaseURL),
			Endpoint:     endpoint,
			SecretRef:    strings.TrimSpace(provider.SecretRef),
			Capabilities: capabilities,
		},
		Model:       strings.TrimSpace(provider.Model),
		APIKey:      apiKey,
		Temperature: provider.Temperature,
		MaxTokens:   provider.MaxTokens,
		Request: RequestProfile{
			Timeout:          input.RequestTimeout,
			MaxAttempts:      maxAttempts,
			MaxResponseBytes: maxResponseBytes,
			Capabilities:     []string{"timeout", "retry", "request-id", "usage", "bounded-response"},
		},
	}, nil
}

// BuildModelProfile resolves one explicitly selected stored provider. It is
// used by control-plane operations such as provider connectivity checks.
func BuildModelProfile(provider workspace.ModelProvider, secretStore *secretstore.Store, userID string, input ResolutionInput) (ModelProfile, error) {
	return buildModelProfile(provider, secretStore, userID, input)
}

func normalizeProviderProtocol(providerType string, baseURL string) (string, error) {
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case strings.Contains(providerType, "anthropic"), strings.Contains(providerType, "claude"), strings.Contains(baseURL, "api.anthropic.com"):
		return ProtocolAnthropic, nil
	case isAzureOpenAIProvider(providerType, baseURL) && (strings.Contains(providerType, "responses") || strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/responses")):
		return ProtocolAzureOpenAIResponses, nil
	case isAzureOpenAIProvider(providerType, baseURL):
		return ProtocolAzureOpenAI, nil
	case strings.Contains(providerType, "responses"), strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/responses"):
		return ProtocolOpenAIResponses, nil
	case strings.Contains(providerType, "bedrock"), strings.Contains(providerType, "vertex"), strings.Contains(baseURL, "aiplatform.googleapis.com"):
		return "", fmt.Errorf("provider protocol %q is not implemented", providerType)
	case strings.Contains(providerType, "gemini"), strings.Contains(baseURL, "generativelanguage.googleapis.com"):
		return ProtocolGemini, nil
	default:
		return ProtocolOpenAICompatible, nil
	}
}

func isAzureOpenAIProvider(providerType, baseURL string) bool {
	return strings.Contains(providerType, "azure") ||
		strings.Contains(baseURL, ".openai.azure.com") ||
		strings.Contains(baseURL, ".services.ai.azure.com") ||
		strings.Contains(baseURL, ".api.cognitive.microsoft.com")
}

func normalizeProviderEndpoint(protocol string, baseURL string, model string) (string, error) {
	adapter, err := defaultProviderAdapters.resolve(protocol)
	if err != nil {
		return "", err
	}
	return adapter.normalizeEndpoint(baseURL, model)
}

func normalizeOpenAIEndpoint(baseURL string) (string, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func normalizeOpenAIResponsesEndpoint(baseURL string) (string, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/responses") {
		path += "/responses"
	}
	if path == "/responses" {
		path = "/v1/responses"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func normalizeAzureOpenAIEndpoint(baseURL string, responses bool) (string, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch strings.ToLower(path) {
	case "", "/openai":
		path = "/openai/v1"
	}
	suffix := "/chat/completions"
	if responses {
		suffix = "/responses"
	}
	if !strings.HasSuffix(strings.ToLower(path), suffix) {
		path += suffix
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func normalizeGeminiEndpoint(baseURL string, model string) (string, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" && strings.EqualFold(parsed.Hostname(), "generativelanguage.googleapis.com") {
		path = "/v1beta"
	}
	switch {
	case strings.HasSuffix(path, ":generateContent"):
	case strings.Contains(path, "/models/"):
		path += ":generateContent"
	default:
		path += "/models/" + strings.TrimSpace(model) + ":generateContent"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func normalizeAnthropicEndpoint(baseURL string) (string, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/messages") {
		path += "/messages"
	}
	if path == "/messages" {
		path = "/v1/messages"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func parseProviderBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("model provider base url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse model provider base url: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("model provider base url must be an absolute HTTP URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("model provider base url must not contain user credentials")
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("model provider base url must not contain a fragment")
	}
	for key := range parsed.Query() {
		if isSensitiveProviderQueryKey(key) {
			return nil, fmt.Errorf("model provider base url must not contain credentials; use secretRef")
		}
	}
	return parsed, nil
}

func isSensitiveProviderQueryKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "authorization")
}

func providerCapabilities(protocol string) ([]string, error) {
	adapter, err := defaultProviderAdapters.resolve(protocol)
	if err != nil {
		return nil, err
	}
	return adapter.capabilities(), nil
}

func resolveSecretRef(store *secretstore.Store, userID string, secretRef string) (string, error) {
	secretRef = strings.TrimSpace(secretRef)
	if secretRef == "" {
		return "", nil
	}
	if store == nil {
		return "", fmt.Errorf("secret store is required to resolve provider credentials")
	}
	secretID, err := secretIDFromRef(secretRef)
	if err != nil {
		return "", err
	}
	secret, found, err := store.ResolveForUser(secretID, userID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("secret %s not found", secretID)
	}
	if strings.TrimSpace(secret.Value) != "" {
		return strings.TrimSpace(secret.Value), nil
	}
	for _, key := range []string{"apiKey", "api_key", "token", "access_token"} {
		if value := strings.TrimSpace(secret.Credentials[key]); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("secret %s does not contain an API key", secretID)
}

func secretIDFromRef(secretRef string) (string, error) {
	parsed, err := url.Parse(secretRef)
	if err != nil {
		return "", fmt.Errorf("parse secret ref %q: %w", secretRef, err)
	}
	if parsed.Scheme != "secret" {
		return "", fmt.Errorf("unsupported secret ref %q", secretRef)
	}
	secretID := strings.TrimSpace(parsed.Host + parsed.Path)
	secretID = strings.TrimPrefix(secretID, "/")
	if secretID == "" {
		return "", fmt.Errorf("secret ref %q is missing an id", secretRef)
	}
	return secretID, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}
