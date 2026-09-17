package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
)

type openAIResponsesStreamEvent struct {
	Type     string                    `json:"type"`
	Delta    string                    `json:"delta,omitempty"`
	ItemID   string                    `json:"item_id,omitempty"`
	Item     openAIResponsesOutputItem `json:"item,omitempty"`
	Response openAIResponsesResponse   `json:"response,omitempty"`
}

type openAIResponsesStreamCallIdentity struct {
	CallID string
	Name   string
}

type openAIResponsesStreamAccumulator struct {
	content          strings.Builder
	arguments        map[string]*strings.Builder
	callIdentity     map[string]openAIResponsesStreamCallIdentity
	semanticBytes    int64
	semanticLimit    int64
	requestID        string
	usage            providerTokenUsage
	completed        *agentruntime.ModelResponse
	emitted          bool
	toolBoundarySent bool
}

func (c *streamingRuntimeModelClient) completeOpenAIResponsesStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	maxAttempts := c.profile.Request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retryable, emitted, err := c.completeOpenAIResponsesStreamAttempt(ctx, request, attempt, emit)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryable || emitted || attempt >= maxAttempts {
			break
		}
		if err := waitProviderRetry(ctx, attempt, lastErr); err != nil {
			return agentruntime.ModelResponse{}, err
		}
	}
	return agentruntime.ModelResponse{}, lastErr
}

func (c *streamingRuntimeModelClient) completeOpenAIResponsesStreamAttempt(ctx context.Context, request agentruntime.ModelRequest, attempt int, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, bool, bool, error) {
	input, err := openAIResponsesInputFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, false, err
	}
	payload, err := json.Marshal(openAIResponsesRequest{
		Model: c.profile.Model, Input: input, Tools: openAIResponsesToolsFromRuntime(request.Tools),
		Metadata: request.Metadata, Store: false, Stream: true,
		MaxOutputTokens: request.MaxTokens, Temperature: request.Temperature,
		ToolChoice: openAIResponsesToolChoice(request.ToolChoice),
	})
	if err != nil {
		return agentruntime.ModelResponse{}, false, false, err
	}

	startedAt := time.Now().UTC()
	firstByteTimeout, idleTimeout := openAIChatStreamTimeouts(c.profile.Request.Timeout)
	requestCtx, cancelRequest := context.WithCancelCause(ctx)
	defer cancelRequest(nil)

	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.profile.Provider.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return agentruntime.ModelResponse{}, false, false, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("X-Request-ID", newProviderRequestID())
	c.applyHeaders(httpRequest, request.Headers)
	outboundRequestID := httpRequest.Header.Get("X-Request-ID")

	firstByteTimer := time.AfterFunc(firstByteTimeout, func() {
		cancelRequest(errOpenAIChatStreamFirstByteTimeout)
	})
	response, err := c.httpClient.Do(httpRequest)
	_ = firstByteTimer.Stop()
	if err != nil {
		err = openAIChatStreamRequestError(ctx, requestCtx, err)
		record := c.baseAuditRecord(attempt, c.profile.Provider.Endpoint, startedAt)
		record.RequestID = outboundRequestID
		record.FinishedAt = time.Now().UTC()
		record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Error = c.redactSensitiveText(err.Error())
		c.emitAudit(record)
		return agentruntime.ModelResponse{}, ctx.Err() == nil, false, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, readErr := readProviderStreamResponseBody(response.Body, c.profile.Request.MaxResponseBytes, idleTimeout, cancelRequest)
		record := c.baseAuditRecord(attempt, c.profile.Provider.Endpoint, startedAt)
		record.HTTPStatus = response.StatusCode
		record.RequestID = firstNonEmpty(requestIDFromHeaders(response.Header), outboundRequestID)
		record.FinishedAt = time.Now().UTC()
		record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		if readErr != nil {
			record.Error = c.redactSensitiveText(readErr.Error())
			c.emitAudit(record)
			return agentruntime.ModelResponse{}, false, false, readErr
		}
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
		return agentruntime.ModelResponse{}, retryable, false, responseErr
	}

	var decoded agentruntime.ModelResponse
	var requestID string
	var usage providerTokenUsage
	var emitted bool
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		idleReader := newOpenAIChatStreamIdleReader(response.Body, idleTimeout, cancelRequest)
		defer idleReader.Stop()
		decoded, requestID, usage, emitted, err = readOpenAIResponsesStream(
			idleReader, c.profile.Request.MaxResponseBytes, emit, c.openAIResponsesResponseDecoder,
		)
	} else {
		var body []byte
		body, err = readProviderStreamResponseBody(response.Body, c.profile.Request.MaxResponseBytes, idleTimeout, cancelRequest)
		if err == nil {
			decoded, requestID, usage, err = c.openAIResponsesResponseDecoder(body, response.Header)
			err = classifyDecodedOutputLimit(decoded, err)
			if IsContinuationSafeResponseTruncation(err) && decoded.Message.Content != "" {
				if emitErr := emit(agentruntime.ModelStreamEvent{ContentDelta: decoded.Message.Content}); emitErr != nil {
					err = emitErr
				} else {
					emitted = true
				}
			}
		}
	}
	record := c.baseAuditRecord(attempt, c.profile.Provider.Endpoint, startedAt)
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
		err = classifyProviderStreamInterruption(ctx, requestCtx, emitted, err)
		err = enrichOutputLimitFailure(err, c.effectiveRequestMaxTokens(request), &record, usage)
		record.Error = c.redactSensitiveText(err.Error())
		c.emitAudit(record)
		retryable := !emitted && ctx.Err() == nil &&
			!errors.Is(err, errProviderResponseTooLarge) &&
			(!errors.Is(err, errProviderResponseTruncated) || errors.Is(err, errProviderStreamIncomplete))
		return agentruntime.ModelResponse{}, retryable, emitted, err
	}
	c.emitAudit(record)
	return decoded, false, emitted, nil
}

