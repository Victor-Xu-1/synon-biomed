package server

import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"net/http"

	"strings"

	"time"

	"synon-go/internal/agentruntime"

	eventjournal "synon-go/internal/persistence/journal"

	sessionstore "synon-go/internal/persistence/sessions"

	"synon-go/internal/providers"
)

func agentRuntimeMessagesFromChat(messages []chatCompletionMessage) []agentruntime.Message {
	out := make([]agentruntime.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, agentruntime.Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			ToolCalls:  agentRuntimeToolCallsFromChat(message.ToolCalls),
		})
	}
	return out
}

func agentRuntimeToolCallsFromChat(calls []chatCompletionToolCall) []agentruntime.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]agentruntime.ToolCall, 0, len(calls))
	for _, call := range calls {
		arguments := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		out = append(out, agentruntime.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	return out
}

func builtinSessionRunnerChatResponse(messages []chatCompletionMessage) chatCompletionResponse {
	latestUser := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) == "user" && strings.TrimSpace(messages[i].Content) != "" {
			latestUser = strings.TrimSpace(messages[i].Content)
			break
		}
	}
	if latestUser == "" {
		latestUser = "No user message was available in the replay window."
	}
	content := "Synon built-in deterministic runner processed the latest session request.\n\nLatest user request:\n" + latestUser
	response := chatCompletionResponse{}
	response.Choices = append(response.Choices, struct {
		Message chatCompletionMessage `json:"message"`
	}{Message: chatCompletionMessage{Role: "assistant", Content: content}})
	return response
}

// BuiltinSessionRunnerSmoke exercises the same deterministic response path used
// by the built-in runner without starting a session worker.
func BuiltinSessionRunnerSmoke(prompt string) (string, error) {
	response := builtinSessionRunnerChatResponse([]chatCompletionMessage{{Role: "user", Content: strings.TrimSpace(prompt)}})
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", errors.New("built-in runner returned no assistant content")
	}
	return response.Choices[0].Message.Content, nil
}
func (s *Server) callSessionRunnerChat(ctx context.Context, options SessionRunnerChatOptions, messages []chatCompletionMessage, tools []chatCompletionTool) (chatCompletionResponse, error) {
	if options.Endpoint == BuiltinSessionRunnerChatEndpoint {
		return builtinSessionRunnerChatResponse(messages), nil
	}
	rawRequest, err := json.Marshal(chatCompletionRequest{
		Model:       options.Model,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.2,
	})
	if err != nil {
		return chatCompletionResponse{}, err
	}
	var lastErr error
	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		decoded, retryable, err := s.callSessionRunnerChatAttempt(ctx, options, rawRequest)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
		if !retryable || attempt == options.MaxAttempts {
			break
		}
	}
	return chatCompletionResponse{}, lastErr
}

func (s *Server) callSessionRunnerChatAttempt(ctx context.Context, options SessionRunnerChatOptions, rawRequest []byte) (chatCompletionResponse, bool, error) {
	requestCtx := ctx
	cancel := func() {}
	if options.RequestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, options.RequestTimeout)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, options.Endpoint, bytes.NewReader(rawRequest))
	if err != nil {
		return chatCompletionResponse{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	if options.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+options.APIKey)
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return chatCompletionResponse{}, false, err
	}
	defer response.Body.Close()
	body, err := readBoundedBody(response.Body, options.ModelResponseLimitBytes)
	if err != nil {
		return chatCompletionResponse{}, false, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return chatCompletionResponse{}, retryable, fmt.Errorf("runner chat endpoint returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatCompletionResponse{}, false, fmt.Errorf("decode runner chat response: %w", err)
	}
	return decoded, false, nil
}

func firstChatChoiceMessage(response chatCompletionResponse) (chatCompletionMessage, error) {
	if len(response.Choices) == 0 {
		return chatCompletionMessage{}, errors.New("runner chat response has no choices")
	}
	message := response.Choices[0].Message
	message.Role = strings.TrimSpace(message.Role)
	if message.Role == "" {
		message.Role = "assistant"
	}
	if message.Role != "assistant" {
		return chatCompletionMessage{}, fmt.Errorf("runner chat response role must be assistant, got %s", message.Role)
	}
	return message, nil
}

