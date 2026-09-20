package server

import transcriptstore "synon-go/internal/persistence/transcript"

const sessionRunnerFinalPresentationReasonCode = "response_presentation_invalid"

// Only protocol metadata belongs here. Never embed provider text or executable
// arguments in the error, public reason, or correction instruction.
type sessionRunnerPresentationViolation struct{ Code string }

func (failure sessionRunnerPresentationViolation) Error() string {
	return "model response progress contract is invalid"
}

// Only an unusable final candidate requires a replacement response. Optional
// progress failures with valid actions never implement this correction contract.
type sessionRunnerFinalPresentationCorrection struct{}

func (sessionRunnerFinalPresentationCorrection) Error() string {
	return "final response presentation is invalid"
}

func (sessionRunnerFinalPresentationCorrection) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	return newRunnerTextCorrection(sessionRunnerFinalPresentationReasonCode,
		"replace only the final response presentation using the existing completed evidence; do not repeat completed scientific operations or publish malformed progress control fields")
}
