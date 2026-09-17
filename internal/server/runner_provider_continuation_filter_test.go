package server

import (
	"context"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestSessionRunnerContinuationPreservesToolBoundaryAfterPrefixFiltering(t *testing.T) {
	delegate := responseLanguageFixtureModel{
		events: []agentruntime.ModelStreamEvent{
			{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: "saved prefix"},
			{Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: " and new evidence"},
			{Kind: agentruntime.ModelStreamEventToolCallBoundary},
		},
		response: agentruntime.ModelResponse{Message: agentruntime.Message{
			Content:   "saved prefix and new evidence",
			ToolCalls: []agentruntime.ToolCall{{ID: "call-1", Name: "web_search"}},
		}},
	}
	client := &sessionRunnerContinuationModelClient{delegate: delegate, prefix: "saved prefix"}
	events := make([]agentruntime.ModelStreamEvent, 0, 2)
	response, err := client.CompleteStream(
		context.Background(),
		agentruntime.ModelRequest{},
		func(event agentruntime.ModelStreamEvent) error {
			events = append(events, event)
			return nil
		},
	)
	if err != nil || response.Message.Content != " and new evidence" || len(events) != 2 ||
		events[0].Kind != agentruntime.ModelStreamEventContentDelta ||
		events[0].ContentDelta != " and new evidence" ||
		events[1].Kind != agentruntime.ModelStreamEventToolCallBoundary {
		t.Fatalf("response=%#v events=%#v err=%v", response, events, err)
	}
}

func TestSessionRunnerContinuationPreservesLegacyPrivateReasoningSignal(t *testing.T) {
	client := &sessionRunnerContinuationModelClient{
		delegate: responseLanguageFixtureModel{
			events:   []agentruntime.ModelStreamEvent{{ReasoningActive: true}},
			response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: "done"}},
		},
		prefix: "saved prefix",
	}
	var events []agentruntime.ModelStreamEvent
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(event agentruntime.ModelStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || len(events) != 1 || !events[0].ReasoningActive || events[0].ContentDelta != "" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSessionRunnerContinuationReturnsPrivatePrefixForCompletionValidation(t *testing.T) {
	client := &sessionRunnerContinuationModelClient{
		delegate: responseLanguageFixtureModel{
			response: agentruntime.ModelResponse{Message: agentruntime.Message{Content: "private draft continued"}},
		},
		prefix: "private draft", privatePrefix: "private draft",
	}
	response, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, nil)
	if err != nil || response.Message.Content != "private draft continued" {
		t.Fatalf("completion validation lost private prefix: %#v err=%v", response, err)
	}
}
