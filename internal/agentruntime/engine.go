package agentruntime

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"strings"
	"time"

	"synon-go/internal/toolprogress"
)

const (
	maxOpenAIChatResponseBytes         = 4 << 20
	maxInitialToolChoiceRepairAttempts = 2
	// Bound consecutive verification bounces so private repair cannot become
	// an invisible loop: three corrections may repair
	// an invalid proposal, while a fourth unchanged rejection becomes visible
	// so the run cannot loop forever.
	maxPrivatePreflightRepairAttempts = 3
)

// ToolMediaContextNotice identifies the synthetic user message that carries
// rich media returned by trusted file-inspection tools. Provider adapters must
// encode these parts faithfully or return the provider capability error; media
// is never silently removed or projected into a second text-only path.
const ToolMediaContextNotice = "Visual content returned by file inspection tools."

type Message struct {
	Role               string             `json:"role"`
	Content            string             `json:"content,omitempty"`
	Parts              []ContentPart      `json:"parts,omitempty"`
	ContextUsageSource ContextUsageSource `json:"-"`
	ReasoningContent   string             `json:"-"`
	ToolCallID         string             `json:"tool_call_id,omitempty"`
	ToolCalls          []ToolCall         `json:"tool_calls,omitempty"`
	pending            []ContentPart
	terminal           bool
	noProgress         bool
}

// ContextUsageSource is request-local provenance for usage attribution. It is
// never part of provider payloads or durable conversation history.
type ContextUsageSource string

const (
	ContextUsageSystemPrompt ContextUsageSource = "systemPrompt"
	ContextUsageMessages     ContextUsageSource = "messages"
	ContextUsageMCP          ContextUsageSource = "mcp"
	ContextUsageSkills       ContextUsageSource = "skills"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Resumed marks a durable call whose start boundary was committed by an
	// earlier runner attempt. Recovery executes the exact same call without
	// publishing a second start event, so one call never becomes two paths.
	Resumed bool `json:"-"`
	// VerifiedEvidence is set only after a server-owned durable evidence
	// attestation has been validated. It is never sent to the model or encoded
	// into provider requests.
	VerifiedEvidence bool `json:"-"`
	// RejectedBeforeExecution marks a model-selected call that the runtime
	// converted into a model-visible failure result without invoking the tool.
	// Durable consumers must record the call/result pair, but must not allocate
	// an execution authority or infer that a side effect could have started.
	RejectedBeforeExecution bool `json:"-"`
	// ProviderProtocolDiagnostic carries a bounded adapter-side parse failure
	// for a call that still has a usable call ID and name. Arguments are replaced
	// with a safe object so the call/result pair remains durably replayable.
	ProviderProtocolDiagnostic string `json:"-"`
	// RuntimeRecovered marks a deterministic control transition reconstructed
	// by the trusted gateway after a provider repeatedly ignored an exact named
	// tool requirement. It is never set for scientific, source, or user-owned
	// actions and is retained only for truthful lifecycle attribution.
	RuntimeRecovered bool `json:"-"`
}

// ToolExposure keeps an executable Tool registered while making its
// model-facing availability explicit. The runtime captures one complete Tool
// authority snapshot for a turn and derives the visible projection from this
// field; plans, Skills, and recovery code must not maintain parallel allowlists.
type ToolExposure string

const (
	ToolExposureDirect   ToolExposure = "direct"
	ToolExposureDeferred ToolExposure = "deferred"
	ToolExposureHidden   ToolExposure = "hidden"
)

type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	// OutputSchema is retained for Harness-side Skills and result-shape
	// guidance. Provider function declarations intentionally omit it because
	// OpenAI-compatible tool schemas do not admit an output contract field.
	OutputSchema map[string]any `json:"-"`
	// Capabilities describe the executable runtime contract, not task intent.
	// They are retained in durable snapshots and intentionally omitted from
	// provider function declarations by each provider adapter.
	Capabilities []string `json:"capabilities,omitempty"`
	// Exposure is resolved once by the model Tool projection. Empty preserves
	// the direct visibility of older in-process/test schemas during migration.
	Exposure ToolExposure `json:"exposure,omitempty"`
}

func (schema ToolSchema) EffectiveExposure() ToolExposure {
	switch schema.Exposure {
	case ToolExposureDeferred, ToolExposureHidden:
		return schema.Exposure
	default:
		return ToolExposureDirect
	}
}

