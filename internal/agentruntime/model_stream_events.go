package agentruntime

type ModelStreamEventKind string

const (
	ModelStreamEventContentDelta           ModelStreamEventKind = "content_delta"
	ModelStreamEventPublicProgressDelta    ModelStreamEventKind = "public_progress_delta"
	ModelStreamEventPublicProgressBoundary ModelStreamEventKind = "public_progress_boundary"
	ModelStreamEventPrivateReasoning       ModelStreamEventKind = "private_reasoning"
	ModelStreamEventToolCallBoundary       ModelStreamEventKind = "tool_call_boundary"
)

type ModelStreamEvent struct {
	// Kind identifies the provider fact that made this event observable. Empty
	// remains compatible with older model clients and is inferred only from the
	// two legacy fields below.
	Kind ModelStreamEventKind
	// BlockID identifies one explicitly public progress envelope. It is empty
	// for final-candidate text, private reasoning, and provider tool boundaries.
	BlockID      string
	ContentDelta string
	// ReasoningActive reports only that the provider is producing a private
	// reasoning stream. The reasoning bytes are deliberately absent and must
	// never be projected into user-visible content or audit logs.
	ReasoningActive bool
}