func (s *Server) resolveSessionRunnerModelAuthority(ctx context.Context, session sessionstore.Session, options SessionRunnerChatOptions, attempt int) (SessionRunnerChatOptions, error) {
	resolutionInput := providers.ResolutionInput{
		Context:          ctx,
		ProjectID:        sessionRunnerProjectID(session),
		RequestTimeout:   options.RequestTimeout,
		MaxAttempts:      options.MaxAttempts,
		MaxResponseBytes: options.ModelResponseLimitBytes,
	}
	profile, _, snapshot, err := s.resolveSessionModelProfileSnapshotWithFallback(session, resolutionInput, "", "")
	options.modelSelection = snapshot.Selection
	options.modelSelectionRevision = snapshot.Revision
	if err != nil {
		return options, wrapSessionRunnerModelCallError(err, snapshot, "", "")
	}
	if profile != nil {
		options.Endpoint = profile.Provider.Endpoint
		options.APIKey = profile.APIKey
		options.Model = profile.Model
		options.ModelProfile = profile
	}
	options.ModelAudit = func(record providers.AuditRecord) {
		s.recordSessionRunnerModelAudit(session.ID, attempt, record)
	}
	if options.RequireSavedModel && options.ModelProfile == nil {
		return options, wrapSessionRunnerModelCallError(
			errors.New("no active saved model provider is configured"), snapshot, options.Model, "",
		)
	}
	if strings.TrimSpace(options.Endpoint) == "" {
		return options, wrapSessionRunnerModelCallError(
			errors.New("session runner chat endpoint is required"), snapshot, options.Model, "",
		)
	}
	if strings.TrimSpace(options.Model) == "" {
		return options, wrapSessionRunnerModelCallError(
			errors.New("session runner chat model is required"), snapshot, options.Model, "",
		)
	}
	return options, nil
}

// resolveSessionModelProfile is the single authority for every model-backed
// role in a session. It resolves the user's current saved selection at call
// time so agent execution, compact summaries, reviews, and resumes cannot
// drift to a stale process-level provider configuration.
func (s *Server) resolveSessionModelProfile(session sessionstore.Session, resolutionInput providers.ResolutionInput) (*providers.ModelProfile, string, error) {
	return s.resolveSessionModelProfileWithFallback(session, resolutionInput, "", "")
}

// resolveSessionModelProfileWithFallback keeps the user's live conversation
// selection authoritative for every task role. roleFallbackModel is used only
// when the conversation has no explicit selection (for example a configured
// reviewer model); a stale delegate assignment must never override a model the
// user selected while the task is running.
func (s *Server) resolveSessionModelProfileWithFallback(
	session sessionstore.Session,
	resolutionInput providers.ResolutionInput,
	roleFallbackModel string,
	roleFallbackSource string,
) (*providers.ModelProfile, string, error) {
	profile, source, _, err := s.resolveSessionModelProfileSnapshotWithFallback(
		session, resolutionInput, roleFallbackModel, roleFallbackSource,
	)
	return profile, source, err
}