func readOpenAIResponsesStream(reader io.Reader, limit int64, emit func(agentruntime.ModelStreamEvent) error, decode func([]byte, http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error)) (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	accumulator := openAIResponsesStreamAccumulator{
		arguments:     make(map[string]*strings.Builder),
		callIdentity:  make(map[string]openAIResponsesStreamCallIdentity),
		semanticLimit: limit,
	}
	eventLimit := limit
	buffered := bufio.NewReader(reader)
	var eventName string
	data := make([]string, 0, 1)
	var eventBytes int64

	dispatch := func() error {
		if len(data) == 0 {
			eventName = ""
			eventBytes = 0
			return nil
		}
		raw := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		name := strings.TrimSpace(eventName)
		eventName = ""
		eventBytes = 0
		if raw == "" || raw == "[DONE]" {
			return nil
		}
		var event openAIResponsesStreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("decode OpenAI Responses stream event: %w", err)
		}
		eventType := firstNonEmpty(strings.TrimSpace(event.Type), name)
		if accumulator.completed != nil {
			return errors.New("OpenAI Responses stream emitted an event after response.completed")
		}
		switch eventType {
		case "response.output_item.added":
			if !strings.EqualFold(strings.TrimSpace(event.Item.Type), "function_call") {
				return nil
			}
			itemID := strings.TrimSpace(event.Item.ID)
			callID := strings.TrimSpace(event.Item.CallID)
			name, err := normalizeModelToolName(event.Item.Name)
			if itemID == "" || callID == "" || err != nil {
				return errors.New("OpenAI Responses stream function call identity is incomplete")
			}
			identity := openAIResponsesStreamCallIdentity{CallID: callID, Name: name}
			if existing, found := accumulator.callIdentity[itemID]; found && existing != identity {
				return errors.New("OpenAI Responses stream function call identity changed")
			}
			if !accumulator.toolBoundarySent {
				if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventToolCallBoundary}); err != nil {
					return err
				}
				accumulator.toolBoundarySent = true
			}
			accumulator.callIdentity[itemID] = identity
		case "response.output_text.delta":
			if event.Delta == "" {
				return nil
			}
			recordProviderStreamProgress(reader)
			if err := accumulator.reserveSemanticBytes(int64(len(event.Delta))); err != nil {
				if errors.Is(err, errProviderSemanticSegmentBoundary) && accumulator.emitted &&
					len(accumulator.callIdentity) == 0 && len(accumulator.arguments) == 0 {
					err = newProviderStreamRecoveryCandidate(err)
				}
				return err
			}
			accumulator.content.WriteString(event.Delta)
			accumulator.emitted = true
			if err := emit(agentruntime.ModelStreamEvent{
				Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: event.Delta,
			}); err != nil {
				return err
			}
		case "response.function_call_arguments.delta":
			if event.Delta != "" {
				recordProviderStreamProgress(reader)
			}
			if err := accumulator.reserveSemanticBytes(int64(len(event.Delta))); err != nil {
				return err
			}
			key := strings.TrimSpace(event.ItemID)
			if key == "" {
				return errors.New("OpenAI Responses stream function arguments are missing item_id")
			}
			if _, found := accumulator.callIdentity[key]; !found {
				return errors.New("OpenAI Responses stream function arguments reference an unknown item")
			}
			builder := accumulator.arguments[key]
			if builder == nil {
				builder = &strings.Builder{}
				accumulator.arguments[key] = builder
			}
			builder.WriteString(event.Delta)
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if event.Delta != "" {
				if err := accumulator.reserveSemanticBytes(int64(len(event.Delta))); err != nil {
					return err
				}
				recordProviderStreamProgress(reader)
				if err := emit(agentruntime.ModelStreamEvent{ReasoningActive: true}); err != nil {
					return err
				}
			}
		case "response.completed":
			recordProviderStreamProgress(reader)
			if err := accumulator.validateCompletedToolCalls(event.Response); err != nil {
				return err
			}
			rawResponse, err := json.Marshal(event.Response)
			if err != nil {
				return err
			}
			response, requestID, usage, err := decode(rawResponse, nil)
			if err != nil {
				return err
			}
			if isProviderStreamTruncatedReason(response.StopReason) {
				return fmt.Errorf("%w: finish reason %s", errProviderResponseTruncated, response.StopReason)
			}
			if err := accumulator.observeCompletedSemanticBytes(openAIResponsesModelSemanticBytes(response)); err != nil {
				return err
			}
			accumulator.requestID = requestID
			accumulator.usage = usage
			accumulator.completed = &response
		case "response.incomplete":
			accumulator.requestID = event.Response.ID
			accumulator.usage = providerTokenUsage{PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens, TotalTokens: event.Response.Usage.TotalTokens}
			reason := event.Response.IncompleteDetails.Reason
			if limited := responseOutputLimit(reason, len(accumulator.callIdentity) > 0 || len(accumulator.arguments) > 0); limited != nil {
				return limited
			}
			return fmt.Errorf("OpenAI Responses incomplete response: %s", reason)
		case "response.failed", "error":
			return fmt.Errorf("OpenAI Responses stream terminated with %s", eventType)
		}
		return nil
	}

	for {
		line, readErr := readProviderSSELine(buffered, eventLimit)
		if line != "" {
			if int64(len(line)) > eventLimit-eventBytes {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted,
					fmt.Errorf("%w: SSE event exceeded %d bytes", errProviderResponseTooLarge, eventLimit)
			}
			eventBytes += int64(len(line))
		}
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, err
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case len(data) > 0 && !strings.HasPrefix(line, ":"):
			data = append(data, line)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				if accumulator.emitted && len(accumulator.callIdentity) == 0 && len(accumulator.arguments) == 0 && !errors.Is(readErr, errProviderResponseTooLarge) {
					readErr = newProviderStreamRecoveryCandidate(readErr)
				}
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, readErr
			}
			if err := dispatch(); err != nil {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, err
			}
			break
		}
	}
	if accumulator.completed == nil {
		streamErr := fmt.Errorf("%w: %w", errProviderResponseTruncated, errProviderStreamIncomplete)
		if accumulator.emitted && len(accumulator.callIdentity) == 0 && len(accumulator.arguments) == 0 {
			streamErr = newProviderStreamRecoveryCandidate(streamErr)
		}
		return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted,
			streamErr
	}
	return *accumulator.completed, accumulator.requestID, accumulator.usage, accumulator.emitted, nil
}

