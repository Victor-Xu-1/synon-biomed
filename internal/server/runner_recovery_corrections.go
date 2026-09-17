package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func normalizeSessionRunnerChatOptions(options SessionRunnerChatOptions) SessionRunnerChatOptions {
	options.SessionID = strings.TrimSpace(options.SessionID)
	options.RunnerID = strings.TrimSpace(options.RunnerID)
	if options.RunnerID == "" {
		options.RunnerID = defaultSessionRunnerID
	}
	options.Endpoint = strings.TrimSpace(options.Endpoint)
	options.APIKey = strings.TrimSpace(options.APIKey)
	options.Model = strings.TrimSpace(options.Model)
	if options.Endpoint == BuiltinSessionRunnerChatEndpoint && options.Model == "" {
		options.Model = BuiltinSessionRunnerChatModel
	}
	options.SystemPrompt = strings.TrimSpace(options.SystemPrompt)
	if options.SystemPrompt == "" {
		options.SystemPrompt = defaultSessionRunnerChatSystemPrompt
	}
	explicitAllowedTools := len(options.AllowedTools) > 0
	options.AllowedTools = normalizeChatToolNames(options.AllowedTools)
	if explicitAllowedTools && len(options.AllowedTools) == 0 {
		options.AllowedTools = []string{noChatToolsAllowedSentinel}
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultSessionRunnerChatRequestTimeout
	}
	if options.PreparationTimeout <= 0 {
		options.PreparationTimeout = defaultSessionRunnerChatPreparationTimeout
	}
	if options.CompactSummaryTimeout <= 0 {
		options.CompactSummaryTimeout = defaultSessionRunnerCompactSummaryTimeout
	}
	if options.MaxToolRounds < 0 {
		options.MaxToolRounds = 0
	}
	if options.MaxToolCallsPerRound < 0 {
		options.MaxToolCallsPerRound = 0
	}
	if options.MaxConsecutiveIdenticalToolRounds <= 0 {
		options.MaxConsecutiveIdenticalToolRounds = sessionRunnerConsecutiveIdenticalToolRoundBudget
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = defaultSessionRunnerChatMaxAttempts
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = defaultSessionRunnerLeaseTTL
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultSessionRunnerPollInterval
	}
	if options.ReplayLimit <= 0 {
		options.ReplayLimit = defaultSessionRunnerReplayLimit
	}
	if options.OutputLimitBytes <= 0 {
		options.OutputLimitBytes = defaultSessionRunnerOutputLimitBytes
	}
	if options.ModelResponseLimitBytes <= 0 {
		options.ModelResponseLimitBytes = defaultSessionRunnerModelResponseLimitBytes
	}
	if options.TranscriptResumeSource == "" {
		options.TranscriptResumeSource = transcriptstore.ResumeSourceFresh
	}
	return options
}

func sessionEntriesToChatMessages(systemPrompt string, entries []eventjournal.Entry) []chatCompletionMessage {
	messages := []chatCompletionMessage{{Role: "system", Content: systemPrompt}}
	compactSummary, compactEventID, hasCompact := latestCompactModelContext(entries)
	if hasCompact {
		messages = append(messages, chatCompletionMessage{
			Role:    "system",
			Content: "Synon compact handoff context:\n" + compactSummary,
		})
	}
	if correctionContext := recoveredRunnerCorrectionContext(entries); correctionContext != "" {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: correctionContext})
	}
	latestPreCompactUser := ""
	for _, entry := range entries {
		if hasCompact && entry.EventID <= compactEventID {
			if role := strings.TrimSpace(stringValue(entry.Message["role"])); role == "user" {
				if text := runnerModelMessageText(entry.Message); text != "" {
					latestPreCompactUser = text
				}
			}
			continue
		}
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		if eventType := strings.TrimSpace(stringValue(entry.Message["type"])); eventType == "content_delta" || eventType == "content_reset" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		text := runnerMessageText(entry.Message)
		if role == "user" {
			text = runnerModelMessageText(entry.Message)
		}
		if text == "" {
			continue
		}
		messages = append(messages, chatCompletionMessage{Role: role, Content: text})
	}
	if hasCompact && !hasChatRole(messages, "user") && strings.TrimSpace(latestPreCompactUser) != "" {
		messages = append(messages, chatCompletionMessage{Role: "user", Content: latestPreCompactUser})
	}
	return messages
}

type recoveredRunnerCorrection struct {
	ReasonCode               string
	Detail                   string
	RecoveryContractRevision int64
}

