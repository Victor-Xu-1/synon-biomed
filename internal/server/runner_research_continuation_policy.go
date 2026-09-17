package server

// researchContinuationRequiresExecution keeps backward compatibility with
// source continuations persisted before the required flag existed. An
// explicitly advisory continuation remains visible in plan state, but cannot
// steer tool input, force tool choice, or be replayed by protocol recovery.
func researchContinuationRequiresExecution(continuation map[string]any) bool {
	if len(continuation) == 0 {
		return false
	}
	required, recorded := continuation["required"].(bool)
	return !recorded || required
}

// researchContinuationAsAdvisory preserves the source-owned route and its raw
// diagnostics after the model explicitly completes a research step. This is
// quality feedback, not runtime authority to reopen the step or execute the
// route without a new model decision.
func researchContinuationAsAdvisory(continuation map[string]any) map[string]any {
	if len(continuation) == 0 {
		return nil
	}
	advisory := copyMapAny(continuation)
	advisory["required"] = false
	advisory["blocking"] = false
	advisory["quality_advisory"] = true
	return advisory
}