func (a *openAIResponsesStreamAccumulator) validateCompletedToolCalls(response openAIResponsesResponse) error {
	completed := make(map[string]openAIResponsesOutputItem)
	for _, item := range response.Output {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "function_call") {
			continue
		}
		itemID := strings.TrimSpace(item.ID)
		if itemID == "" {
			if len(a.callIdentity) != 0 || len(a.arguments) != 0 {
				return errors.New("OpenAI Responses completed function call is missing item identity")
			}
			continue
		}
		if _, exists := completed[itemID]; exists {
			return errors.New("OpenAI Responses completed function call identity is duplicated")
		}
		completed[itemID] = item
	}
	for itemID, identity := range a.callIdentity {
		item, found := completed[itemID]
		if !found || strings.TrimSpace(item.CallID) != identity.CallID {
			return errors.New("OpenAI Responses completed function call identity does not match streamed identity")
		}
		name, err := normalizeModelToolName(item.Name)
		if err != nil || name != identity.Name {
			return errors.New("OpenAI Responses completed function call name does not match streamed identity")
		}
	}
	for itemID, arguments := range a.arguments {
		item, found := completed[itemID]
		if !found || strings.TrimSpace(item.Arguments) != strings.TrimSpace(arguments.String()) {
			return errors.New("OpenAI Responses completed function arguments do not match streamed arguments")
		}
	}
	return nil
}

func (a *openAIResponsesStreamAccumulator) reserveSemanticBytes(count int64) error {
	if count <= 0 {
		return nil
	}
	if count > a.semanticLimit-a.semanticBytes {
		return newProviderSemanticSegmentBoundary(a.semanticLimit)
	}
	a.semanticBytes += count
	return nil
}

func (a *openAIResponsesStreamAccumulator) observeCompletedSemanticBytes(count int64) error {
	if count <= a.semanticBytes {
		return nil
	}
	if count > a.semanticLimit {
		return newProviderSemanticSegmentBoundary(a.semanticLimit)
	}
	a.semanticBytes = count
	return nil
}

func openAIResponsesModelSemanticBytes(response agentruntime.ModelResponse) int64 {
	count := int64(len(response.Message.Content) + len(response.Message.ReasoningContent))
	for _, call := range response.Message.ToolCalls {
		count += int64(len(strings.TrimSpace(call.ID)) + len(call.Name) + len(call.Arguments))
	}
	return count
}
