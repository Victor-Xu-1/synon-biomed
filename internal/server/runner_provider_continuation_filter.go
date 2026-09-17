package server

import (
	"context"
	"errors"
	"strings"
	"sync"

	"synon-go/internal/agentruntime"
)

type sessionRunnerProviderNoProgressInterruption struct{}

func (sessionRunnerProviderNoProgressInterruption) Error() string {
	return "provider continuation produced no new durable semantic content"
}

type sessionRunnerExactPrefixFilter struct {
	prefix  string
	matched int
	decided bool
	pending strings.Builder
}

func (filter *sessionRunnerExactPrefixFilter) delta(value string) string {
	if filter == nil || value == "" || filter.prefix == "" || filter.decided {
		return value
	}
	remaining := filter.prefix[filter.matched:]
	common := 0
	for common < len(value) && common < len(remaining) && value[common] == remaining[common] {
		common++
	}
	if common > 0 {
		filter.pending.WriteString(value[:common])
		filter.matched += common
	}
	if filter.matched == len(filter.prefix) {
		filter.decided = true
		filter.pending.Reset()
		return value[common:]
	}
	if common < len(value) {
		filter.decided = true
		buffered := filter.pending.String() + value[common:]
		filter.pending.Reset()
		return buffered
	}
	return ""
}

func stripSessionRunnerExactPrefixReplay(prefix, content string) string {
	if prefix == "" || content == "" {
		return content
	}
	if strings.HasPrefix(content, prefix) {
		return content[len(prefix):]
	}
	if strings.HasPrefix(prefix, content) {
		return ""
	}
	return content
}

type sessionRunnerContinuationModelClient struct {
	delegate      agentruntime.ModelClient
	prefix        string
	privatePrefix string
	mu            sync.Mutex
	calls         int
}

func (client *sessionRunnerContinuationModelClient) Complete(context.Context, agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{}, errors.New("provider continuation requires a streaming model client")
}

func (client *sessionRunnerContinuationModelClient) CompleteStream(
	ctx context.Context,
	request agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	if client == nil || client.delegate == nil {
		return agentruntime.ModelResponse{}, errors.New("provider continuation model client is unavailable")
	}
	streaming, ok := client.delegate.(agentruntime.StreamingModelClient)
	if !ok {
		return agentruntime.ModelResponse{}, errors.New("provider continuation cannot safely resume a non-streaming response")
	}
	client.mu.Lock()
	client.calls++
	call := client.calls
	client.mu.Unlock()
	if call != 1 || client.prefix == "" {
		return streaming.CompleteStream(ctx, request, emit)
	}
	filter := &sessionRunnerExactPrefixFilter{prefix: client.prefix}
	response, err := streaming.CompleteStream(ctx, request, func(event agentruntime.ModelStreamEvent) error {
		if (event.Kind != "" && event.Kind != agentruntime.ModelStreamEventContentDelta) ||
			(event.Kind == "" && event.ContentDelta == "") {
			if emit == nil {
				return nil
			}
			return emit(event)
		}
		event.ContentDelta = filter.delta(event.ContentDelta)
		if event.ContentDelta == "" || emit == nil {
			return nil
		}
		return emit(event)
	})
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	response.Message.Content = stripSessionRunnerExactPrefixReplay(client.prefix, response.Message.Content)
	if response.Message.Content == "" && len(response.Message.ToolCalls) == 0 {
		return agentruntime.ModelResponse{}, sessionRunnerProviderNoProgressInterruption{}
	}
	// Previously interrupted candidate bytes must reach the same completion
	// gates as the new suffix; they have never been published as progress.
	response.Message.Content = client.privatePrefix + response.Message.Content
	return response, nil
}
