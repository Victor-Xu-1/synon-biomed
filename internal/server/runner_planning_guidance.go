package server

// sessionRunnerPlanningGuidance names only the control boundary. Research
// depth, source routing, and module continuity belong to structured tool
// state; duplicating those mechanics in a long prompt makes the model less
// reliable and creates a second, unenforceable implementation.
func sessionRunnerPlanningGuidance(planReviewEnabled bool) string {
	guidance := "For multi-stage work, record one concise working plan and keep its structured step state current while executing. Use research steps for source-backed investigation, synthesis steps for combining recorded findings, and delivery steps for final outputs."
	if planReviewEnabled {
		return guidance + " This request uses explicit plan review; wait for approval before execution."
	}
	return guidance + " The working plan returns immediately; continue without waiting for approval."
}