func (s *Server) resolveSessionModelProfileSnapshotWithFallback(
	session sessionstore.Session,
	resolutionInput providers.ResolutionInput,
	roleFallbackModel string,
	roleFallbackSource string,
) (*providers.ModelProfile, string, sessionConversationModelSnapshot, error) {
	ctx := resolutionInput.Context
	if ctx == nil {
		ctx = context.Background()
	}
	resolutionInput.Context = ctx
	snapshot, err := s.sessionConversationModelSnapshotWithContext(ctx, session.ID)
	if err != nil {
		return nil, "conversation-model-override", snapshot, err
	}
	if snapshot.Selection != "" {
		profile, err := s.resolveUserModelSelectionProfileWithContext(ctx, snapshot.OwnerUserID, snapshot.Selection, resolutionInput)
		if err != nil {
			return nil, "conversation-model-override", snapshot, err
		}
		return &profile, "conversation-model-override", snapshot, nil
	}
	resolveOwnedModel := func(model, source string) (*providers.ModelProfile, string, error) {
		if strings.TrimSpace(snapshot.OwnerUserID) == "" {
			var found bool
			snapshot.OwnerUserID, found, err = s.workspaceStore.ProjectOwnerIDContext(ctx, resolutionInput.ProjectID)
			if err != nil {
				return nil, source, err
			}
			if !found || strings.TrimSpace(snapshot.OwnerUserID) == "" {
				return nil, source, fmt.Errorf("project %s not found", resolutionInput.ProjectID)
			}
		}
		profile, resolveErr := s.resolveUserModelSelectionProfileWithContext(ctx, snapshot.OwnerUserID, model, resolutionInput)
		if resolveErr != nil {
			return nil, source, resolveErr
		}
		return &profile, source, nil
	}
	if roleFallbackModel = strings.TrimSpace(roleFallbackModel); roleFallbackModel != "" {
		profile, source, err := resolveOwnedModel(roleFallbackModel, firstNonEmpty(strings.TrimSpace(roleFallbackSource), "role-model-fallback"))
		return profile, source, snapshot, err
	}
	delegatedModel := strings.TrimSpace(stringValue(session.Orchestration["delegate_model"]))
	if delegatedModel != "" {
		profile, source, err := resolveOwnedModel(delegatedModel, "workspace-delegated-model")
		return profile, source, snapshot, err
	}
	resolution, err := providers.ResolveRunnerModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, resolutionInput)
	if err != nil {
		return nil, "workspace-active-provider", snapshot, err
	}
	if resolution.Resolved && resolution.ModelProfile != nil {
		profile := *resolution.ModelProfile
		return &profile, "workspace-active-provider", snapshot, nil
	}
	return nil, "", snapshot, nil
}

func sessionRunnerProjectID(session sessionstore.Session) string {
	if session.Project == nil {
		return ""
	}
	return strings.TrimSpace(session.Project.ID)
}

func (s *Server) recordSessionRunnerModelAudit(sessionID string, attempt int, record providers.AuditRecord) {
	s.recordSessionRunnerModelAuditRole(sessionID, attempt, "agent", record)
}

func (s *Server) recordSessionRunnerModelAuditRole(sessionID string, attempt int, role string, record providers.AuditRecord) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	key := fmt.Sprintf("%s-%d-%d", runtimeKeyFromSessionID(sessionID), attempt, time.Now().UTC().UnixNano())
	value := map[string]any{
		"sessionId":               sessionID,
		"attempt":                 attempt,
		"providerId":              record.ProviderID,
		"providerType":            record.ProviderType,
		"protocol":                record.Protocol,
		"model":                   record.Model,
		"endpoint":                record.Endpoint,
		"requestId":               record.RequestID,
		"httpStatus":              record.HTTPStatus,
		"promptTokens":            record.PromptTokens,
		"completionTokens":        record.CompletionTokens,
		"requestedOutputTokens":   record.RequestedOutputTokens,
		"providerMaxOutputTokens": record.ProviderMaxOutputTokens,
		"outputTokenLimited":      record.OutputTokenLimited,
		"cacheReadTokens":         record.CacheReadTokens,
		"cacheWriteTokens":        record.CacheWriteTokens,
		"totalTokens":             record.TotalTokens,
		"durationMs":              record.DurationMs,
		"error":                   record.Error,
		"startedAt":               record.StartedAt,
		"finishedAt":              record.FinishedAt,
		"recordedAt":              time.Now().UTC(),
		"providerAuthority":       true,
		"runtimeRole":             strings.TrimSpace(role),
	}
	_, _ = s.runtimeStore.Set(sessionRunnerModelAuditRuntimeNamespace, key, value)
}

func (s *Server) finalizeClaimedSessionRunnerFailure(sessionID string, runnerID string, attempt int, claimToken string, message string, result *SessionRunnerCycleResult) error {
	finished, err := s.finishSessionRunner(map[string]any{
		"sessionId":       sessionID,
		"runnerId":        runnerID,
		"runnerAttempt":   attempt,
		"claimToken":      claimToken,
		"status":          "failed",
		"message":         message,
		"afterEventId":    int64(0),
		"clientMessageId": runnerCommandClientMessageID(runnerID, sessionID, attempt, claimToken, "chat-finish"),
	})
	if err != nil {
		return err
	}
	if result != nil {
		result.Status = "failed"
		if event, ok := finished["event"].(*eventjournal.Entry); ok && event != nil {
			result.FinishEventID = event.EventID
		}
	}
	return nil
}
