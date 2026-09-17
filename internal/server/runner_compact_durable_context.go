package server

import (
	"fmt"
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
)

// compactDurableRuntimeContextLines projects the bounded server-derived tool
// ledger into both deterministic and model-generated compaction inputs. This
// keeps exact failures, accepted outputs, workspace paths, and unresolved work
// available even when an optional summarizer times out. Immutable checkpoints
// remain the evidence authority; this is only their compact continuation view.
func compactDurableRuntimeContextLines(entries []eventjournal.Entry) ([]string, error) {
	records, err := sessionRunnerToolContinuityRecords(entries)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	lines := []string{"Durable execution and repair state:"}
	lines = append(lines, strings.Split(sessionRunnerToolContinuityContext(records), "\n")...)
	latest := records[len(records)-1]
	if !latest.Successful {
		lines = append(lines, fmt.Sprintf(
			"Pending work: repair the latest failed tool call %s (%s) from event %d using its exact failureDiagnostic, then rerun only the affected governed step.",
			latest.ToolCallID, latest.ToolName, latest.EventID,
		))
	} else {
		lines = append(lines, fmt.Sprintf(
			"Continuation status: the latest governed tool call %s (%s) succeeded at event %d. Do not create a new inventory, repeat a source lookup, regenerate an artifact, or request software approval for the same capability unless a later immutable receipt proves its input, output, or validation changed; if no unresolved step remains, finalize the task and release task resources.",
			latest.ToolCallID, latest.ToolName, latest.EventID,
		))
	}
	return lines, nil
}
