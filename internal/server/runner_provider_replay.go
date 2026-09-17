package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// sessionEntriesToProviderMessages is the sole Transcript/journal to provider
// history projection. Tool calls remain native assistant tool_calls paired
// with native tool messages. Incomplete or conflicting protocol history fails
// closed instead of being converted to advisory system prose.
func sessionEntriesToProviderMessages(
	systemPrompt string,
	entries []eventjournal.Entry,
) ([]chatCompletionMessage, error) {
	messages := []chatCompletionMessage{{Role: "system", Content: systemPrompt}}
	compactSummary, compactEventID, hasCompact := latestCompactModelContext(entries)
	if hasCompact {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: "Synon compact handoff context:\n" + compactSummary})
	}
	if correctionContext := recoveredRunnerCorrectionContext(entries); correctionContext != "" {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: correctionContext})
	}
	if toolContext := recoveredToolTranscriptContext(entries, compactEventID); toolContext != "" {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: toolContext})
	}
	if repairContext := recoveredToolFailureContext(entries, compactEventID); repairContext != "" {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: repairContext})
	}
	// Compaction may summarize the scientific work, but an answered AskUser
	// choice is an exact user-owned execution decision rather than summarizable
	// prose. Replay every durable input response from the current logical task
	// verbatim across the compact boundary so a selected implementation cannot
	// be widened to a method family or silently replaced after resume.
	if hasCompact {
		for _, entry := range entries {
			if entry.EventID > compactEventID || !runnerEntryIsInputResponse(entry) {
				continue
			}
			text := strings.TrimSpace(runnerModelMessageText(entry.Message))
			if text != "" {
				messages = append(messages, chatCompletionMessage{Role: "user", Content: text})
			}
		}
	}
	latestPreCompactUser := ""
	pending := map[string]struct{}{}
	settled := map[string]string{}
	settledBackground := map[string]bool{}
	settledPreflight := map[string]bool{}
	deferredAfterToolBatch := []chatCompletionMessage{}
	for _, entry := range entries {
		if hasCompact && entry.EventID <= compactEventID {
			if role := strings.TrimSpace(stringValue(entry.Message["role"])); role == "user" {
				if text := runnerModelMessageText(entry.Message); text != "" {
					latestPreCompactUser = text
				}
			}
			continue
		}
		eventType := strings.TrimSpace(stringValue(entry.Message["type"]))
		if eventType == "runner_checkpoint" {
			// Kernel-host MCP evidence is an immutable receipt nested inside an
			// outer repl/python tool transaction. The model declared only the
			// outer call, so replaying this child receipt as a provider tool
			// message would create a result with no matching assistant tool_call.
			// Durable evidence reconstruction consumes these receipts separately.
			if isNestedKernelMCPReplayReceipt(entry.Message) {
				continue
			}
			calls, hasCalls, err := nativeProviderToolCalls(entry.Message["modelToolCalls"])
			if err != nil {
				return nil, fmt.Errorf("replay model tool calls at event %d: %w", entry.EventID, err)
			}
			if hasCalls {
				for _, call := range calls {
					if _, duplicate := pending[call.ID]; duplicate {
						return nil, errors.New("provider replay contains a duplicate unsettled tool call")
					}
					if _, duplicate := settled[call.ID]; duplicate {
						return nil, errors.New("provider replay reuses a settled tool call identity")
					}
					pending[call.ID] = struct{}{}
				}
				messages = append(messages, chatCompletionMessage{Role: "assistant", ToolCalls: calls})
				continue
			}
			phase := strings.TrimSpace(stringValue(entry.Message["toolPhase"]))
			// outcome_unknown is a durable terminal settlement produced by the
			// runner recovery path when a tool's execution outcome cannot be
			// reconstructed after a crash/restart. It must settle the provider
			// replay transaction exactly like failed, otherwise an otherwise
			// recovered attempt fails closed with an unsettled tool transaction.
			if !sessionRunnerToolContinuityTerminalPhase(phase) {
				continue
			}
			callID := strings.TrimSpace(stringValue(entry.Message["toolCallId"]))
			if callID == "" {
				return nil, errors.New("provider replay terminal tool result has no call identity")
			}
			result, present := entry.Message["toolResult"]
			if !present || result == nil {
				return nil, errors.New("provider replay terminal tool result is missing")
			}
			if isCompletedSkillToolName(strings.TrimSpace(stringValue(entry.Message["toolName"]))) {
				// Historical checkpoints can contain an older rendered Skill
				// contract in either the original object shape or the newer readable
				// string shape. Reduce both to the same identity-only receipt so the
				// current materialized Skill injected by runner_execution is the sole
				// execution contract for this recovery turn.
				result = agentRuntimeLegacySkillModelResult(result)
			}
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) == 0 {
				return nil, errors.New("provider replay terminal tool result is invalid")
			}
			providerContent := string(encoded)
			if text, ok := result.(string); ok {
				providerContent = text
			}
			providerContent, err = compactRunnerLargeToolResultDescriptorForReplay(providerContent)
			if err != nil {
				return nil, fmt.Errorf("compact provider replay tool result at event %d: %w", entry.EventID, err)
			}
			if previous, duplicate := settled[callID]; duplicate {
				// A terminal-frame sweep may only synthesize outcome_unknown for an
				// actually unsettled call. Historical builds swept a batch that had
				// already published a terminal provider receipt but had not advanced
				// its storage cursor. The earlier receipt is authoritative; the later
				// uncertainty marker cannot retroactively erase a known outcome.
				if phase == "outcome_unknown" {
					continue
				}
				// A background tool call is settled for the provider when the
				// launcher returns its durable execution identity. Its later cell
				// notification is consumed through wait_for_notification and is a
				// lifecycle update, not a second provider tool result for the same
				// assistant call. Historical builds emitted both checkpoints; ignore
				// only that precisely identified legacy projection while preserving
				// fail-closed handling for every other divergent duplicate.
				if settledBackground[callID] && entry.Message["toolInput"] == nil {
					continue
				}
				// A non-executing preflight is the terminal authority for this
				// provider call. Historical runtimes could leave its durable local
				// operation approved and execute it during recovery, producing a
				// second divergent receipt. The later execution is invalid by
				// definition: once executed=false settled the call, it must not run.
				// Preserve the first result and collapse that impossible lifecycle.
				if settledPreflight[callID] {
					continue
				}
				// A runner handoff can observe the same immutable terminal result
				// after the originating checkpoint was already committed. Provider
				// protocols permit exactly one tool result per assistant call, so
				// collapse byte-identical receipts while still failing closed on any
				// divergent terminal fact.
				if previous != providerContent {
					return nil, errors.New("provider replay contains conflicting terminal tool results")
				}
				continue
			}
			if _, ok := pending[callID]; !ok {
				// A machine-created terminal recovery receipt may close a batch
				// whose assistant root belongs to an older compacted replay
				// window. It is ledger settlement, not a provider message. Keep
				// arbitrary or model-authored stray terminal results
				// fail-closed; only loadTranscriptRunnerReplay can attach this
				// source authority after decoding the immutable event type.
				if entry.SourceEventType == transcriptstore.TerminalToolRecoveryEventType ||
					sessionRunnerRematerializedTerminalReceipt(entry) {
					continue
				}
				// Completed Skill checkpoints are deliberately pinned outside the
				// bounded provider window so long tasks retain capability identity.
				// When their original assistant tool-call root has aged out, the
				// current Skill contract is injected separately at system priority;
				// replaying this orphaned result would violate provider protocol.
				if phase == "completed" && isCompletedSkillToolName(strings.TrimSpace(stringValue(entry.Message["toolName"]))) {
					continue
				}
				return nil, fmt.Errorf(
					"provider replay terminal tool result at event %d for call %q has no matching assistant tool call",
					entry.EventID, callID,
				)
			}
			messages = append(messages, chatCompletionMessage{Role: "tool", ToolCallID: callID, Content: providerContent})
			delete(pending, callID)
			settled[callID] = providerContent
			settledBackground[callID] = boolValue(mapValue(entry.Message["toolInput"])["background"], false)
			if resultMap, ok := result.(map[string]any); ok {
				// Every executed=false result is authoritative pre-execution
				// settlement, including non-kernel policy decisions such as an
				// agent-owned AskUser choice. A historical recovery must never
				// replace it with a later divergent receipt for the same call ID.
				settledPreflight[callID] = !boolValue(resultMap["executed"], true) || agentKernelPreflightResult(resultMap)
			}
			if len(pending) == 0 && len(deferredAfterToolBatch) > 0 {
				messages = append(messages, deferredAfterToolBatch...)
				deferredAfterToolBatch = nil
			}
			continue
		}
		if eventType == "content_delta" || eventType == "content_reset" {
			continue
		}
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		if role != "user" && role != "assistant" {
			continue
		}
		text := runnerMessageText(entry.Message)
		if role == "user" {
			text = runnerModelMessageText(entry.Message)
		}
		if text != "" {
			message := chatCompletionMessage{Role: role, Content: text}
			if len(pending) > 0 {
				deferredAfterToolBatch = append(deferredAfterToolBatch, message)
			} else {
				messages = append(messages, message)
			}
		}
	}
	if len(pending) != 0 {
		return nil, errors.New("provider replay contains an unsettled tool transaction")
	}
	if hasCompact && !hasChatRole(messages, "user") && strings.TrimSpace(latestPreCompactUser) != "" {
		messages = append(messages, chatCompletionMessage{Role: "user", Content: latestPreCompactUser})
	}
	return messages, nil
}

