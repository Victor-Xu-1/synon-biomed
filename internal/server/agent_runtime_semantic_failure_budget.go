package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/failurecontract"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const agentRuntimeSemanticFailureNamespace = "agent-runtime-semantic-failures"

type durableSemanticFailureState struct {
	Attempts          int
	Fingerprint       string
	ExecutionIdentity string
}

// durableSemanticFailureStateForTarget carries executed failure identity across
// runner leases. It records diagnostics for recovery but never converts a
// changing correction sequence into a call-count gate.
func durableSemanticFailureStateForTarget(
	projected []transcriptstore.RunnerReplayEvent,
	toolName string,
	arguments json.RawMessage,
) durableSemanticFailureState {
	normalizedTool := strings.ToLower(strings.TrimSpace(toolName))
	if normalizedTool == "" {
		return durableSemanticFailureState{}
	}
	target := agentruntime.SemanticFailureTarget(normalizedTool, arguments)
	state := durableSemanticFailureState{}
	for _, projectedEvent := range projected {
		if projectedEvent.Event.Type != "runner_checkpoint" || len(projectedEvent.ResolvedPayloadJSON) == 0 {
			continue
		}
		message := map[string]any{}
		if err := json.Unmarshal(projectedEvent.ResolvedPayloadJSON, &message); err != nil {
			continue
		}
		phase := strings.ToLower(strings.TrimSpace(stringValue(message["toolPhase"])))
		status := strings.ToLower(strings.TrimSpace(stringValue(message["status"])))
		name := strings.TrimSpace(stringValue(message["toolName"]))
		result, ok := message["toolResult"].(map[string]any)
		code := strings.ToLower(strings.TrimSpace(stringValue(result["code"])))
		if !ok || code == "execution_path_exhausted" || code == "semantic_failure_retry_exhausted" {
			continue
		}
		if agentruntime.ToolResultDidNotExecute(result) ||
			(phase != "failed" && status != "failed") {
			continue
		}
		toolInput, _ := message["toolInput"].(map[string]any)
		inputJSON, _ := json.Marshal(toolInput)
		capabilities := stringArrayValue(message["toolCapabilities"])
		fingerprint := agentruntime.SemanticFailureFingerprintWithCapabilities(name, inputJSON, result, capabilities)
		if fingerprint == "" || agentruntime.SemanticFailureFamilyTool(fingerprint) != normalizedTool ||
			agentruntime.SemanticFailureTarget(name, inputJSON) != target {
			continue
		}
		state.Attempts++
		state.Fingerprint = fingerprint
		state.ExecutionIdentity = agentruntime.ExecutionCallFingerprint(name, inputJSON)
	}
	return state
}

func (g serverAgentRuntimeToolGateway) durableSemanticFailureBoundary(
	ctx context.Context,
	toolName string,
	input map[string]any,
) map[string]any {
	if g.server == nil || strings.TrimSpace(g.sessionID) == "" {
		return nil
	}
	arguments, _ := json.Marshal(input)
	state := g.server.agentRuntimeSemanticFailureState(g.sessionID, toolName, arguments)
	if g.server.transcriptStore == nil {
		return durableSemanticExecutionBoundary(toolName, arguments, state)
	}
	stream, found, err := g.server.transcriptStore.GetFrameStreamBySession(ctx, "local", strings.TrimSpace(g.sessionID))
	if err != nil || !found {
		return durableSemanticExecutionBoundary(toolName, arguments, state)
	}
	projected, err := g.server.transcriptStore.ListRunnerReplay(ctx, transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MessageLimit: transcriptstore.MaxRunnerReplayProjection, CheckpointLimit: transcriptstore.MaxRunnerReplayProjection,
	})
	if err != nil {
		// A replay read is advisory to the tool gateway. If the read authority is
		// temporarily unavailable, preserve the normal in-memory guard and let the
		// runner report the durable replay error through its own recovery path.
		return nil
	}
	replayState := durableSemanticFailureStateForTarget(projected, toolName, arguments)
	if replayState.Attempts >= state.Attempts && replayState.ExecutionIdentity != "" {
		state = replayState
	}
	return durableSemanticExecutionBoundary(toolName, arguments, state)
}

