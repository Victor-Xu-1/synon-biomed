package server

import (
	"context"
	"encoding/json"
	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"
)

// Observation only: one delegate call, no generated prose, no stream writes,
// no tool decisions. Publication remains owned by the existing runner boundary.
type sessionRunnerCommunicationObserver struct {
	delegate         agentruntime.ModelClient
	publicationBytes func() int
	audit            func(map[string]any)
	pending          map[string]any
	before           int
}

func (observer *sessionRunnerCommunicationObserver) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return observer.delegate.Complete(ctx, request)
}

func (observer *sessionRunnerCommunicationObserver) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	observer.record("private_repair")
	observer.before = observer.publicationBytes()
	streamBytes, private := 0, false
	streaming, ok := observer.delegate.(agentruntime.StreamingModelClient)
	if !ok {
		return observer.delegate.Complete(ctx, request)
	}
	response, err := streaming.CompleteStream(ctx, request, func(event agentruntime.ModelStreamEvent) error {
		streamBytes += len(event.ContentDelta)
		private = private || event.ReasoningActive || event.Kind == agentruntime.ModelStreamEventPrivateReasoning
		if emit != nil {
			return emit(event)
		}
		return nil
	})
	known := map[string]bool{}
	for _, name := range harnesscontract.RootModelTools() {
		known[name] = true
	}
	for _, tool := range request.Tools {
		known[tool.Name] = true
	}
	names := make([]string, 0, len(response.Message.ToolCalls))
	for _, call := range response.Message.ToolCalls {
		if known[call.Name] {
			names = append(names, call.Name)
		} else {
			names = append(names, "unadvertised")
		}
	}
	choice := mapValue(request.ToolChoice)
	structuredBytes := 0
	for _, call := range response.Message.ToolCalls {
		var args map[string]json.RawMessage
		var progress string
		if json.Unmarshal(call.Arguments, &args) == nil && json.Unmarshal(args[runnerPublicProgressField], &progress) == nil {
			structuredBytes += len(progress)
		}
	}
	observer.pending = map[string]any{
		"structured_progress_bytes": structuredBytes,
		"native_text_bytes":         len(response.Message.Content), "native_stream_bytes": streamBytes,
		"safe_native_text_bytes":     len(sessionRunnerPublicProgressNarration(response.Message.Content)),
		"private_reasoning_observed": private, "tool_names": names,
		"required_tool_choice": agentruntime.InitialToolChoiceRequiresCall(request.ToolChoice),
		"required_tool_name":   firstNonEmpty(stringValue(choice["name"]), stringValue(mapValue(choice["function"])["name"])),
	}
	addSessionRunnerNarrationCounters(observer.pending, "native_", response.Message.Content)
	if err != nil {
		observer.record("provider_interrupted")
	}
	return response, err
}

func (observer *sessionRunnerCommunicationObserver) recordBoundary(tools bool) {
	if tools {
		observer.record("tool_boundary")
	} else {
		observer.record("final_candidate")
	}
}

func (observer *sessionRunnerCommunicationObserver) record(decision string) {
	if observer == nil || observer.pending == nil {
		return
	}
	observer.pending["decision"] = decision
	observer.pending["published_bytes"] = observer.publicationBytes() - observer.before
	if decision == "private_repair" && observer.publicationBytes() > observer.before {
		observer.pending["decision"] = "public_preamble_private_tool_repair"
	}
	if observer.audit != nil {
		observer.audit(observer.pending)
	}
	observer.pending = nil
}
