package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
)

var (
	errProviderResponseTooLarge  = errors.New("provider response too large")
	errProviderResponseTruncated = errors.New("provider response truncated")
	errProviderStreamIncomplete  = errors.New("provider stream ended before a terminal marker")
)

type AuditRecord struct {
	ProviderID              string    `json:"providerId"`
	ProviderType            string    `json:"providerType"`
	Protocol                string    `json:"protocol"`
	Model                   string    `json:"model"`
	Endpoint                string    `json:"endpoint"`
	RequestID               string    `json:"requestId,omitempty"`
	Attempt                 int       `json:"attempt"`
	HTTPStatus              int       `json:"httpStatus,omitempty"`
	PromptTokens            int       `json:"promptTokens,omitempty"`
	CompletionTokens        int       `json:"completionTokens,omitempty"`
	RequestedOutputTokens   int       `json:"requestedOutputTokens,omitempty"`
	ProviderMaxOutputTokens int       `json:"providerMaxOutputTokens,omitempty"`
	OutputTokenLimited      bool      `json:"outputTokenLimited,omitempty"`
	CacheReadTokens         int       `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens        int       `json:"cacheWriteTokens,omitempty"`
	TotalTokens             int       `json:"totalTokens,omitempty"`
	StartedAt               time.Time `json:"startedAt"`
	FinishedAt              time.Time `json:"finishedAt"`
	DurationMs              int64     `json:"durationMs"`
	Error                   string    `json:"error,omitempty"`
}

type providerTokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	TotalTokens      int
}

type runtimeModelClient struct {
	profile    ModelProfile
	httpClient *http.Client
	audit      func(AuditRecord)
	adapter    providerAdapter
}

func NewRuntimeModelClient(profile ModelProfile, httpClient *http.Client, audit func(AuditRecord)) (agentruntime.ModelClient, error) {
	adapter, err := defaultProviderAdapters.resolve(profile.Provider.Protocol)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(profile.Provider.Endpoint)
	if endpoint == "" {
		return nil, errors.New("provider endpoint is required")
	}
	if _, err := parseProviderBaseURL(endpoint); err != nil {
		return nil, err
	}
	if strings.TrimSpace(profile.Model) == "" {
		return nil, errors.New("provider model is required")
	}
	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}
	base := &runtimeModelClient{profile: profile, httpClient: client, audit: audit, adapter: adapter}
	return &streamingRuntimeModelClient{runtimeModelClient: base}, nil
}

func (c *runtimeModelClient) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	request = c.applyProfileGenerationControls(request)
	if err := rejectUnencodedMediaParts(c.profile.Provider.Protocol, request); err != nil {
		return agentruntime.ModelResponse{}, err
	}
	maxAttempts := c.profile.Request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retryable, err := c.adapter.complete(ctx, c, request, attempt)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryable || attempt >= maxAttempts {
			break
		}
		if err := waitProviderRetry(ctx, attempt, lastErr); err != nil {
			return agentruntime.ModelResponse{}, err
		}
	}
	return agentruntime.ModelResponse{}, lastErr
}

func (c *runtimeModelClient) applyProfileGenerationControls(request agentruntime.ModelRequest) agentruntime.ModelRequest {
	if request.Temperature == nil && c.profile.Temperature != nil {
		value := *c.profile.Temperature
		request.Temperature = &value
	}
	if request.MaxTokens <= 0 && c.profile.MaxTokens != nil {
		request.MaxTokens = *c.profile.MaxTokens
	}
	return request
}

func rejectUnencodedMediaParts(protocol string, request agentruntime.ModelRequest) error {
	if protocol == ProtocolOpenAICompatible || protocol == ProtocolAzureOpenAI || protocol == ProtocolOpenAIResponses || protocol == ProtocolAzureOpenAIResponses || protocol == ProtocolGemini || protocol == ProtocolAnthropic {
		return nil
	}
	for messageIndex, message := range request.Messages {
		for partIndex, part := range message.Parts {
			if part.Type != agentruntime.ContentPartText {
				return fmt.Errorf("provider request contains media part at message %d part %d; protocol-specific media encoding is not implemented", messageIndex, partIndex)
			}
		}
	}
	return nil
}

func (c *runtimeModelClient) completeOpenAICompatible(ctx context.Context, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
	messages, err := openAIMessagesFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	maxTokens, maxCompletionTokens := openAIChatTokenBudget(c.profile, request.MaxTokens)
	payload, err := json.Marshal(openAIChatRequest{
		Model:               c.profile.Model,
		Messages:            messages,
		Tools:               openAIToolsFromRuntime(request.Tools),
		Temperature:         modelTemperature(request),
		MaxTokens:           maxTokens,
		MaxCompletionTokens: maxCompletionTokens,
		ToolChoice:          openAIChatToolChoiceForProfile(c.profile, request.ToolChoice),
		Metadata:            request.Metadata,
		Thinking:            openAIThinkingForProfile(c.profile, request.ReasoningMode),
	})
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	return c.completeJSON(ctx, attempt, c.profile.Provider.Endpoint, payload, request, c.openAIResponseDecoder)
}

func (c *runtimeModelClient) completeGemini(ctx context.Context, request agentruntime.ModelRequest, attempt int) (agentruntime.ModelResponse, bool, error) {
	systemInstruction, contents, err := geminiContentsFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	generationConfig := map[string]any{"temperature": *modelTemperature(request)}
	if request.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = request.MaxTokens
	}
	payloadBody := geminiGenerateContentRequest{
		Contents:         contents,
		Tools:            geminiToolsFromRuntime(request.Tools),
		GenerationConfig: generationConfig,
		ToolConfig:       geminiToolConfig(request.ToolChoice),
	}
	if systemInstruction != nil {
		payloadBody.SystemInstruction = systemInstruction
	}
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	endpoint, err := c.geminiEndpointURL()
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	return c.completeJSON(ctx, attempt, endpoint, payload, request, c.geminiResponseDecoder)
}

func modelTemperature(request agentruntime.ModelRequest) *float64 {
	if request.Temperature != nil {
		value := *request.Temperature
		return &value
	}
	value := 0.2
	return &value
}

func openAIChatTokenBudget(profile ModelProfile, value int) (maxTokens int, maxCompletionTokens int) {
	if value <= 0 {
		return 0, 0
	}
	if usesMiMoV25ChatContract(profile) {
		return 0, value
	}
	return value, 0
}

func usesMiMoV25ChatContract(profile ModelProfile) bool {
	model := strings.ToLower(strings.TrimSpace(profile.Model))
	if model != "mimo-v2.5" && !strings.HasPrefix(model, "mimo-v2.5-") {
		return false
	}
	providerType := strings.ToLower(strings.TrimSpace(profile.Provider.Type))
	if strings.Contains(providerType, "xiaomi") || strings.Contains(providerType, "mimo") {
		return true
	}
	return isOfficialMiMoAPIURL(profile.Provider.BaseURL) || isOfficialMiMoAPIURL(profile.Provider.Endpoint)
}

func isOfficialMiMoAPIURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "xiaomimimo.com" || strings.HasSuffix(host, ".xiaomimimo.com")
}

func geminiToolConfig(choice any) map[string]any {
	if choice == nil {
		return nil
	}
	mode := "AUTO"
	switch value := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "auto":
			mode = "AUTO"
		default:
			return nil
		}
	case map[string]any:
		if kind, _ := value["type"].(string); strings.EqualFold(strings.TrimSpace(kind), "tool") {
			if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
				return map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []string{strings.TrimSpace(name)}}}
			}
		}
		if function, ok := value["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && strings.TrimSpace(name) != "" {
				return map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []string{strings.TrimSpace(name)}}}
			}
		}
		return nil
	default:
		return nil
	}
	return map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
}

func openAIChatToolChoice(choice any) any {
	value, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	kind, _ := value["type"].(string)
	name, _ := value["name"].(string)
	if strings.EqualFold(strings.TrimSpace(kind), "tool") && strings.TrimSpace(name) != "" {
		return map[string]any{"type": "function", "function": map[string]any{"name": strings.TrimSpace(name)}}
	}
	return choice
}

// openAIChatToolChoiceForProfile keeps named tool selection for normal
// OpenAI-compatible endpoints, but adapts the Ark thinking-mode contract.
// The configured ark-code-latest endpoint accepts tools while rejecting an
// object/required tool_choice with HTTP 400. "auto" is the provider-supported
// representation that keeps the tool set available without sending a request
// shape the endpoint cannot parse.
func openAIChatToolChoiceForProfile(profile ModelProfile, choice any) any {
	if !usesArkThinkingChatContract(profile) {
		return openAIChatToolChoice(choice)
	}
	switch value := choice.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(value), "required") {
			return "auto"
		}
	case map[string]any:
		if len(value) > 0 {
			return "auto"
		}
	}
	return openAIChatToolChoice(choice)
}

func usesArkThinkingChatContract(profile ModelProfile) bool {
	model := strings.ToLower(strings.TrimSpace(profile.Model))
	providerType := strings.ToLower(strings.TrimSpace(profile.Provider.Type))
	if strings.HasPrefix(model, "ark-") {
		return true
	}
	return strings.Contains(providerType, "volcengine-ark") || strings.Contains(providerType, "volcengine_ark")
}

func (c *runtimeModelClient) completeJSON(ctx context.Context, attempt int, endpoint string, payload []byte, request agentruntime.ModelRequest, decode func([]byte, http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error)) (agentruntime.ModelResponse, bool, error) {
	startedAt := time.Now().UTC()
	requestCtx := ctx
	cancel := func() {}
	if c.profile.Request.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.profile.Request.Timeout)
	}
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return agentruntime.ModelResponse{}, false, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Request-ID", newProviderRequestID())
	c.applyHeaders(httpRequest, request.Headers)
	outboundRequestID := httpRequest.Header.Get("X-Request-ID")

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		record := c.baseAuditRecord(attempt, endpoint, startedAt)
		record.FinishedAt = time.Now().UTC()
		record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Error = c.redactSensitiveText(err.Error())
		record.RequestID = outboundRequestID
		c.emitAudit(record)
		return agentruntime.ModelResponse{}, requestCtx.Err() == nil, err
	}
	defer response.Body.Close()
	body, readErr := readBoundedProviderBody(response.Body, c.profile.Request.MaxResponseBytes)
	if readErr != nil {
		record := c.baseAuditRecord(attempt, endpoint, startedAt)
		record.HTTPStatus = response.StatusCode
		record.RequestID = firstNonEmpty(requestIDFromHeaders(response.Header), outboundRequestID)
		record.FinishedAt = time.Now().UTC()
		record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Error = c.redactSensitiveText(readErr.Error())
		c.emitAudit(record)
		retryable := !errors.Is(readErr, errProviderResponseTooLarge) && providerHTTPStatusRetryable(response.StatusCode, nil)
		return agentruntime.ModelResponse{}, retryable, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		record := c.baseAuditRecord(attempt, endpoint, startedAt)
		record.HTTPStatus = response.StatusCode
		record.RequestID = firstNonEmpty(requestIDFromHeaders(response.Header), outboundRequestID)
		record.FinishedAt = time.Now().UTC()
		record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Error = c.redactSensitiveText(formatHTTPError(response.StatusCode, body))
		var responseErr error = newResponseHTTPError(response.StatusCode, record.Error, body)
		if maximum, ok := ProviderOutputTokenMaximum(responseErr); ok {
			record.ProviderMaxOutputTokens = maximum
		}
		c.emitAudit(record)
		retryable := providerHTTPStatusRetryable(response.StatusCode, body)
		if retryable {
			responseErr = withProviderRetryAfter(responseErr, response.Header, time.Now())
		}
		return agentruntime.ModelResponse{}, retryable, responseErr
	}
	decoded, requestID, usage, err := decode(body, response.Header)
	err = classifyDecodedOutputLimit(decoded, err)
	record := c.baseAuditRecord(attempt, endpoint, startedAt)
	record.HTTPStatus = response.StatusCode
	record.RequestID = firstNonEmpty(requestID, requestIDFromHeaders(response.Header), outboundRequestID)
	record.PromptTokens = usage.PromptTokens
	record.RequestedOutputTokens = c.effectiveRequestMaxTokens(request)
	record.CompletionTokens = usage.CompletionTokens
	record.CacheReadTokens = usage.CacheReadTokens
	record.CacheWriteTokens = usage.CacheWriteTokens
	record.TotalTokens = usage.TotalTokens
	record.FinishedAt = time.Now().UTC()
	record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
	if err != nil {
		err = enrichOutputLimitFailure(err, c.effectiveRequestMaxTokens(request), &record, usage)
		record.Error = err.Error()
		c.emitAudit(record)
		return agentruntime.ModelResponse{}, false, err
	}
	c.emitAudit(record)
	return c.responseWithUsage(decoded, record), false, nil
}

func (c *runtimeModelClient) applyHeaders(request *http.Request, headers map[string]string) {
	if request == nil {
		return
	}
	c.adapter.applyAuthHeaders(request, c.profile)
	for key, value := range c.profile.Request.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			request.Header.Set(key, value)
		}
	}
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			request.Header.Set(key, value)
		}
	}
}

func azureOpenAIUsesBearerToken(providerType string) bool {
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	return strings.Contains(providerType, "entra") ||
		strings.Contains(providerType, "oauth") || strings.Contains(providerType, "aad")
}

func (c *runtimeModelClient) geminiEndpointURL() (string, error) {
	parsed, err := url.Parse(c.profile.Provider.Endpoint)
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("Gemini provider endpoint must be an absolute HTTP URL")
	}
	return parsed.String(), nil
}

func (c *runtimeModelClient) baseAuditRecord(attempt int, endpoint string, startedAt time.Time) AuditRecord {
	return AuditRecord{
		ProviderID:   c.profile.Provider.ID,
		ProviderType: c.profile.Provider.Type,
		Protocol:     c.profile.Provider.Protocol,
		Model:        c.profile.Model,
		Endpoint:     redactedProviderEndpoint(endpoint),
		Attempt:      attempt,
		StartedAt:    startedAt,
	}
}

func (c *runtimeModelClient) emitAudit(record AuditRecord) {
	if c.audit != nil {
		c.audit(record)
	}
}

func (c *runtimeModelClient) openAIResponseDecoder(body []byte, headers http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error) {
	var decoded openAIChatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agentruntime.ModelResponse{}, "", providerTokenUsage{}, err
	}
	usage := providerTokenUsage{
		PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens,
		CacheReadTokens: decoded.Usage.PromptTokensDetails.CachedTokens, TotalTokens: decoded.Usage.TotalTokens,
	}
	if len(decoded.Choices) == 0 {
		return agentruntime.ModelResponse{}, decoded.ID, usage, errors.New("OpenAI-compatible response has no choices")
	}
	if len(decoded.Choices[0].Message.ToolCalls) > 0 {
		if limited := responseOutputLimit(decoded.Choices[0].FinishReason, true); limited != nil {
			return agentruntime.ModelResponse{}, decoded.ID, usage, limited
		}
	}
	message, err := runtimeMessageFromOpenAI(decoded.Choices[0].Message)
	if err != nil {
		return agentruntime.ModelResponse{}, decoded.ID, usage, err
	}
	if isTokenLimitFinishReason(decoded.Choices[0].FinishReason) {
		if len(message.ToolCalls) != 0 {
			return agentruntime.ModelResponse{}, decoded.ID, usage,
				fmt.Errorf("%w: finish reason %s", errProviderResponseTruncated, decoded.Choices[0].FinishReason)
		}
		// Some OpenAI-compatible endpoints ignore stream=true and return one
		// JSON response. Preserve its content-only prefix so the streaming
		// caller can durably checkpoint it before continuing. An empty prefix
		// is still typed as continuation-safe and is bounded by the runner's
		// no-progress budget; a partial tool call remains terminal above.
		return agentruntime.ModelResponse{
				Message: message, Model: decoded.Model,
				StopReason: decoded.Choices[0].FinishReason,
			}, firstNonEmpty(decoded.ID, decoded.RequestID), usage,
			newProviderContentResponseTruncation(decoded.Choices[0].FinishReason)
	}
	return agentruntime.ModelResponse{
		Message: message, Model: decoded.Model,
		StopReason: decoded.Choices[0].FinishReason,
	}, firstNonEmpty(decoded.ID, decoded.RequestID), usage, nil
}

func isTokenLimitFinishReason(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "length")
}

func (c *runtimeModelClient) geminiResponseDecoder(body []byte, headers http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error) {
	var decoded geminiGenerateContentResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agentruntime.ModelResponse{}, "", providerTokenUsage{}, err
	}
	usage := providerTokenUsage{
		PromptTokens: decoded.UsageMetadata.PromptTokenCount, CompletionTokens: decoded.UsageMetadata.CandidatesTokenCount,
		CacheReadTokens: decoded.UsageMetadata.CachedContentTokenCount,
		TotalTokens:     decoded.UsageMetadata.TotalTokenCount,
	}
	if len(decoded.Candidates) == 0 {
		return agentruntime.ModelResponse{}, decoded.ResponseID, usage, errors.New("Gemini response has no candidates")
	}
	if isProviderStreamTruncatedReason(decoded.Candidates[0].FinishReason) {
		for _, part := range decoded.Candidates[0].Content.Parts {
			if part.FunctionCall != nil {
				return agentruntime.ModelResponse{}, decoded.ResponseID, usage, responseOutputLimit(decoded.Candidates[0].FinishReason, true)
			}
		}
	}
	message, err := runtimeMessageFromGemini(decoded.Candidates[0].Content)
	if err != nil {
		return agentruntime.ModelResponse{}, decoded.ResponseID, usage, err
	}
	return agentruntime.ModelResponse{Message: message, Model: decoded.ModelVersion, StopReason: decoded.Candidates[0].FinishReason}, decoded.ResponseID, usage, nil
}

func requestIDFromHeaders(headers http.Header) string {
	for _, key := range []string{"x-request-id", "request-id", "x-goog-request-id"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func (c *runtimeModelClient) redactSensitiveText(value string) string {
	secret := strings.TrimSpace(c.profile.APIKey)
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

func newProviderRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "synon-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("synon-%d", time.Now().UTC().UnixNano())
}

func readBoundedProviderBody(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: response exceeded %d bytes", errProviderResponseTooLarge, limit)
	}
	return body, nil
}

func redactedProviderEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "<invalid-provider-endpoint>"
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(lower, "key") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "authorization") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func formatHTTPError(status int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("provider endpoint returned %d", status)
	}
	return fmt.Sprintf("provider endpoint returned %d: %s", status, trimmed)
}

type openAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	Tools               []openAITool    `json:"tools,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	Metadata            map[string]any  `json:"metadata,omitempty"`
	Thinking            *openAIThinking `json:"thinking,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

func openAIThinkingForProfile(profile ModelProfile, mode agentruntime.ReasoningMode) *openAIThinking {
	if !usesArkThinkingChatContract(profile) || mode != agentruntime.ReasoningModeDisabled {
		return nil
	}
	return &openAIThinking{Type: "disabled"}
}

type openAIMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIChatContentPart struct {
	Type       string                `json:"type"`
	Text       string                `json:"text,omitempty"`
	ImageURL   *openAIChatImageURL   `json:"image_url,omitempty"`
	InputAudio *openAIChatInputAudio `json:"input_audio,omitempty"`
	File       *openAIChatInputFile  `json:"file,omitempty"`
}

type openAIChatImageURL struct {
	URL string `json:"url"`
}

type openAIChatInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type openAIChatInputFile struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	ID        string `json:"id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Model     string `json:"model,omitempty"`
	Choices   []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func openAIMessagesFromRuntime(messages []agentruntime.Message) ([]openAIMessage, error) {
	out := make([]openAIMessage, 0, len(messages))
	for messageIndex, message := range messages {
		content, err := openAIChatContentFromRuntime(message)
		if err != nil {
			return nil, fmt.Errorf("OpenAI chat message %d: %w", messageIndex, err)
		}
		out = append(out, openAIMessage{
			Role:             message.Role,
			Content:          content,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
			ToolCalls:        openAIToolCallsFromRuntime(message.ToolCalls),
		})
	}
	return out, nil
}

func openAIChatContentFromRuntime(message agentruntime.Message) (any, error) {
	if len(message.Parts) == 0 {
		return message.Content, nil
	}
	parts := make([]openAIChatContentPart, 0, len(message.Parts)+1)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, openAIChatContentPart{Type: "text", Text: message.Content})
	}
	for index, part := range message.Parts {
		switch part.Type {
		case agentruntime.ContentPartText:
			parts = append(parts, openAIChatContentPart{Type: "text", Text: part.Text})
		case agentruntime.ContentPartImage, agentruntime.ContentPartDocument, agentruntime.ContentPartAudio:
			if part.Media == nil || part.Media.Source.Type != agentruntime.MediaSourceData || len(part.Media.Source.Data) == 0 {
				return nil, fmt.Errorf("media part %d must be normalized inline data", index)
			}
			encoded := base64.StdEncoding.EncodeToString(part.Media.Source.Data)
			switch part.Type {
			case agentruntime.ContentPartImage:
				parts = append(parts, openAIChatContentPart{Type: "image_url", ImageURL: &openAIChatImageURL{URL: "data:" + part.Media.MIMEType + ";base64," + encoded}})
			case agentruntime.ContentPartDocument:
				parts = append(parts, openAIChatContentPart{Type: "file", File: &openAIChatInputFile{Filename: part.Media.Filename, FileData: "data:" + part.Media.MIMEType + ";base64," + encoded}})
			case agentruntime.ContentPartAudio:
				format := "wav"
				if part.Media.MIMEType == "audio/mpeg" {
					format = "mp3"
				}
				parts = append(parts, openAIChatContentPart{Type: "input_audio", InputAudio: &openAIChatInputAudio{Data: encoded, Format: format}})
			}
		default:
			return nil, fmt.Errorf("unsupported content part %q", part.Type)
		}
	}
	return parts, nil
}