func latestRunnerCorrection(entries []eventjournal.Entry) (recoveredRunnerCorrection, bool) {
	var protocolFallback *recoveredRunnerCorrection
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		message := entry.Message
		if runnerEntryStartsNewLogicalTask(entry) {
			break
		}
		if strings.TrimSpace(stringValue(message["type"])) == "runner_finished" &&
			strings.TrimSpace(stringValue(message["status"])) == "failed" {
			detail := strings.TrimSpace(stringValue(message["detail"]))
			if strings.HasPrefix(detail, "completion reviewer rejected the current candidate") &&
				len(detail) <= maxRunnerCorrectionResumeDetailBytes {
				return recoveredRunnerCorrection{
					ReasonCode: "completion_review_correction_required", Detail: detail,
				}, true
			}
		}
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
			continue
		}
		reason := strings.TrimSpace(stringValue(message["reason_code"]))
		if reason == "" {
			reason = strings.TrimSpace(stringValue(message["reasonCode"]))
		}
		switch reason {
		case "artifact_reference_correction_required",
			"completion_review_correction_required",
			sessionRunnerCompletionReviewRecoveryReasonCode,
			sessionRunnerRealScientificEvidenceRequiredReasonCode,
			sessionRunnerPlanStepsIncompleteReasonCode,
			sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
			sessionRunnerVisualArtifactValidationReasonCode, sessionRunnerResponseLanguageMismatchReasonCode:
		default:
			continue
		}
		detail := strings.TrimSpace(stringValue(message["resume_detail"]))
		if detail == "" {
			detail = strings.TrimSpace(stringValue(message["resumeDetail"]))
		}
		if detail == "" || len(detail) > maxRunnerCorrectionResumeDetailBytes {
			return recoveredRunnerCorrection{}, false
		}
		correction := recoveredRunnerCorrection{
			ReasonCode: reason, Detail: detail,
			RecoveryContractRevision: recoveredRunnerContractRevision(message),
		}
		if recoveredRunnerCorrectionSupersededByCurrentContract(correction) {
			return recoveredRunnerCorrection{}, false
		}
		if correction.ReasonCode == sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode {
			// Provider protocol noncompliance is not a new task defect. Retain it
			// only when no earlier substantive correction exists; otherwise the
			// original evidence/artifact obligation remains authoritative.
			if protocolFallback == nil {
				copy := correction
				protocolFallback = &copy
			}
			continue
		}
		return correction, true
	}
	if protocolFallback != nil {
		return *protocolFallback, true
	}
	return recoveredRunnerCorrection{}, false
}

func recoveredRunnerContractRevision(message eventjournal.Message) int64 {
	for _, key := range []string{"recovery_contract_revision", "recoveryContractRevision"} {
		switch value := message[key].(type) {
		case int:
			if value > 0 && value <= 1_000_000 {
				return int64(value)
			}
		case int64:
			if value > 0 && value <= 1_000_000 {
				return value
			}
		case float64:
			if value > 0 && value <= 1_000_000 && float64(int64(value)) == value {
				return int64(value)
			}
		case json.Number:
			if parsed, err := value.Int64(); err == nil && parsed > 0 && parsed <= 1_000_000 {
				return parsed
			}
		}
	}
	return 0
}

func recoveredRunnerCorrectionSupersededByCurrentContract(correction recoveredRunnerCorrection) bool {
	return correction.RecoveryContractRevision > 0 &&
		correction.RecoveryContractRevision < sessionRunnerRecoveryContractRevision &&
		correction.ReasonCode == "artifact_reference_correction_required" &&
		sessionRunnerCorrectionOnlyHasPublicationFreshnessFailures(correction.Detail)
}

func hasRecoveredRunnerCorrection(entries []eventjournal.Entry) bool {
	_, found := latestRunnerCorrection(entries)
	return found
}

func recoveredRunnerCorrectionRequiresTool(entries []eventjournal.Entry) bool {
	correction, found := latestRunnerCorrection(entries)
	if !found {
		return false
	}
	switch correction.ReasonCode {
	case sessionRunnerPlanStepsIncompleteReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"completion_review_correction_required":
		return true
	case "artifact_reference_correction_required":
		// A missing deliverable requires a real workspace mutation. A final-answer
		// or artifact defect requires a real workspace mutation. Final-answer
		// reference corrections remain model-directed: removing an unsupported
		// claim needs no tool, while preserving it still fails the immutable
		// reference validator unless a new durable source receipt attests it.
		return runnerCorrectionReportsArtifactMutation(correction.Detail)
	default:
		return false
	}
}

func runnerCorrectionEntriesForClassification(reason, detail string) []eventjournal.Entry {
	return []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code": strings.TrimSpace(reason), "resume_detail": strings.TrimSpace(detail),
	}}}
}

func recoveredRunnerInitialToolChoice(entries []eventjournal.Entry, availableToolSets ...[]agentruntime.ToolSchema) any {
	if !recoveredRunnerCorrectionRequiresTool(entries) {
		return nil
	}
	for _, tools := range availableToolSets {
		if len(tools) > 0 {
			// Stop-hook feedback may require a real action, but it must never
			// prescribe which action. "required" leaves the complete advertised
			// catalog visible and lets the model select the tool and arguments.
			// This differs from a named tool choice, which would create a fixed
			// workflow and compete with model-directed recovery.
			return "required"
		}
	}
	return nil
}

func runnerCorrectionRequiresMCPExecution(detail string) bool {
	detail = strings.ToLower(strings.TrimSpace(detail))
	return strings.Contains(detail, "real mcp method execution") &&
		strings.Contains(detail, "no completed mcp tool result")
}