// sessionRunnerRematerializedTerminalReceipt identifies the machine-created
// receipt emitted when durable large-result bytes are rematerialized after a
// process interruption. Older rows were persisted as ordinary
// runner_checkpoint events rather than TerminalToolRecoveryEventType. When the
// original assistant call belongs to an earlier compacted window, this receipt
// is ledger settlement, not a new provider tool message. Exact internal fields
// keep arbitrary or model-authored orphan results fail-closed.
func sessionRunnerRematerializedTerminalReceipt(entry eventjournal.Entry) bool {
	message := entry.Message
	callID := strings.TrimSpace(stringValue(message["toolCallId"]))
	return callID != "" &&
		strings.EqualFold(strings.TrimSpace(stringValue(message["lifecyclePhase"])), "recovery") &&
		strings.TrimSpace(stringValue(message["message"])) == "recovered durable externalized tool result" &&
		strings.TrimSpace(stringValue(message["resumeCacheKey"])) == "chat-tool-completed-"+callID
}

// recoveredToolFailureContext turns a durable, structured tool failure into a
// bounded repair directive for the next model generation. The native tool
// result remains in the provider protocol below; this extra system message
// prevents a resumed task from treating a failed intermediate step as a final
// answer or repeating the same invalid call. It is emitted only when the
// failure carries an explicit recovery contract, so arbitrary prose cannot
// manufacture an automatic retry.
func recoveredToolFailureContext(entries []eventjournal.Entry, compactEventID int64) string {
	lines := make([]string, 0, 8)
	for _, entry := range entries {
		if compactEventID > 0 && entry.EventID <= compactEventID {
			continue
		}
		message := entry.Message
		phase := strings.TrimSpace(stringValue(message["toolPhase"]))
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
			continue
		}
		result := mapValue(message["toolResult"])
		errorValue := mapValue(result["error"])
		code := strings.TrimSpace(firstNonEmpty(stringValue(result["code"]), stringValue(errorValue["code"])))
		legacyArtifactFailure := code == "artifact_save_requires_correction"
		if phase != "failed" && phase != prestartToolFailurePhase && !legacyArtifactFailure {
			continue
		}
		recovery := strings.TrimSpace(firstNonEmpty(stringValue(result["recovery"]), stringValue(errorValue["recovery"])))
		retryable, hasRetryable := result["retryable"]
		if !hasRetryable {
			retryable, hasRetryable = errorValue["retryable"]
		}
		if code == "" && recovery == "" && !hasRetryable {
			continue
		}
		line := fmt.Sprintf("event %d: tool=%s", entry.EventID, strings.TrimSpace(stringValue(message["toolName"])))
		if code != "" {
			line += " code=" + code
		}
		if hasRetryable {
			line += fmt.Sprintf(" retryable=%t", boolValue(retryable, false))
		}
		if recovery != "" {
			line += " recovery=" + recovery
		}
		if code == "artifact_save_requires_correction" {
			unsupported, available, replacements := artifactSaveEvidenceRepairReferences(result)
			if len(unsupported) > 0 {
				line += " unsupported=" + strings.Join(unsupported, ",")
			}
			if len(replacements) > 0 {
				line += " unique_exact_replacement_candidates=" + strings.Join(replacements, ",")
			}
			if len(available) > 0 {
				line += " exact_available=" + strings.Join(available, ",")
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return "Synon task self-repair controller: the prior tool step produced a structured failure. Treat it as an intermediate diagnosis, not a completed result. Do not repeat an identical failed call. For a retryable failure, classify it, apply the stated governed-runtime repair or create a new immutable corrected input, then rerun only the affected step and verify its real output/receipt before continuing. Preserve successful earlier steps, provenance, logs, and task identity. For retryable=false or any unresolved approval/user decision, stop at that boundary and ask or report the exact blocker; never claim success.\n" + strings.Join(lines, "\n")
}

func artifactSaveEvidenceRepairReferences(result map[string]any) ([]string, []string, []string) {
	unsupported := make([]string, 0)
	available := make([]string, 0)
	entries := append(anySliceValue(result["errors"]), anySliceValue(result["warnings"])...)
	for _, raw := range entries {
		failure, _ := raw.(map[string]any)
		if strings.TrimSpace(stringValue(failure["code"])) != "unsupported_evidence_references" {
			continue
		}
		for _, item := range anySliceValue(failure["unsupported_evidence_references"]) {
			if value := strings.TrimSpace(stringValue(item)); value != "" {
				unsupported = append(unsupported, value)
			}
		}
		for _, item := range anySliceValue(failure["available_evidence_references"]) {
			if value := strings.TrimSpace(stringValue(item)); value != "" {
				available = append(available, value)
			}
		}
	}
	unsupported = uniqueSortedFolded(unsupported)
	available = uniqueSortedFolded(available)
	if len(unsupported) > 8 {
		unsupported = unsupported[:8]
	}
	if len(available) > 24 {
		available = available[:24]
	}
	replacements := uniqueEvidenceReferenceReplacementCandidates(unsupported, available)
	return unsupported, available, replacements
}

func uniqueEvidenceReferenceReplacementCandidates(unsupported, available []string) []string {
	replacements := make([]string, 0, len(unsupported))
	for _, source := range unsupported {
		bestDistance := 3
		best := ""
		ambiguous := false
		for _, candidate := range available {
			if evidenceReferenceNamespace(source) != evidenceReferenceNamespace(candidate) {
				continue
			}
			distance := boundedIdentifierEditDistance(source, candidate, 2)
			if distance < bestDistance {
				bestDistance, best, ambiguous = distance, candidate, false
			} else if distance == bestDistance && candidate != best {
				ambiguous = true
			}
		}
		if best != "" && bestDistance <= 2 && !ambiguous {
			replacements = append(replacements, source+"=>"+best)
		}
	}
	return replacements
}

func evidenceReferenceNamespace(value string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), ":")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ":")
}