func openAIToolCallsFromRuntime(calls []agentruntime.ToolCall) []openAIToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openAIToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, openAIToolCall{
			ID:   call.ID,
			Type: "function",
			Function: openAIToolCallFunction{
				Name:      call.Name,
				Arguments: string(call.Arguments),
			},
		})
	}
	return out
}

func openAIToolsFromRuntime(tools []agentruntime.ToolSchema) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return out
}

func runtimeMessageFromOpenAI(message openAIMessage) (agentruntime.Message, error) {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		role = "assistant"
	}
	toolCalls, err := runtimeToolCallsFromOpenAI(message.ToolCalls)
	if err != nil {
		return agentruntime.Message{}, err
	}
	runtimeMessage := agentruntime.Message{
		Role:             role,
		Content:          openAIResponseText(message.Content),
		ReasoningContent: message.ReasoningContent,
		ToolCallID:       message.ToolCallID,
		ToolCalls:        toolCalls,
	}
	return normalizeOpenAITextControlEnvelope(runtimeMessage)
}

func openAIResponseText(content any) string {
	if value, ok := content.(string); ok {
		return value
	}
	return ""
}

func runtimeToolCallsFromOpenAI(calls []openAIToolCall) ([]agentruntime.ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	out := make([]agentruntime.ToolCall, 0, len(calls))
	for _, call := range calls {
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			return nil, newRetryableModelProtocolError(errors.New("OpenAI-compatible tool call is missing an id"))
		}
		name, err := normalizeModelToolName(call.Function.Name)
		if err != nil {
			return nil, newRetryableModelProtocolError(err)
		}
		protocolDiagnostic := ""
		arguments, valid := normalizeProviderToolArgumentsObject(call.Function.Arguments)
		if !valid {
			log.Printf("provider_tool_arguments_unresolved transport=nonstream %s",
				providerToolArgumentsSyntaxDiagnostic(call.Function.Arguments))
			protocolDiagnostic = fmt.Sprintf("OpenAI-compatible tool call %s has invalid JSON object arguments", callID)
			arguments = json.RawMessage(`{}`)
		}
		out = append(out, agentruntime.ToolCall{
			ID: callID, Name: name, Arguments: arguments,
			ProviderProtocolDiagnostic: protocolDiagnostic,
		})
	}
	return out, nil
}

