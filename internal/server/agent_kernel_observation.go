package server

import (
	"errors"

	"synon-go/internal/executionprep"
	kernelruntime "synon-go/internal/kernel"
)

// Only the kernel host can witness that a proof-bearing invocation terminated
// before its execution acknowledgement. Never accept a guest/model claim alone,
// or publish partial execution as a non-executing diagnostic refusal.
func agentKernelObservationRefusal(outcome kernelruntime.ExecutionOutcome, exitStatus string) (map[string]any, error) {
	if !outcome.ObservationRefused {
		return nil, nil
	}
	if exitStatus != "ok" || !outcome.StartedAt.IsZero() || outcome.Err != nil || outcome.Response.Error != "" ||
		outcome.Response.Stdout != "" || outcome.Response.Stderr != "" || len(outcome.FilesWritten) != 0 ||
		!executionprep.IsObservationDeclined(outcome.Response.Preflight) {
		return nil, errors.New("kernel observation refusal witness is invalid")
	}
	result := executionprep.ObservationDeclined(stringValue(outcome.Response.Preflight["reason"]))
	// The established durable preflight protocol recognizes this suffix. The
	// pending scientific decision remains explicit rather than a tool retry.
	result["status"] = "execution_observation_preflight_required"
	result["code"] = "implementation_selection_required"
	return result, nil
}
