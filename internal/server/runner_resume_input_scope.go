package server

import transcriptstore "synon-go/internal/persistence/transcript"

// runnerResumeContinuesPriorInput is the single boundary for carrying recovery
// state from one runner attempt into the next. Checkpoints, correction context,
// and immutable completion candidates belong to the input revision that
// created them; a later user task must never inherit any of those values.
func runnerResumeContinuesPriorInput(
	authority *transcriptRunnerAuthority,
	previous transcriptstore.RunnerRuntimeState,
) bool {
	if authority == nil || authority.Claim.ClaimedInputRevision <= 0 || previous.ClaimedInputRevision <= 0 {
		return false
	}
	return authority.Claim.ClaimedInputRevision == previous.ClaimedInputRevision
}