type geminiGenerateContentRequest struct {
	SystemInstruction *geminiContent  `json:"system_instruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	GenerationConfig  map[string]any  `json:"generationConfig,omitempty"`
	ToolConfig        map[string]any  `json:"toolConfig,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiGenerateContentResponse struct {
	ResponseID   string `json:"responseId,omitempty"`
	ModelVersion string `json:"modelVersion,omitempty"`
	Candidates   []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason,omitempty"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func geminiContentsFromRuntime(messages []agentruntime.Message) (*geminiContent, []geminiContent, error) {
	toolNameByID := map[string]string{}
	systemParts := make([]geminiPart, 0)
	contents := make([]geminiContent, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(strings.ToLower(message.Role))
		switch role {
		case "system":
			if len(message.Parts) > 0 {
				return nil, nil, errors.New("Gemini system messages do not support media parts")
			}
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, geminiPart{Text: message.Content})
			}
			continue
		case "assistant":
			if len(message.Parts) > 0 {
				return nil, nil, errors.New("Gemini assistant history does not support media parts")
			}
			parts := make([]geminiPart, 0, 1+len(message.ToolCalls))
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, geminiPart{Text: message.Content})
			}
			for _, call := range message.ToolCalls {
				toolNameByID[call.ID] = call.Name
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: call.Name, Args: jsonObjectFromRaw(call.Arguments)}})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "model", Parts: parts})
			}
		case "tool":
			if len(message.Parts) > 0 {
				return nil, nil, errors.New("Gemini tool results do not support media parts")
			}
			name := toolNameByID[message.ToolCallID]
			if strings.TrimSpace(name) == "" {
				name = "tool_result"
			}
			contents = append(contents, geminiContent{Role: "user", Parts: []geminiPart{{FunctionResponse: &geminiFunctionResponse{Name: name, Response: map[string]any{"name": name, "content": runtimeToolResultPayload(message.Content)}}}}})
		default:
			parts := make([]geminiPart, 0, len(message.Parts)+1)
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, geminiPart{Text: message.Content})
			}
			for index, part := range message.Parts {
				switch part.Type {
				case agentruntime.ContentPartText:
					parts = append(parts, geminiPart{Text: part.Text})
				case agentruntime.ContentPartImage, agentruntime.ContentPartDocument, agentruntime.ContentPartAudio:
					if part.Media == nil || part.Media.Source.Type != agentruntime.MediaSourceData || len(part.Media.Source.Data) == 0 {
						return nil, nil, fmt.Errorf("Gemini media part %d must be normalized inline data", index)
					}
					parts = append(parts, geminiPart{InlineData: &geminiInlineData{
						MIMEType: part.Media.MIMEType,
						Data:     base64.StdEncoding.EncodeToString(part.Media.Source.Data),
					}})
				default:
					return nil, nil, fmt.Errorf("Gemini does not support content part %q", part.Type)
				}
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "user", Parts: parts})
			}
		}
	}
	var systemInstruction *geminiContent
	if len(systemParts) > 0 {
		systemInstruction = &geminiContent{Parts: systemParts}
	}
	return systemInstruction, contents, nil
}

