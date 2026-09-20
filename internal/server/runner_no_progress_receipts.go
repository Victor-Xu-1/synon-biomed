package server

import (
	"context"
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type noProgressReceiptMatch struct {
	pending bool
	closed  bool
}

// noProgressBatchPreflight evaluates only this proposal's execution identities.
// History remains in the canonical transcript; neither the model preview nor
// an unbounded in-memory set is an admission authority. Each cancellable paged
// membership pass serves the whole batch, including normalized inputs.
func (g serverAgentRuntimeToolGateway) noProgressBatchPreflight(ctx context.Context, calls []agentruntime.ToolCall) (map[int]string, error) {
	diagnostics, err := g.correctionRoutePreflight(ctx, calls)
	if err != nil {
		return nil, err
	}
	nativeDiagnostics, err := g.nativeEditRecoveryPreflight(ctx, calls)
	if err != nil {
		return nil, err
	}
	if len(nativeDiagnostics) > 0 && diagnostics == nil {
		diagnostics = make(map[int]string)
	}
	for index, diagnostic := range nativeDiagnostics {
		diagnostics[index] = diagnostic
	}
	run := g.taskRun
	if run == nil || run.NoProgressRecovery == nil || run.NoProgressRecovery.Consecutive <= 0 {
		return diagnostics, nil
	}
	targets := make(map[string]noProgressReceiptMatch, len(calls)*2)
	callKeys := make([][]string, len(calls))
	for index, call := range calls {
		if !g.AdmitsToolCall(call) {
			continue
		}
		name, err := canonicalRuntimeToolName(call.Name)
		if err != nil {
			continue
		}
		input := map[string]any{}
		if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &input) != nil {
			continue
		}
		normalized, err := json.Marshal(g.normalizeAdmittedToolArguments(name, input))
		if err != nil {
			return nil, err
		}
		callKeys[index] = []string{agentruntime.ExecutionCallFingerprint(call.Name, call.Arguments), agentruntime.ExecutionCallFingerprint(name, normalized)}
		for _, key := range callKeys[index] {
			targets[key] = noProgressReceiptMatch{}
		}
	}
	if len(targets) == 0 {
		return diagnostics, nil
	}
	recovery := newSessionRunnerNoProgressRecovery(runnerRecoveryObligationFingerprint(run.Transcript, transcriptstore.RunnerInterruptionCause{}))
	err = g.server.scanSessionRunnerRecoveryEntries(ctx, run.Transcript, func(entry eventjournal.Entry) error {
		previousScope := recovery.ObligationFingerprint
		recovery.observeEntry(entry, run.Transcript)
		if previousScope != recovery.ObligationFingerprint || runnerEntryStartsNewLogicalTask(entry) || runnerCheckpointHasMaterialProgress(entry.Message) {
			for key := range targets {
				targets[key] = noProgressReceiptMatch{}
			}
		}
		message := entry.Message
		if message["type"] != "runner_checkpoint" {
			return nil
		}
		reason := firstNonEmpty(strings.TrimSpace(stringValue(message["reason_code"])), strings.TrimSpace(stringValue(message["reasonCode"])))
		if reason == sessionRunnerToolRoundNoProgressReasonCode || reason == sessionRunnerToolRoundNoProgressExhaustedReasonCode {
			// Explicit closure records preserve older route decisions, including
			// rejected routes. They do not assert that those proposals executed.
			for key, match := range targets {
				match.closed = match.closed || match.pending || recovery.containsFingerprint(key)
				targets[key] = match
			}
			return nil
		}
		if !runnerCheckpointHasReusableNoProgressReceipt(message) && !runnerCheckpointHasRejectedRouteReceipt(message) {
			return nil
		}
		tool := stringValue(message["toolName"])
		for _, input := range []any{message["toolInput"], runnerCheckpointExecutedToolInput(message)} {
			if _, ok := input.(map[string]any); !ok {
				continue
			}
			arguments, err := json.Marshal(input)
			if err != nil {
				return err
			}
			key := agentruntime.ExecutionCallFingerprint(tool, arguments)
			if match, found := targets[key]; found {
				match.pending = true
				targets[key] = match
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	expected := runnerRecoveryObligationFingerprint(run.Transcript, run.correctionCause())
	if recovery.ObligationFingerprint != expected || recovery.Consecutive <= 0 {
		return diagnostics, nil
	}
	if diagnostics == nil {
		diagnostics = make(map[int]string)
	}
	for index, keys := range callKeys {
		for _, key := range keys {
			if targets[key].closed {
				if diagnostics[index] == "" {
					diagnostics[index] = noProgressClosedRouteDiagnostic
				}
				break
			}
		}
	}
	return diagnostics, nil
}

const noProgressClosedRouteDiagnostic = `{"code":"durable_no_progress_route_closed","message":"This exact tool route is closed in the current recovery obligation.","recovery":"Reuse completed receipts and choose materially different arguments or another advertised capability; finish if no further evidence is needed."}`

func runnerCheckpointHasReusableNoProgressReceipt(message eventjournal.Message) bool {
	if _, ok := message["toolInput"].(map[string]any); !ok {
		return false
	}
	if message["type"] != "runner_checkpoint" || message["status"] != "completed" || message["toolPhase"] != "completed" ||
		boolValue(message["rejectedBeforeExecution"], false) || strings.TrimSpace(stringValue(message["toolName"])) == "" {
		return false
	}
	result := mapValue(message["toolResult"])
	return len(result) > 0 && agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded &&
		!agentruntime.IsNonExecutingPreflight(result) && !runnerCheckpointHasMaterialProgress(message)
}

// A persisted admission rejection is evidence that the unchanged route is
// closed, not that an execution succeeded. Ordinary failures/unavailable
// sources and proposals without a terminal receipt do not qualify here.
func runnerCheckpointHasRejectedRouteReceipt(message eventjournal.Message) bool {
	if _, ok := message["toolInput"].(map[string]any); !ok {
		return false
	}
	if message["type"] != "runner_checkpoint" || message["status"] != "failed" ||
		message["toolPhase"] != prestartToolFailurePhase || !boolValue(message["rejectedBeforeExecution"], false) ||
		strings.TrimSpace(stringValue(message["toolName"])) == "" {
		return false
	}
	result := mapValue(message["toolResult"])
	return result["ok"] == false && result["executed"] == false && strings.TrimSpace(stringValue(result["code"])) != ""
}
