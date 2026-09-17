package agentruntime

import (
	"fmt"
	"sync"
)

// ToolRoundBudget is an attempt-scoped, concurrency-safe budget that can be
// shared by multiple Engine.Run calls. A non-positive limit is unlimited.
type ToolRoundBudget struct {
	mu    sync.Mutex
	limit int
	used  int
}

func NewToolRoundBudget(limit int) *ToolRoundBudget {
	if limit < 0 {
		limit = 0
	}
	return &ToolRoundBudget{limit: limit}
}

func (b *ToolRoundBudget) consume() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit == 0 {
		return nil
	}
	if b.used >= b.limit {
		return &ToolRoundLimitError{Limit: b.limit}
	}
	b.used++
	return nil
}

func (b *ToolRoundBudget) Remaining() (int, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit == 0 {
		return 0, false
	}
	remaining := b.limit - b.used
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

type ToolRoundLimitError struct {
	Limit int
}

// ToolRoundNoProgressError is returned when a model repeats the same tool
// call round without emitting any content or changing its arguments. A hung
// agent must not burn an unbounded tool budget on an identical loop (for
// example repeated TodoWrite updates while claiming to start a search).
type ToolRoundNoProgressError struct {
	Limit int
	// Calls contains the bounded semantic execution identities that returned no
	// progress in this unit. The outer runner persists their fingerprints so a
	// resumed Engine cannot unknowingly reopen the same route.
	Calls []ToolCall
}

type ToolCallBatchLimitError struct {
	Limit    int
	Received int
}

type ToolCallBatchValidationError struct {
	Index int
	Code  string
}

func (e *ToolCallBatchLimitError) Error() string {
	if e == nil {
		return "agent runtime tool call batch limit exceeded"
	}
	return fmt.Sprintf("agent runtime received %d tool calls in one round; limit is %d", e.Received, e.Limit)
}

func (e *ToolCallBatchValidationError) Error() string {
	if e == nil {
		return "agent runtime tool call batch is invalid"
	}
	return fmt.Sprintf("agent runtime tool call batch item %d is invalid: %s", e.Index, e.Code)
}

func (e *ToolRoundLimitError) Error() string {
	if e == nil {
		return "agent runtime tool call round limit exceeded"
	}
	return fmt.Sprintf("agent runtime exceeded %d tool call rounds", e.Limit)
}

func (e *ToolRoundNoProgressError) Error() string {
	if e == nil {
		return "agent runtime tool call round made no semantic progress"
	}
	return fmt.Sprintf("agent runtime made no semantic progress for %d consecutive identical tool call rounds", e.Limit)
}