func durableSemanticExecutionBoundary(
	toolName string,
	arguments json.RawMessage,
	state durableSemanticFailureState,
) map[string]any {
	if state.Attempts > 0 && state.Fingerprint != "" {
		switch agentruntime.SemanticFailureFamilyKind(state.Fingerprint) {
		case failurecontract.RateLimited, failurecontract.QuotaExhausted, failurecontract.Transient,
			failurecontract.NetworkBridgeDown, failurecontract.ProviderDegraded:
			return durableSemanticPreflightBoundary(map[string]any{
				"ok": false, "status": "external_state_required", "executed": false,
				"code": "external_state_required", "tool": strings.TrimSpace(toolName),
				"message":     "the previous registered execution ended on an external runtime condition; wait for a new user or external-state signal before starting another execution",
				"next_action": "wait_for_external_state_then_start_new_execution",
			})
		}
	}
	if state.Attempts == 0 || state.ExecutionIdentity == "" ||
		state.ExecutionIdentity != agentruntime.ExecutionCallFingerprint(toolName, arguments) {
		return nil
	}
	return durableSemanticPreflightBoundary(map[string]any{
		"ok": false, "status": "repeated_failed_tool_call", "executed": false,
		"code": "repeated_failed_tool_call", "tool": strings.TrimSpace(toolName),
		"message":     "the same registered execution already failed and cannot be repeated unchanged",
		"retryable":   false,
		"next_action": "change_the_execution_inputs_then_retry",
	})
}

func durableSemanticPreflightBoundary(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	value["executed"] = false
	value["preflight"] = true
	return value
}

func (s *Server) recordAgentRuntimeSemanticFailure(
	sessionID string,
	call agentruntime.ToolCall,
	value any,
	capabilitySets ...[]string,
) {
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(sessionID) == "" ||
		!agentruntime.ClassifyToolResult(value).HardFailed() || agentruntime.ToolResultDidNotExecute(value) {
		return
	}
	capabilities := []string(nil)
	if len(capabilitySets) > 0 {
		capabilities = capabilitySets[0]
	}
	fingerprint := agentruntime.SemanticFailureFingerprintWithCapabilities(
		call.Name, call.Arguments, value, capabilities,
	)
	if fingerprint == "" {
		return
	}
	switch agentruntime.SemanticFailureFamilyKind(fingerprint) {
	case failurecontract.RateLimited, failurecontract.QuotaExhausted, failurecontract.Transient,
		failurecontract.NetworkBridgeDown, failurecontract.ProviderDegraded:
		return
	}
	target := agentruntime.SemanticFailureTarget(call.Name, call.Arguments)
	key := "semantic-failure-" + serverStringHash(strings.Join([]string{
		strings.TrimSpace(sessionID), strings.TrimSpace(call.ID), strings.TrimSpace(call.Name), target,
	}, "\n"))[:24]
	_, _ = s.runtimeStore.Set(agentRuntimeSemanticFailureNamespace, key, map[string]any{
		"sessionId": strings.TrimSpace(sessionID), "toolCallId": strings.TrimSpace(call.ID),
		"tool": strings.ToLower(strings.TrimSpace(call.Name)), "target": target,
		"fingerprint": fingerprint, "executionIdentity": agentruntime.ExecutionCallFingerprint(call.Name, call.Arguments),
		"executed":  true,
		"createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) agentRuntimeSemanticFailureState(
	sessionID string,
	toolName string,
	arguments json.RawMessage,
) durableSemanticFailureState {
	state := durableSemanticFailureState{}
	if s == nil || s.runtimeStore == nil {
		return state
	}
	entries, err := s.runtimeStore.List(agentRuntimeSemanticFailureNamespace)
	if err != nil {
		return state
	}
	wantedSession := strings.TrimSpace(sessionID)
	wantedTool := strings.ToLower(strings.TrimSpace(toolName))
	wantedTarget := agentruntime.SemanticFailureTarget(wantedTool, arguments)
	for _, entry := range entries {
		value := mapValue(entry.Value)
		if value["executed"] != true {
			// Legacy entries did not distinguish a preflight rejection from a
			// real execution. The transcript remains the execution authority, so
			// an ambiguous cache row must not close a live runtime path.
			continue
		}
		if strings.TrimSpace(stringValue(value["sessionId"])) != wantedSession ||
			strings.ToLower(strings.TrimSpace(stringValue(value["tool"]))) != wantedTool ||
			strings.TrimSpace(stringValue(value["target"])) != wantedTarget {
			continue
		}
		fingerprint := strings.TrimSpace(stringValue(value["fingerprint"]))
		if fingerprint == "" {
			continue
		}
		state.Attempts++
		state.Fingerprint = fingerprint
		state.ExecutionIdentity = strings.TrimSpace(stringValue(value["executionIdentity"]))
	}
	return state
}
