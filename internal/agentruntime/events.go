package agentruntime

import (
	"time"

	"synon-go/internal/toolprogress"
)

type EventType string

const (
	EventModelRequest           EventType = "model_request"
	EventModelDelta             EventType = "model_delta"
	EventModelResponse          EventType = "model_response"
	EventPresentationDiagnostic EventType = "presentation_diagnostic"
	EventToolStarted            EventType = "tool_started"
	EventToolProgress           EventType = "tool_progress"
	EventToolCompleted          EventType = "tool_completed"
	EventToolFailed             EventType = "tool_failed"
	EventToolPaused             EventType = "tool_paused"
	EventFinal                  EventType = "final"
)

type Event struct {
	Type              EventType
	ToolName          string
	ToolCallID        string
	Message           string
	Arguments         string
	ExecutedArguments string
	Result            string
	ToolCalls         []ToolCall
	// RejectedBeforeExecution distinguishes a model-visible protocol/policy
	// result from a tool that reached its execution start boundary.
	RejectedBeforeExecution bool
	// Elapsed is populated for EventToolProgress and is intentionally kept as
	// runtime metadata rather than model-visible tool output.
	Elapsed time.Duration
	// ProgressOrdinal makes durable progress checkpoints idempotent within one
	// tool call while allowing a long-running call to emit more than one update.
	ProgressOrdinal int
	// Progress carries optional observed detail from the executing tool. A nil
	// value is a liveness heartbeat only; determinate percentages are never
	// synthesized by the runtime.
	Progress *toolprogress.Update
}
