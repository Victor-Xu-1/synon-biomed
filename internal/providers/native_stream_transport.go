package providers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
)

type nativeStreamReader func(*runtimeModelClient, http.Response, func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, string, providerTokenUsage, bool, error)
type nativeJSONDecoder func([]byte, http.Header) (agentruntime.ModelResponse, string, providerTokenUsage, error)

func (c *streamingRuntimeModelClient) completeNativeStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error, endpoint string, payload []byte, decode nativeJSONDecoder, readStream nativeStreamReader) (agentruntime.ModelResponse, error) {
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	maxAttempts := c.profile.Request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retryable, emitted, err := c.completeNativeStreamAttempt(ctx, request, emit, attempt, endpoint, payload, decode, readStream)
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

func (c *streamingRuntimeModelClient) completeNativeStreamAttempt(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error, attempt int, endpoint string, payload []byte, decode nativeJSONDecoder, readStream nativeStreamReader) (agentruntime.ModelResponse, bool, bool, error) {
	startedAt := time.Now().UTC()
	firstByteTimeout, idleTimeout := openAIChatStreamTimeouts(c.profile.Request.Timeout)
	requestCtx, cancelRequest := context.WithCancelCause(ctx)
	defer cancelRequest(nil)

	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		record := c.baseAuditRecord(attempt, endpoint, startedAt)
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
		record := c.baseAuditRecord(attempt, endpoint, startedAt)
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
		response.Body = &providerStreamIdleReadCloser{reader: idleReader, closer: response.Body}
		decoded, requestID, usage, emitted, err = readStream(c.runtimeModelClient, *response, emit)
	} else {
		var body []byte
		body, err = readProviderStreamResponseBody(response.Body, c.profile.Request.MaxResponseBytes, idleTimeout, cancelRequest)
		if err == nil {
			decoded, requestID, usage, err = decode(body, response.Header)
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