func runnerCorrectionReportsMissingDeliverable(detail string) bool {
	return runnerCorrectionReportsPositiveCount(detail, "missing_required_deliverables=")
}

func runnerCorrectionReportsArtifactMutation(detail string) bool {
	for _, marker := range []string{
		"missing_required_deliverables=",
		"invalid_reference_artifacts=",
		"invalid_scientific_artifacts=",
		"cross_artifact_failures=",
		"invalid_research_artifacts=",
		"missing_local_artifacts=",
	} {
		if runnerCorrectionReportsPositiveCount(detail, marker) {
			return true
		}
	}
	return false
}

func runnerCorrectionReportsUnsupportedCitation(detail string) bool {
	return runnerCorrectionReportsPositiveCount(detail, "unsupported_citations=")
}

func runnerCorrectionReportsPositiveCount(detail, marker string) bool {
	detail = strings.ToLower(strings.TrimSpace(detail))
	marker = strings.ToLower(strings.TrimSpace(marker))
	if marker == "" {
		return false
	}
	if index := strings.Index(detail, marker); index >= 0 {
		digits := detail[index+len(marker):]
		count := 0
		foundDigit := false
		for _, char := range digits {
			if char < '0' || char > '9' {
				break
			}
			foundDigit = true
			count = count*10 + int(char-'0')
		}
		if foundDigit {
			return count > 0
		}
	}
	return strings.Contains(detail, "missing required deliverable")
}

func hasRecoveredRunnerContentGeneration(entries []eventjournal.Entry) bool {
	if providerStreamInterruptionCount(entries) > 0 || hasRecoveredRunnerCorrection(entries) {
		return true
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		message := entry.Message
		if runnerEntryStartsNewLogicalTask(entry) {
			break
		}
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
			continue
		}
		reason := strings.TrimSpace(stringValue(message["reason_code"]))
		if reason == "" {
			reason = strings.TrimSpace(stringValue(message["reasonCode"]))
		}
		if reason == "content_delta_persistence_deadline" {
			return true
		}
	}
	return false
}

func classifySessionRunnerContentDeltaPersistenceError(index int, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("persist model content delta batch %d: %w", index, err)
	if errors.Is(err, context.DeadlineExceeded) {
		return sessionRunnerPersistenceInterruption{cause: wrapped}
	}
	return wrapped
}

func recoveredRunnerCorrectionContext(entries []eventjournal.Entry) string {
	correction, found := latestRunnerCorrection(entries)
	if !found {
		return ""
	}
	transition := "resume_from_checkpoint"
	switch correction.ReasonCode {
	case sessionRunnerPlanStepsIncompleteReasonCode:
		transition = "advance_durable_plan"
	case "artifact_reference_correction_required":
		transition = "repair_current_candidate"
	case "completion_review_correction_required":
		transition = "repair_rejected_acceptance_condition"
	case sessionRunnerCompletionReviewRecoveryReasonCode:
		transition = "restore_review_transport"
	case sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode:
		transition = "resume_pending_runtime_transition"
	case sessionRunnerRealScientificEvidenceRequiredReasonCode:
		transition = "satisfy_missing_capability"
	case sessionRunnerResponseLanguageMismatchReasonCode:
		transition = "replace_response_presentation"
	case sessionRunnerVisualArtifactValidationReasonCode:
		transition = "repair_and_revalidate_visual_output"
	}
	contract := map[string]any{
		"schema":                              "synon.runner_recovery.v1",
		"reason_code":                         correction.ReasonCode,
		"detail":                              correction.Detail,
		"required_transition":                 transition,
		"requires_tool":                       recoveredRunnerCorrectionRequiresTool(entries),
		"choose_from_advertised_capabilities": true,
		"preserve_completed_state":            true,
		"reject_unchanged_repeat":             true,
		"revalidate_after_state_change":       true,
		"logical_task_unbounded":              true,
	}
	if requirements := runnerArtifactRepairRequirements(correction.Detail); len(requirements) > 0 {
		contract["repair_requirements"] = requirements
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return ""
	}
	return sessionRunnerDurableCorrectionContextMarker + ". Continue the same logical task from durable state. Resolve the machine-owned condition below through the current turn's advertised capabilities; preserve completed work and do not repeat an unchanged failed call.\n" + string(encoded)
}

// moveRecoveredRunnerCorrectionContextToEnd keeps exactly one correction
// instruction and places stop-hook feedback adjacent to the next provider
// action. Long-running tasks append Skills, memory and runtime policy after the
// initial transcript projection; leaving the correction near the front lets a
// weaker provider anchor on the stale candidate it is supposed to repair.
func moveRecoveredRunnerCorrectionContextToEnd(messages []chatCompletionMessage) []chatCompletionMessage {
	if len(messages) == 0 {
		return messages
	}
	result := make([]chatCompletionMessage, 0, len(messages))
	var correction *chatCompletionMessage
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			copyMessage := message
			correction = &copyMessage
			continue
		}
		result = append(result, message)
	}
	if correction != nil {
		result = append(result, *correction)
	}
	return result
}
