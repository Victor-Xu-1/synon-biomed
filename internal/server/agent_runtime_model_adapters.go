package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/providers"
	"time"
)

func (client sessionRunnerStaticStreamingCompatibilityClient) Complete(
	ctx context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	requestCtx, cancel := staticStreamingRequestContext(ctx, client.timeout)
	defer cancel()
	response, err := client.delegate.Complete(requestCtx, request)
	return response, normalizeStaticStreamingModelError(err)
}

func (client sessionRunnerStaticStreamingCompatibilityClient) CompleteStream(
	ctx context.Context,
	request agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	requestCtx, cancel := staticStreamingRequestContext(ctx, client.timeout)
	defer cancel()
	response, err := client.delegate.CompleteStream(requestCtx, request, emit)
	return response, normalizeStaticStreamingModelError(err)
}

func staticStreamingRequestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func normalizeStaticStreamingModelError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "provider stream response headers timeout") ||
		strings.Contains(lower, "provider stream idle timeout") || strings.Contains(lower, "provider stream total timeout") {
		return fmt.Errorf("OpenAI chat request failed: %w", context.DeadlineExceeded)
	}
	for _, prefix := range []string{"provider endpoint returned ", "model provider returned http "} {
		if index := strings.Index(lower, prefix); index >= 0 {
			return errors.New(message[:index] + "OpenAI chat endpoint returned " + message[index+len(prefix):])
		}
	}
	return err
}

func (m serverErrorModelClient) Complete(context.Context, agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{}, m.err
}

func (m serverErrorModelClient) CompleteStream(context.Context, agentruntime.ModelRequest, func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{}, m.err
}

func (serverBuiltinModelClient) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	if err := agentRuntimeContextError(ctx); err != nil {
		return agentruntime.ModelResponse{}, err
	}
	latestUser := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		if strings.TrimSpace(message.Role) == "user" && strings.TrimSpace(message.Content) != "" {
			latestUser = strings.TrimSpace(message.Content)
			break
		}
	}
	if latestUser == "" {
		latestUser = "No user message was available in the replay window."
	}
	content := "Synon built-in deterministic runner processed the latest session request.\n\nLatest user request:\n" + latestUser
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: content}}, nil
}

func (client serverBuiltinModelClient) CompleteStream(
	ctx context.Context,
	request agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	response, err := client.Complete(ctx, request)
	if err != nil {
		return response, err
	}
	if emit != nil && response.Message.Content != "" {
		if err := emit(agentruntime.ModelStreamEvent{ContentDelta: response.Message.Content}); err != nil {
			return agentruntime.ModelResponse{}, err
		}
	}
	return response, nil
}

func (client sessionRunnerAuditedStaticModelClient) Complete(
	ctx context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	startedAt := time.Now().UTC()
	response, err := client.delegate.Complete(ctx, request)
	finishedAt := time.Now().UTC()
	record := providers.AuditRecord{
		ProviderID:   client.profile.Provider.ID,
		ProviderType: client.profile.Provider.Type,
		Protocol:     client.profile.Provider.Protocol,
		Model:        firstNonEmpty(response.Model, client.profile.Model),
		Endpoint:     redactedStaticRunnerModelEndpoint(client.profile.Provider.Endpoint),
		RequestID:    response.RequestID,
		Attempt:      1,
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
		DurationMs:   finishedAt.Sub(startedAt).Milliseconds(),
	}
	if err != nil {
		record.Error = staticRunnerModelAuditError(err, client.profile)
	} else {
		record.HTTPStatus = http.StatusOK
		record.PromptTokens = response.Usage.InputTokens
		record.CompletionTokens = response.Usage.OutputTokens
		record.CacheReadTokens = response.Usage.CacheReadTokens
		record.CacheWriteTokens = response.Usage.CacheWriteTokens
		record.TotalTokens = response.Usage.TotalTokens
	}
	if client.audit != nil {
		client.audit(record)
	}
	return response, err
}

func redactedStaticRunnerModelEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid-provider-endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func staticRunnerModelAuditError(err error, profile providers.ModelProfile) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if secret := strings.TrimSpace(profile.APIKey); secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	if endpoint := strings.TrimSpace(profile.Provider.Endpoint); endpoint != "" {
		message = strings.ReplaceAll(message, endpoint, redactedStaticRunnerModelEndpoint(endpoint))
	}
	const maxAuditErrorRunes = 2048
	runes := []rune(message)
	if len(runes) > maxAuditErrorRunes {
		message = string(runes[:maxAuditErrorRunes]) + "..."
	}
	return message
}

func (s *Server) newAgentRuntimeEngine(options SessionRunnerChatOptions) agentruntime.Engine {
	return s.newAgentRuntimeEngineWithContext(context.Background(), options)
}