func geminiToolsFromRuntime(tools []agentruntime.ToolSchema) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		declarations = append(declarations, geminiFunctionDeclaration{Name: tool.Name, Description: tool.Description, Parameters: parameters})
	}
	return []geminiTool{{FunctionDeclarations: declarations}}
}

func runtimeMessageFromGemini(content geminiContent) (agentruntime.Message, error) {
	parts := make([]string, 0, len(content.Parts))
	calls := make([]agentruntime.ToolCall, 0)
	for index, part := range content.Parts {
		if !part.Thought && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
		if part.FunctionCall != nil {
			name, err := normalizeModelToolName(part.FunctionCall.Name)
			if err != nil {
				return agentruntime.Message{}, err
			}
			raw, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return agentruntime.Message{}, err
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				raw = []byte(`{}`)
			}
			calls = append(calls, agentruntime.ToolCall{ID: fmt.Sprintf("gemini_call_%d", index+1), Name: name, Arguments: json.RawMessage(raw)})
		}
	}
	return agentruntime.Message{Role: "assistant", Content: strings.Join(parts, "\n\n"), ToolCalls: calls}, nil
}

func jsonObjectFromRaw(raw json.RawMessage) map[string]any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return map[string]any{"raw": string(trimmed)}
	}
	return decoded
}

func runtimeToolResultPayload(content string) any {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return map[string]any{"raw": trimmed}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
