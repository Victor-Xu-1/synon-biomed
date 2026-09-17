package providers

import (
	"encoding/json"
	"errors"
	"fmt"

	"synon-go/internal/agentruntime"
)

// OutputLimitDetails is generation evidence, not a model capability claim.
// Zero RequestedTokens means that the provider chose its default. Usage may
// be absent; callers must never manufacture a budget from missing usage.
type OutputLimitDetails struct {
	RequestedTokens int    `json:"requestedTokens"`
	InputTokens     int    `json:"inputTokens"`
	OutputTokens    int    `json:"outputTokens"`
	RequestID       string `json:"requestId,omitempty"`
}

type outputLimitError struct {
	cause   error
	details OutputLimitDetails
}

func (c *runtimeModelClient) effectiveRequestMaxTokens(request agentruntime.ModelRequest) int {
	if c.profile.Provider.Protocol == ProtocolAnthropic {
		return effectiveMaxTokens(request.MaxTokens, defaultAnthropicMaxTokens)
	}
	return request.MaxTokens
}

func (e *outputLimitError) Error() string { return e.cause.Error() }
func (e *outputLimitError) Unwrap() error { return e.cause }

func ProviderOutputLimitDetails(err error) (OutputLimitDetails, bool) {
	var failure *outputLimitError
	if !IsProviderOutputTokenLimit(err) || !errors.As(err, &failure) {
		return OutputLimitDetails{}, false
	}
	return failure.details, true
}

func enrichOutputLimitFailure(err error, requested int, record *AuditRecord, usage providerTokenUsage) error {
	err = withOutputLimitDetails(err, requested, record.RequestID, usage)
	if details, ok := ProviderOutputLimitDetails(err); ok {
		record.OutputTokenLimited = true
		record.RequestedOutputTokens = details.RequestedTokens
		record.PromptTokens = details.InputTokens
		record.CompletionTokens = details.OutputTokens
	}
	return err
}

func withOutputLimitDetails(err error, requested int, requestID string, usage providerTokenUsage) error {
	if !IsProviderOutputTokenLimit(err) {
		return err
	}
	details, _ := ProviderOutputLimitDetails(err)
	details.RequestedTokens = max(details.RequestedTokens, requested)
	details.InputTokens = max(details.InputTokens, usage.PromptTokens)
	details.OutputTokens = max(details.OutputTokens, usage.CompletionTokens)
	if requestID != "" {
		details.RequestID = requestID
	}
	return &outputLimitError{cause: err, details: details}
}

func responseOutputLimit(reason string, hasTools bool) error {
	if !isProviderStreamTruncatedReason(reason) {
		return nil
	}
	if !hasTools {
		return newProviderContentResponseTruncation(reason)
	}
	return fmt.Errorf("%w: finish reason %s", errProviderResponseTruncated, reason)
}

// A server may answer a streaming request with JSON. Apply exactly the same
// stop-reason contract as SSE, retaining content only through its existing
// durable continuation path and never returning a partial action as success.
func classifyDecodedOutputLimit(response agentruntime.ModelResponse, err error) error {
	if limited := responseOutputLimit(response.StopReason, len(response.Message.ToolCalls) > 0); limited != nil {
		return limited
	}
	return err
}

// Native terminal chunks carry final usage together with the stop reason.
// The framing reader must not dispatch truncated tool content, but must still
// retain that usage so recovery is based on the response that actually failed.
func nativeTerminalUsage(raw []byte) providerTokenUsage {
	var event struct {
		Usage         anthropicStreamUsage `json:"usage"`
		UsageMetadata struct {
			Prompt int `json:"promptTokenCount"`
			Output int `json:"candidatesTokenCount"`
			Total  int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return providerTokenUsage{}
	}
	return providerTokenUsage{
		PromptTokens:     max(event.Usage.InputTokens, event.UsageMetadata.Prompt),
		CompletionTokens: max(event.Usage.OutputTokens, event.UsageMetadata.Output),
		TotalTokens:      event.UsageMetadata.Total,
	}
}
