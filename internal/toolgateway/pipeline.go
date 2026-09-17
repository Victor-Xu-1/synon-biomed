// Package toolgateway owns the single ordered tool-execution pipeline used by
// every Synon Biomed Harness entry point. Domain adapters provide stage
// handlers, but they cannot reorder or bypass the canonical stages.
package toolgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Stage is one immutable phase in the canonical Harness tool lifecycle.
type Stage string

const (
	StageNormalize     Stage = "normalize"
	StageAdmit         Stage = "admit"
	StagePreflight     Stage = "preflight"
	StageFailureBudget Stage = "failure-budget"
	StageReviewScope   Stage = "review-scope"
	StagePreHooks      Stage = "pre-hooks"
	StageRevalidate    Stage = "revalidate"
	StagePermission    Stage = "permission"
	StageSourceBudget  Stage = "source-budget"
	StageExecute       Stage = "execute"
	StageMaterialize   Stage = "materialize"
	StagePostHooks     Stage = "post-hooks"
	StageAudit         Stage = "audit"
)

// OrderedStages is copied by callers that need to publish architecture
// evidence. NewPipeline remains the enforcement authority.
var OrderedStages = []Stage{
	StageNormalize,
	StageAdmit,
	StagePreflight,
	StageFailureBudget,
	StageReviewScope,
	StagePreHooks,
	StageRevalidate,
	StagePermission,
	StageSourceBudget,
	StageExecute,
	StageMaterialize,
	StagePostHooks,
	StageAudit,
}

// Continuation controls the only legal short-circuit routes. Every route ends
// at the durable audit stage; execution outcomes may additionally pass through
// post-tool hooks.
type Continuation uint8

const (
	Continue Continuation = iota
	PostHooksThenAudit
	AuditOnly
)

// Invocation is the transport-neutral state shared by the ordered stages.
// Extension belongs to the adapter and must not be interpreted by this package.
type Invocation struct {
	Context       context.Context
	StartedAt     time.Time
	CallID        string
	RequestedName string
	CanonicalName string
	Arguments     json.RawMessage
	OriginalInput map[string]any
	Input         map[string]any
	Value         any
	Status        string
	ErrorMessage  string
	Err           error
	AuditExtra    map[string]any
	Extension     any

	continuation Continuation
}

// NewInvocation normalizes the context and start timestamp without changing
// any model-provided values.
func NewInvocation(ctx context.Context, callID, requestedName string, arguments json.RawMessage, extension any) *Invocation {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Invocation{
		Context:       ctx,
		StartedAt:     time.Now().UTC(),
		CallID:        callID,
		RequestedName: requestedName,
		Arguments:     arguments,
		Extension:     extension,
		continuation:  Continue,
	}
}

// CompleteForAudit records a terminal decision that must skip execution and
// proceed directly to the durable audit stage.
func (invocation *Invocation) CompleteForAudit(value any, status, errorMessage string, err error) {
	invocation.Value = value
	invocation.Status = status
	invocation.ErrorMessage = errorMessage
	invocation.Err = err
	invocation.continuation = AuditOnly
}

// CompleteWithPostHooks records an execution outcome that must still be
// visible to post-tool hooks before durable audit.
func (invocation *Invocation) CompleteWithPostHooks(value any, status, errorMessage string, err error) {
	invocation.Value = value
	invocation.Status = status
	invocation.ErrorMessage = errorMessage
	invocation.Err = err
	invocation.continuation = PostHooksThenAudit
}

// ContinuationState exposes the current short-circuit route for adapters and
// tests without allowing direct mutation.
func (invocation *Invocation) ContinuationState() Continuation {
	return invocation.continuation
}

// Step binds one required stage to its adapter handler.
type Step struct {
	Stage Stage
	Run   func(*Invocation)
}

// Pipeline contains exactly one handler for every canonical stage.
type Pipeline struct {
	steps []Step
}

// NewPipeline rejects missing, duplicate, reordered or unknown stages.
func NewPipeline(steps ...Step) (*Pipeline, error) {
	if len(steps) != len(OrderedStages) {
		return nil, fmt.Errorf("tool gateway requires %d ordered stages, got %d", len(OrderedStages), len(steps))
	}
	owned := make([]Step, len(steps))
	copy(owned, steps)
	for index, expected := range OrderedStages {
		step := owned[index]
		if step.Stage != expected {
			return nil, fmt.Errorf("tool gateway stage %d must be %q, got %q", index, expected, step.Stage)
		}
		if step.Run == nil {
			return nil, fmt.Errorf("tool gateway stage %q has no handler", expected)
		}
	}
	return &Pipeline{steps: owned}, nil
}

// MustPipeline is reserved for package-level immutable pipeline construction.
func MustPipeline(steps ...Step) *Pipeline {
	pipeline, err := NewPipeline(steps...)
	if err != nil {
		panic(err)
	}
	return pipeline
}

// Run executes the canonical stage order. Short-circuit decisions can only
// skip forward to post-hooks or audit, and audit is always invoked exactly once.
func (pipeline *Pipeline) Run(invocation *Invocation) {
	if pipeline == nil {
		panic("tool gateway pipeline is nil")
	}
	if invocation == nil {
		panic("tool gateway invocation is nil")
	}
	audited := false
	for _, step := range pipeline.steps {
		if !stageEnabled(step.Stage, invocation.continuation) {
			continue
		}
		step.Run(invocation)
		if step.Stage == StagePostHooks && invocation.continuation == PostHooksThenAudit {
			invocation.continuation = AuditOnly
		}
		if step.Stage == StageAudit {
			audited = true
		}
	}
	if !audited {
		panic("tool gateway pipeline completed without audit")
	}
}

func stageEnabled(stage Stage, continuation Continuation) bool {
	if stage == StageAudit {
		return true
	}
	switch continuation {
	case Continue:
		return true
	case PostHooksThenAudit:
		return stage == StagePostHooks
	case AuditOnly:
		return false
	default:
		panic(fmt.Sprintf("unknown tool gateway continuation %d", continuation))
	}
}