func boundedIdentifierEditDistance(left, right string, limit int) int {
	leftRunes, rightRunes := []rune(left), []rune(right)
	if delta := len(leftRunes) - len(rightRunes); delta > limit || delta < -limit {
		return limit + 1
	}
	previous := make([]int, len(rightRunes)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range leftRunes {
		current := make([]int, len(rightRunes)+1)
		current[0] = leftIndex + 1
		rowMinimum := current[0]
		for rightIndex, rightRune := range rightRunes {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[rightIndex+1] = min(
				current[rightIndex]+1,
				previous[rightIndex+1]+1,
				previous[rightIndex]+cost,
			)
			rowMinimum = min(rowMinimum, current[rightIndex+1])
		}
		if rowMinimum > limit {
			return limit + 1
		}
		previous = current
	}
	return previous[len(rightRunes)]
}

func isNestedKernelMCPReplayReceipt(message eventjournal.Message) bool {
	if strings.TrimSpace(stringValue(message["schema"])) != "synon.kernel_mcp_evidence.v1" {
		return false
	}
	toolCallID := strings.TrimSpace(stringValue(message["toolCallId"]))
	hostCallID := strings.TrimSpace(stringValue(message["hostCallId"]))
	outerToolCallID := strings.TrimSpace(stringValue(message["outerToolCallId"]))
	return toolCallID != "" && hostCallID == toolCallID && outerToolCallID != "" && outerToolCallID != toolCallID
}

func nativeProviderToolCalls(raw any) ([]chatCompletionToolCall, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, true, err
	}
	var stored []struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(encoded, &stored); err != nil || len(stored) == 0 {
		return nil, true, errors.New("model tool-call batch is invalid")
	}
	seen := map[string]struct{}{}
	calls := make([]chatCompletionToolCall, 0, len(stored))
	for _, item := range stored {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		if item.ID == "" || item.Name == "" || item.Type != "function" || len(item.Arguments) == 0 || !json.Valid(item.Arguments) {
			return nil, true, errors.New("model tool-call identity or arguments are invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, true, errors.New("model tool-call identity is duplicated")
		}
		seen[item.ID] = struct{}{}
		canonicalArguments := any(nil)
		if err := json.Unmarshal(item.Arguments, &canonicalArguments); err != nil {
			return nil, true, err
		}
		arguments, err := json.Marshal(canonicalArguments)
		if err != nil {
			return nil, true, err
		}
		calls = append(calls, chatCompletionToolCall{ID: item.ID, Type: "function", Function: chatCompletionToolCallFunction{
			Name: item.Name, Arguments: string(arguments),
		}})
	}
	return calls, true, nil
}
