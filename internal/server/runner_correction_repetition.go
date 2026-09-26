package server

import (
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const runnerCorrectionRepetitionProjectionType = "runner_correction_repetition_projection"

// This is derived in constant space from the fenced canonical transcript, not
// from the provider's bounded history. It is never a second durable authority.
type runnerCorrectionRepetition struct {
	ReasonCode              string
	Fingerprint             string
	Count                   int
	StalledCycles           int
	AttemptedSinceCondition bool
	ProgressSinceCondition  bool
}

func correctionRepetitionFingerprint(reason, detail string) string {
	if marker := strings.Index(detail, sessionRunnerNoProgressDetailMarker); marker >= 0 {
		detail = detail[:marker]
	}
	return runnerRecoveryConditionFingerprint(reason, detail)
}

func (state *runnerCorrectionRepetition) observe(entry eventjournal.Entry) {
	if runnerEntryStartsNewLogicalTask(entry) {
		*state = runnerCorrectionRepetition{}
		return
	}
	// Only an in-memory entry created by the canonical replay loader can supply
	// an aggregate. Matching payload keys in a tool/user event are not trusted.
	if entry.SourceEventType == runnerCorrectionRepetitionProjectionType {
		if projected, ok := entry.Message[runnerCorrectionRepetitionProjectionType].(runnerCorrectionRepetition); ok {
			*state = projected
		}
		return
	}
	if strings.TrimSpace(stringValue(entry.Message["type"])) != "runner_checkpoint" {
		return
	}
	if runnerCheckpointHasMaterialProgress(entry.Message) {
		state.ProgressSinceCondition = true
	}
	if stringValue(entry.Message["toolName"]) != "" {
		switch stringValue(entry.Message["toolPhase"]) {
		case "failed", prestartToolFailurePhase, "completed":
			state.AttemptedSinceCondition = true
		}
	}
	correction, found := latestRunnerCorrection([]eventjournal.Entry{entry})
	if !found {
		return
	}
	if correction.ReasonCode == sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode &&
		state.ReasonCode != "" && state.ReasonCode != sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode {
		// Protocol repair belongs to the outstanding substantive obligation; it
		// does not replenish that obligation's strategy budget.
		return
	}
	fingerprint := runnerCorrectionFingerprint(correction.cause())
	if state.Fingerprint != fingerprint {
		*state = runnerCorrectionRepetition{ReasonCode: correction.ReasonCode, Fingerprint: fingerprint}
	} else if int(numberValue(entry.Message["recovery_contract_revision"])) >= sessionRunnerRecoveryContractRevision &&
		state.AttemptedSinceCondition && !state.ProgressSinceCondition {
		state.StalledCycles++
	} else {
		state.StalledCycles = 0
	}
	state.Count++
	state.AttemptedSinceCondition, state.ProgressSinceCondition = false, false
}

func (state runnerCorrectionRepetition) waitsForChangedCondition(cause transcriptstore.RunnerInterruptionCause) bool {
	state.observe(eventjournal.Entry{SourceEventType: "runner_checkpoint", RuntimeProjection: cause, Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted", "reason_code": cause.ReasonCode,
		"resume_detail": cause.Detail, "recovery_contract_revision": sessionRunnerRecoveryContractRevision,
	}})
	return state.StalledCycles >= sessionRunnerConsecutiveIdenticalToolRoundBudget
}

// Count only the current unchanged obligation. A new typed obligation or user
// task opens a new scope; unrelated tool successes and clarifications do not.
func runnerRepeatedCorrectionInterruptionCount(entries []eventjournal.Entry, correction transcriptstore.RunnerInterruptionCause) int {
	var state runnerCorrectionRepetition
	for _, entry := range entries {
		state.observe(entry)
	}
	if state.Fingerprint != runnerCorrectionFingerprint(correction) {
		return 0
	}
	return state.Count
}