type RunRequest struct {
	Messages []Message
	Tools    []ToolSchema
	// InitialToolChoice applies only to the first model request in this run.
	// Subsequent rounds always return to provider-auto selection so a required
	// recovery action cannot accidentally pin the whole task to one tool.
	InitialToolChoice                 any
	MaxToolRounds                     int
	MaxToolCallsPerRound              int
	MaxConsecutiveIdenticalToolRounds int
	ToolRoundBudget                   *ToolRoundBudget
	Metadata                          map[string]any
	Headers                           map[string]string
	MediaPolicy                       MediaPolicy
}

type RunResult struct {
	FinalMessage Message
	Messages     []Message
}

type ModelRequest struct {
	Messages    []Message
	Tools       []ToolSchema
	Metadata    map[string]any
	Headers     map[string]string
	MediaPolicy MediaPolicy
	MaxTokens   int
	Temperature *float64
	ToolChoice  any
	// ReasoningMode is an execution hint for provider adapters. The default
	// preserves the selected model profile. Disabled is reserved for short,
	// user-visible presentation turns where hidden reasoning would consume the
	// entire output budget without producing public content.
	ReasoningMode ReasoningMode
}

type ReasoningMode string

const (
	ReasoningModeDefault  ReasoningMode = ""
	ReasoningModeDisabled ReasoningMode = "disabled"
)

type ModelResponse struct {
	Message    Message
	Model      string
	RequestID  string
	StopReason string
	Usage      ModelUsage
}

type ModelUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`
}

type ModelClient interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}

type StreamingModelClient interface {
	ModelClient
	CompleteStream(context.Context, ModelRequest, func(ModelStreamEvent) error) (ModelResponse, error)
}

type ToolResult struct {
	Value        any
	Parts        []ContentPart
	Materialized *MaterializedToolResult
	// ExecutedArguments is the canonical JSON object that reached the tool after
	// runtime normalization. A nil value means the requested ToolCall arguments
	// were executed unchanged. Terminal lifecycle events use this value so
	// durable receipts describe the operation that actually ran.
	ExecutedArguments json.RawMessage
	// ModelContext augments only the transient model-facing tool result. Durable
	// lifecycle events retain the exact original result bytes and evidence
	// classification. Keeping the context in the same tool role avoids a trailing
	// synthetic assistant turn and does not elevate model-authored state to policy.
	ModelContext any
	// Terminal ends the agent run after this successful tool result has been
	// durably emitted. It is reserved for protocol completion tools such as a
	// fixed-job submit_output; failed results remain model-correctable.
	Terminal bool
}

// LargeToolResultInput carries the one exact JSON encoding produced for a
// tool result. Authorities persist these bytes without decoding or remarshal.
type LargeToolResultInput struct {
	ToolCall ToolCall
	// RawJSON is a borrowed, read-only buffer valid only for the duration of
	// Externalize. Authorities must not modify or retain it; copy explicitly
	// when ownership beyond the synchronous call is required.
	RawJSON        []byte
	Outcome        ToolResultOutcome
	MaxInlineBytes int64
}

// LargeToolResultDescriptor is the complete, strict model-context contract
// for an externalized tool result. ContentURL addresses this exact immutable
// artifact version rather than the artifact's mutable latest version.
type LargeToolResultDescriptor struct {
	ArtifactID  string            `json:"artifact_id"`
	VersionID   string            `json:"version_id"`
	SHA256      string            `json:"sha256"`
	SizeBytes   int64             `json:"size_bytes"`
	ContentType string            `json:"content_type"`
	Outcome     ToolResultOutcome `json:"outcome"`
	ContentURL  string            `json:"content_url"`
	// ReadWith gives the model the exact bounded retrieval contract for the
	// immutable full result. A URL alone is a UI/download address and proved too
	// easy to mistake for content that had already been inspected.
	ReadWith  string `json:"read_with,omitempty"`
	Preview   string `json:"preview"`
	Truncated bool   `json:"truncated"`
}

// MaterializedToolResult is the exact canonical terminal value delivered to
// the model and persisted by durable tool authorities. SHA256 always hashes
// JSON itself. For an externalized value, ResultRef addresses the immutable
// artifact version while the descriptor's SHA256 continues to hash the full
// original result bytes.
type MaterializedToolResult struct {
	JSON      json.RawMessage
	SHA256    string
	ResultRef string
	Outcome   ToolResultOutcome
}

// LargeToolResultAuthority is the only authority allowed to replace an
// oversized inline result with an immutable artifact-version descriptor.
// Externalize receives a borrowed RawJSON buffer under the lifetime and
// immutability contract documented on LargeToolResultInput.
type LargeToolResultAuthority interface {
	Externalize(context.Context, LargeToolResultInput) (LargeToolResultDescriptor, error)
}

// LargeToolResultPreviewValidator is an optional trusted-runtime extension.
// Non-prefix previews must be reproducible from the same original RawJSON.
// Plugins and model-authored result fields cannot grant this capability.
// The engine still validates source digest, size, outcome and descriptor shape.
type LargeToolResultPreviewValidator interface {
	ValidatePreview(context.Context, LargeToolResultInput, LargeToolResultDescriptor) error
}

type FuncLargeToolResultAuthority func(context.Context, LargeToolResultInput) (LargeToolResultDescriptor, error)

func (fn FuncLargeToolResultAuthority) Externalize(ctx context.Context, input LargeToolResultInput) (LargeToolResultDescriptor, error) {
	return fn(ctx, input)
}

var ErrLargeToolResultAuthorityUnavailable = errors.New("large tool result authority is unavailable")

// LargeToolResultInfrastructureError keeps persistence/authorization failures
// out of the tool's business-result channel.
type LargeToolResultInfrastructureError struct {
	ToolCallID string
	ToolName   string
	Code       string
	cause      error
}

func (err *LargeToolResultInfrastructureError) Error() string {
	return "large tool result infrastructure failure"
}

func (err *LargeToolResultInfrastructureError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func (err *LargeToolResultInfrastructureError) ReasonCode() string {
	if err == nil || strings.TrimSpace(err.Code) == "" {
		return "large_tool_result_unknown"
	}
	return err.Code
}

type ToolGateway interface {
	Execute(context.Context, ToolCall) (ToolResult, error)
}

type FuncToolGateway func(context.Context, ToolCall) (ToolResult, error)

func (fn FuncToolGateway) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	return fn(ctx, call)
}

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

type Engine struct {
	Model              ModelClient
	Tools              ToolGateway
	MaxToolResultBytes int64
	LargeToolResults   LargeToolResultAuthority
	OnEvent            func(Event)
	OnEventError       func(Event) error
	OnModelDelta       func(ModelStreamEvent) error
	// AllowToolPreamble lets the publication owner approve a completed,
	// non-final tool-round preamble independently of tool-choice admission.
	// It never admits or executes the requested tool. Nil retains buffering.
	AllowToolPreamble func(string) bool
	// ToolProgressInterval controls how often a running tool emits a durable
	// progress event. A non-positive value uses the runtime default.
	ToolProgressInterval time.Duration
}

type PauseError struct {
	Status  string
	Message string
	Data    map[string]any
}

// ToolBatchExecution is the resumable result of executing one immutable
// assistant tool-call batch. NextOrdinal identifies the first call that has
// not reached a protocol terminal result. A PauseError therefore leaves the
// cursor on the paused call; callers must persist that cursor before ending
// the runner attempt.
type ToolBatchExecution struct {
	Messages       []Message
	NextOrdinal    int
	MediaBytesUsed int64
	Terminal       bool
	// NoProgress is true only when every call in this batch returned the same
	// previously observed idempotent read. The live engine uses it to bound a
	// model that keeps narrating while repeating an already available source.
	NoProgress bool
}

// InitialToolChoiceViolationError reports a provider response that ignored a
// server-required or server-closed tool choice after bounded in-run protocol
// repair. The engine never executes a call while tool choice is closed and
// never chooses scientific tool arguments on the model's behalf.
type InitialToolChoiceViolationError struct {
	RequiredTool   string
	ToolsForbidden bool
	Attempts       int
}

func (e *InitialToolChoiceViolationError) Error() string {
	if e != nil && e.ToolsForbidden {
		return fmt.Sprintf("agent runtime forbade tool calls after %d attempts", e.Attempts)
	}
	if e != nil && strings.TrimSpace(e.RequiredTool) != "" {
		return fmt.Sprintf("agent runtime required an initial %s tool call after %d attempts", e.RequiredTool, e.Attempts)
	}
	if e == nil {
		return "agent runtime initial tool choice was not satisfied"
	}
	return fmt.Sprintf("agent runtime required an initial tool call after %d attempts", e.Attempts)
}

func (e *PauseError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "agent runtime paused"
	}
	return e.Message
}
