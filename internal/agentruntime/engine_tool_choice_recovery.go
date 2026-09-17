package agentruntime

import "strings"

// resolveToolChoiceViolation keeps provider-protocol repair separate from the
// main execution loop. Before the private repair budget is exhausted it returns
// the next transient repair message. Afterwards it permits only an exact named
// call reconstructed from durable gateway state; generic required choices and
// forbidden-tool violations remain fail-closed.
func resolveToolChoiceViolation(
	constraint initialToolConstraint,
	repairAttempts int,
	recovery RequiredToolCallRecovery,
	hasRecovery bool,
	messages []Message,
	tools []ToolSchema,
) (Message, bool, error) {
	if repairAttempts < maxInitialToolChoiceRepairAttempts {
		return constraint.repairMessage(repairAttempts + 1), true, nil
	}
	call, recovered := ToolCall{}, false
	if !constraint.forbidden && hasRecovery && constraint.name != "" {
		call, recovered = recovery.RecoverRequiredToolCall(constraint.name, messages, tools)
		recovered = recovered && strings.TrimSpace(call.ID) != "" &&
			strings.EqualFold(strings.TrimSpace(call.Name), constraint.name)
	}
	if !recovered {
		return Message{}, false, &InitialToolChoiceViolationError{
			RequiredTool:   constraint.name,
			ToolsForbidden: constraint.forbidden,
			Attempts:       repairAttempts + 1,
		}
	}
	call.RuntimeRecovered = true
	return Message{Role: "assistant", ToolCalls: []ToolCall{call}}, false, nil
}
