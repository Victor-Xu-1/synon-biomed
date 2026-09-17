package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"synon-go/internal/agentruntime"
)

const (
	// This window bounds lack of progress. Productive streams inherit only
	// caller cancellation/deadlines and may span any number of idle windows.
	defaultOpenAIChatStreamTimeout = 2 * time.Minute
)

var (
	errOpenAIChatStreamFirstByteTimeout = errors.New("provider stream response headers timeout")
	errOpenAIChatStreamIdleTimeout      = errors.New("provider stream idle timeout")
	errOpenAIChatStreamRead             = errors.New("provider stream transport read failed")
)

type streamingRuntimeModelClient struct {
	*runtimeModelClient
}

func (c *streamingRuntimeModelClient) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	request = c.applyProfileGenerationControls(request)
	if err := rejectUnencodedMediaParts(c.profile.Provider.Protocol, request); err != nil {
		return agentruntime.ModelResponse{}, err
	}
	return c.adapter.completeStream(ctx, c, request, emit)
}

func (c *streamingRuntimeModelClient) completeOpenAIChatStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	maxAttempts := c.profile.Request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		response, retryable, emitted, err := c.completeOpenAIChatStreamAttempt(ctx, request, attempt, emit)
		if err == nil {
			return c.recoverOpenAIChatToolArguments(ctx, request, response)
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

type openAIChatStreamRequest struct {
	Model               string                  `json:"model"`
	Messages            []openAIMessage         `json:"messages"`
	Tools               []openAITool            `json:"tools,omitempty"`
	Temperature         float64                 `json:"temperature,omitempty"`
	MaxTokens           int                     `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                     `json:"max_completion_tokens,omitempty"`
	ToolChoice          any                     `json:"tool_choice,omitempty"`
	Metadata            map[string]any          `json:"metadata,omitempty"`
	Thinking            *openAIThinking         `json:"thinking,omitempty"`
	Stream              bool                    `json:"stream"`
	StreamOptions       openAIChatStreamOptions `json:"stream_options"`
}

type openAIChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatStreamChunk struct {
	ID      string `json:"id,omitempty"`
	Choices []struct {
		Index        *int   `json:"index,omitempty"`
		FinishReason string `json:"finish_reason,omitempty"`
		Delta        struct {
			Role             string `json:"role,omitempty"`
			Content          string `json:"content,omitempty"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id,omitempty"`
				Type     string `json:"type,omitempty"`
				Function struct {
					Name      string `json:"name,omitempty"`
					Arguments string `json:"arguments,omitempty"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"delta"`
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

type openAIChatStreamToolCall struct {
	ID                string
	Name              string
	Arguments         strings.Builder
	ArgumentParts     []string
	ArgumentFragments int
}

type openAIChatStreamAccumulator struct {
	role             string
	roleBound        bool
	content          strings.Builder
	reasoningContent strings.Builder
	toolCalls        map[int]*openAIChatStreamToolCall
	semanticBytes    int64
	semanticLimit    int64
	requestID        string
	finishReason     string
	usage            providerTokenUsage
	emitted          bool
	toolBoundarySent bool
}

func (c *streamingRuntimeModelClient) completeOpenAIChatStreamAttempt(ctx context.Context, request agentruntime.ModelRequest, attempt int, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, bool, bool, error) {
	messages, err := openAIMessagesFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, false, false, err
	}
	maxTokens, maxCompletionTokens := openAIChatTokenBudget(c.profile, request.MaxTokens)
	payload, err := json.Marshal(openAIChatStreamRequest{
		Model: c.profile.Model, Messages: messages,
		Tools: openAIToolsFromRuntime(request.Tools), Temperature: *modelTemperature(request),
		MaxTokens: maxTokens, MaxCompletionTokens: maxCompletionTokens,
		ToolChoice: openAIChatToolChoiceForProfile(c.profile, request.ToolChoice), Metadata: request.Metadata, Stream: true,
		Thinking:      openAIThinkingForProfile(c.profile, request.ReasoningMode),
		StreamOptions: openAIChatStreamOptions{IncludeUsage: true},
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
		decoded, requestID, usage, emitted, err = readOpenAIChatStream(
			idleReader, c.profile.Request.MaxResponseBytes, emit,
		)
	} else {
		var body []byte
		body, err = readProviderStreamResponseBody(response.Body, c.profile.Request.MaxResponseBytes, idleTimeout, cancelRequest)
		if err == nil {
			decoded, requestID, usage, err = c.openAIResponseDecoder(body, response.Header)
			if IsContinuationSafeResponseTruncation(err) && strings.TrimSpace(decoded.Message.Content) != "" {
				if emitErr := emit(agentruntime.ModelStreamEvent{
					Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: decoded.Message.Content,
				}); emitErr != nil {
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

func openAIChatStreamTimeouts(configured time.Duration) (firstByte, idle time.Duration) {
	if configured <= 0 {
		configured = defaultOpenAIChatStreamTimeout
	}
	return configured, configured
}

func openAIChatStreamRequestError(parentCtx, requestCtx context.Context, fallback error) error {
	if err := parentCtx.Err(); err != nil {
		return err
	}
	if cause := context.Cause(requestCtx); cause != nil {
		return cause
	}
	return fallback
}

func isRecoverableOpenAIChatStreamFailure(err error) bool {
	if errors.Is(err, errProviderResponseTooLarge) ||
		(errors.Is(err, errProviderResponseTruncated) && !errors.Is(err, errProviderStreamIncomplete)) {
		return false
	}
	return errors.Is(err, errProviderStreamIncomplete) ||
		errors.Is(err, errOpenAIChatStreamRead) ||
		errors.Is(err, errOpenAIChatStreamIdleTimeout)
}

type openAIChatStreamIdleReader struct {
	reader           io.Reader
	semanticProgress bool
	timeout          time.Duration
	cancel           context.CancelCauseFunc
	mu               sync.Mutex
	timer            *time.Timer
	generation       uint64
	stopped          bool
}

func newOpenAIChatStreamIdleReader(reader io.Reader, timeout time.Duration, cancel context.CancelCauseFunc) *openAIChatStreamIdleReader {
	idleReader := &openAIChatStreamIdleReader{reader: reader, timeout: timeout, cancel: cancel, semanticProgress: true}
	idleReader.reset()
	return idleReader
}

func (reader *openAIChatStreamIdleReader) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	if read > 0 && !reader.semanticProgress {
		reader.reset()
	}
	return read, err
}

func (reader *openAIChatStreamIdleReader) Stop() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.stopped {
		return
	}
	reader.stopped = true
	reader.generation++
	if reader.timer != nil {
		_ = reader.timer.Stop()
	}
}

func (reader *openAIChatStreamIdleReader) reset() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.stopped {
		return
	}
	reader.generation++
	generation := reader.generation
	if reader.timer != nil {
		_ = reader.timer.Stop()
	}
	reader.timer = time.AfterFunc(reader.timeout, func() {
		reader.mu.Lock()
		if reader.stopped || reader.generation != generation {
			reader.mu.Unlock()
			return
		}
		reader.stopped = true
		reader.mu.Unlock()
		reader.cancel(errOpenAIChatStreamIdleTimeout)
	})
}

func readOpenAIChatStream(reader io.Reader, limit int64, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	accumulator := openAIChatStreamAccumulator{
		role: "assistant", toolCalls: map[int]*openAIChatStreamToolCall{}, semanticLimit: limit,
	}
	eventLimit := limit
	buffered := bufio.NewReader(reader)
	data := make([]string, 0, 1)
	var eventBytes int64
	dispatch := func() (bool, error) {
		if len(data) == 0 {
			eventBytes = 0
			return false, nil
		}
		raw := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		eventBytes = 0
		if raw == "" {
			return false, nil
		}
		if raw == "[DONE]" {
			return true, nil
		}
		var chunk openAIChatStreamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return false, fmt.Errorf("decode OpenAI chat stream event: %w", err)
		}
		if requestID := strings.TrimSpace(chunk.ID); requestID != "" {
			if accumulator.requestID != "" && accumulator.requestID != requestID {
				return false, errors.New("OpenAI chat stream changed its request identity")
			}
			accumulator.requestID = requestID
		}
		if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 || chunk.Usage.TotalTokens != 0 {
			accumulator.usage = providerTokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				CacheReadTokens:  chunk.Usage.PromptTokensDetails.CachedTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		choiceIndexes := make(map[int]struct{}, len(chunk.Choices))
		for _, choice := range chunk.Choices {
			choiceIndex := 0
			if choice.Index != nil {
				choiceIndex = *choice.Index
			} else if len(chunk.Choices) != 1 {
				return false, errors.New("OpenAI chat stream choice is missing an index")
			}
			if choiceIndex != 0 {
				return false, errors.New("OpenAI chat stream returned an unexpected choice index")
			}
			if _, exists := choiceIndexes[choiceIndex]; exists {
				return false, errors.New("OpenAI chat stream repeated a choice index in one event")
			}
			choiceIndexes[choiceIndex] = struct{}{}
			hasSemanticDelta := strings.TrimSpace(choice.Delta.Role) != "" || choice.Delta.Content != "" ||
				choice.Delta.ReasoningContent != "" || len(choice.Delta.ToolCalls) != 0
			if accumulator.finishReason != "" && hasSemanticDelta {
				return false, errors.New("OpenAI chat stream emitted output after its terminal reason")
			}
			semanticDeltaBytes := int64(len(choice.Delta.Content) + len(choice.Delta.ReasoningContent))
			for _, delta := range choice.Delta.ToolCalls {
				call := accumulator.toolCalls[0]
				if delta.Index != nil {
					call = accumulator.toolCalls[*delta.Index]
				}
				currentID, currentName, currentArguments := "", "", ""
				if call != nil {
					currentID, currentName, currentArguments = call.ID, call.Name, call.Arguments.String()
				}
				idGrowth := len(strings.TrimSpace(delta.ID))
				if currentID != "" && strings.TrimSpace(delta.ID) == currentID {
					idGrowth = 0
				}
				_, nameGrowth := mergeOpenAIChatStreamFragment(currentName, delta.Function.Name)
				_, argumentGrowth := mergeOpenAIChatStreamFragment(currentArguments, delta.Function.Arguments)
				semanticDeltaBytes += int64(idGrowth + nameGrowth + argumentGrowth)
			}
			if err := accumulator.reserveSemanticBytes(semanticDeltaBytes); err != nil {
				role := strings.TrimSpace(choice.Delta.Role)
				if errors.Is(err, errProviderSemanticSegmentBoundary) &&
					strings.TrimSpace(choice.FinishReason) == "" &&
					(role == "" || role == "assistant") &&
					accumulator.emitted && len(accumulator.toolCalls) == 0 && len(choice.Delta.ToolCalls) == 0 {
					err = newProviderStreamRecoveryCandidate(err)
				}
				return false, err
			}
			if semanticDeltaBytes > 0 || choice.FinishReason != "" {
				recordProviderStreamProgress(reader)
			}
			if finishReason := strings.ToLower(strings.TrimSpace(choice.FinishReason)); finishReason != "" {
				if accumulator.finishReason != "" && accumulator.finishReason != finishReason {
					return false, errors.New("OpenAI chat stream changed its terminal reason")
				}
				switch finishReason {
				case "stop", "tool_calls", "function_call", "length", "content_filter":
					accumulator.finishReason = finishReason
				default:
					return false, errors.New("OpenAI chat stream returned an unsupported finish reason")
				}
			}
			if role := strings.TrimSpace(choice.Delta.Role); role != "" {
				if accumulator.roleBound && accumulator.role != role {
					return false, errors.New("OpenAI chat stream changed its response role")
				}
				accumulator.role = role
				accumulator.roleBound = true
			}
			if choice.Delta.Content != "" {
				accumulator.content.WriteString(choice.Delta.Content)
				accumulator.emitted = true
				if err := emit(agentruntime.ModelStreamEvent{
					Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: choice.Delta.Content,
				}); err != nil {
					return false, err
				}
			}
			if choice.Delta.ReasoningContent != "" {
				accumulator.reasoningContent.WriteString(choice.Delta.ReasoningContent)
				if err := emit(agentruntime.ModelStreamEvent{
					Kind: agentruntime.ModelStreamEventPrivateReasoning, ReasoningActive: true,
				}); err != nil {
					return false, err
				}
			}
			if len(choice.Delta.ToolCalls) > 0 && !accumulator.toolBoundarySent {
				if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventToolCallBoundary}); err != nil {
					return false, err
				}
				accumulator.toolBoundarySent = true
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index == nil {
					return false, errors.New("OpenAI chat stream function call is missing an index")
				}
				callIndex := *delta.Index
				if callIndex < 0 {
					return false, errors.New("OpenAI chat stream function call has a negative index")
				}
				if delta.Type != "" && !strings.EqualFold(strings.TrimSpace(delta.Type), "function") {
					return false, fmt.Errorf("OpenAI chat stream function call %d has unsupported type", callIndex)
				}
				call := accumulator.toolCalls[callIndex]
				if call == nil {
					call = &openAIChatStreamToolCall{}
					accumulator.toolCalls[callIndex] = call
				}
				if id := strings.TrimSpace(delta.ID); id != "" {
					if call.ID != "" && call.ID != id {
						return false, fmt.Errorf("OpenAI chat stream function call %d changed id", callIndex)
					}
					call.ID = id
				}
				if delta.Function.Name != "" {
					call.Name, _ = mergeOpenAIChatStreamFragment(call.Name, delta.Function.Name)
				}
				if delta.Function.Arguments != "" {
					call.ArgumentFragments++
					call.ArgumentParts = append(call.ArgumentParts, delta.Function.Arguments)
					merged, _ := mergeOpenAIChatStreamFragment(call.Arguments.String(), delta.Function.Arguments)
					call.Arguments.Reset()
					call.Arguments.WriteString(merged)
				}
			}
		}
		return false, nil
	}

	for {
		line, readErr := readOpenAIChatStreamLine(buffered, eventLimit)
		switch {
		case line == "":
			done, err := dispatch()
			if err != nil {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, err
			}
			if done {
				return accumulator.response()
			}
		case strings.HasPrefix(line, "data:"):
			dataLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if int64(len(dataLine)) > eventLimit-eventBytes {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted,
					fmt.Errorf("%w: SSE event exceeded %d bytes", errProviderResponseTooLarge, eventLimit)
			}
			eventBytes += int64(len(dataLine))
			data = append(data, dataLine)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				if errors.Is(readErr, errProviderResponseTooLarge) {
					return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, readErr
				}
				streamErr := fmt.Errorf("%w: %w", errOpenAIChatStreamRead, readErr)
				if accumulator.emitted && len(accumulator.toolCalls) == 0 {
					streamErr = newProviderStreamRecoveryCandidate(streamErr)
				}
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, streamErr
			}
			done, err := dispatch()
			if err != nil {
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted, err
			}
			if done {
				return accumulator.response()
			}
			if accumulator.finishReason == "" {
				streamErr := fmt.Errorf("%w: %w", errProviderResponseTruncated, errProviderStreamIncomplete)
				if accumulator.emitted && len(accumulator.toolCalls) == 0 {
					streamErr = newProviderStreamRecoveryCandidate(streamErr)
				}
				return agentruntime.ModelResponse{}, accumulator.requestID, accumulator.usage, accumulator.emitted,
					streamErr
			}
			return accumulator.response()
		}
	}
}

// OpenAI-compatible providers use both true deltas and cumulative snapshots for
// streamed function fields. Exact prefixes are unambiguous snapshots. Short
// token-sized fragments are always true deltas: suffix-overlap deduplication on
// those fragments can silently delete matching quote, brace, or text bytes.
// A legacy suffix snapshot is accepted only when both its overlap and total
// length are large enough to distinguish it from ordinary token streaming.
func mergeOpenAIChatStreamFragment(current, incoming string) (string, int) {
	if incoming == "" {
		return current, 0
	}
	if current == "" {
		return incoming, len(incoming)
	}
	if incoming == current || strings.HasPrefix(current, incoming) {
		return current, 0
	}
	if strings.HasPrefix(incoming, current) {
		return incoming, len(incoming) - len(current)
	}
	const minimumLegacySuffixSnapshotBytes = 8
	const minimumLegacySuffixOverlapBytes = 3
	if len(incoming) < minimumLegacySuffixSnapshotBytes {
		return current + incoming, len(incoming)
	}
	for overlap := minOpenAIChatStreamInt(len(current), len(incoming)); overlap >= minimumLegacySuffixOverlapBytes; overlap-- {
		if current[len(current)-overlap:] == incoming[:overlap] {
			return current + incoming[overlap:], len(incoming) - overlap
		}
	}
	return current + incoming, len(incoming)
}

// Keep malformed-provider diagnostics useful without persisting model-supplied
// argument values. The digest correlates repeated failures while structural
// classes reveal whether a gateway returned a truncated object or another JSON
// shape.
func openAIChatStreamArgumentsDiagnostic(raw string, fragments int) string {
	trimmed := strings.TrimSpace(raw)
	start, end := "empty", "empty"
	if trimmed != "" {
		start = openAIChatStreamArgumentBoundaryClass(trimmed[0])
		end = openAIChatStreamArgumentBoundaryClass(trimmed[len(trimmed)-1])
	}
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("bytes=%d fragments=%d sha256=%x start=%s end=%s valid_json=%t %s",
		len(raw), fragments, digest[:8], start, end, json.Valid([]byte(trimmed)),
		providerToolArgumentsSyntaxDiagnostic(trimmed))
}

func openAIChatStreamArgumentBoundaryClass(value byte) string {
	switch value {
	case '{':
		return "object_open"
	case '}':
		return "object_close"
	case '[':
		return "array_open"
	case ']':
		return "array_close"
	case '"':
		return "quote"
	case ':':
		return "colon"
	case ',':
		return "comma"
	default:
		return "other"
	}
}

func minOpenAIChatStreamInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxOpenAIChatStreamInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func readOpenAIChatStreamLine(reader *bufio.Reader, limit int64) (string, error) {
	var line bytes.Buffer
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if int64(len(fragment)) > limit-int64(line.Len()) {
			return "", fmt.Errorf("%w: SSE event line exceeded %d bytes", errProviderResponseTooLarge, limit)
		}
		_, _ = line.Write(fragment)
		if err != nil {
			return line.String(), err
		}
		if !isPrefix {
			return line.String(), nil
		}
	}
}

func (a *openAIChatStreamAccumulator) reserveSemanticBytes(count int64) error {
	if count <= 0 {
		return nil
	}
	if count > a.semanticLimit-a.semanticBytes {
		return newProviderSemanticSegmentBoundary(a.semanticLimit)
	}
	a.semanticBytes += count
	return nil
}

func (a *openAIChatStreamAccumulator) response() (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	if a.usage.TotalTokens == 0 {
		a.usage.TotalTokens = a.usage.PromptTokens + a.usage.CompletionTokens
	}
	if isTokenLimitFinishReason(a.finishReason) {
		if len(a.toolCalls) == 0 {
			return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
				newProviderContentResponseTruncation(a.finishReason)
		}
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
			fmt.Errorf("%w: finish reason %s", errProviderResponseTruncated, a.finishReason)
	}
	if a.finishReason == "content_filter" {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
			errors.New("OpenAI chat stream response was blocked by the provider content filter")
	}
	indexes := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]agentruntime.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := a.toolCalls[index]
		arguments := strings.TrimSpace(call.Arguments.String())
		protocolDiagnostic := ""
		if arguments == "" {
			arguments = "{}"
		} else if normalized, ok := resolveOpenAIChatStreamArguments(call.ArgumentParts, arguments); ok {
			arguments = normalized
		} else {
			protocolDiagnostic = fmt.Sprintf(
				"OpenAI chat stream function call %d has invalid JSON object arguments (%s)",
				index, openAIChatStreamArgumentsDiagnostic(arguments, call.ArgumentFragments),
			)
			arguments = "{}"
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
				newRetryableModelProtocolError(
					fmt.Errorf("OpenAI chat stream function call %d is missing id or name", index),
				)
		}
		name, err := normalizeModelToolName(call.Name)
		if err != nil {
			return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
				newRetryableModelProtocolError(err)
		}
		calls = append(calls, agentruntime.ToolCall{
			ID: callID, Name: name, Arguments: json.RawMessage(arguments),
			ProviderProtocolDiagnostic: protocolDiagnostic,
		})
	}
	content := a.content.String()
	if strings.TrimSpace(content) == "" && len(calls) == 0 {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted,
			newProviderEmptyResponseError("OpenAI chat stream response has no output")
	}
	message, err := normalizeOpenAITextControlEnvelope(agentruntime.Message{
		Role:             firstNonEmpty(a.role, "assistant"),
		Content:          content,
		ReasoningContent: a.reasoningContent.String(),
		ToolCalls:        calls,
	})
	if err != nil {
		return agentruntime.ModelResponse{}, a.requestID, a.usage, a.emitted, err
	}
	return agentruntime.ModelResponse{Message: message, StopReason: a.finishReason}, a.requestID, a.usage, a.emitted, nil
}
