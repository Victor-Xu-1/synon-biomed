package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"synon-go/internal/toolcontract"
	"synon-go/internal/toolprogress"
)

// executeToolGatewayWithProgress keeps the gateway call alive while the
// runtime emits periodic progress events. This is deliberately a wait-loop,
// not a timeout: a large program or a scientific computation must continue
// until the gateway returns its durable result. Progress callback failures are
// best-effort telemetry and must not turn a still-running computation into a
// tool failure.
func (e Engine) executeToolGatewayWithProgress(ctx context.Context, call ToolCall) (ToolResult, error) {
	if e.Tools == nil {
		return ToolResult{}, errors.New("agent runtime tool gateway is required")
	}
	return toolprogress.Observe(ctx, e.ToolProgressInterval, func(ctx context.Context) (ToolResult, error) {
		return e.Tools.Execute(ctx, call)
	}, func(progress *toolprogress.Update, elapsed time.Duration, ordinal int) {
		e.emitToolProgress(Event{Type: EventToolProgress, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments), Elapsed: elapsed, ProgressOrdinal: ordinal, Progress: progress})
	})
}

func (e Engine) emitToolProgress(event Event) {
	if e.OnEventError != nil {
		// A progress checkpoint is observability only. The underlying gateway
		// call remains authoritative and must not be interrupted by a transient
		// journal or projection failure.
		_ = e.OnEventError(event)
	}
	if e.OnEvent != nil {
		e.OnEvent(event)
	}
}

func (e Engine) validateTrustedMaterializedToolResult(
	call ToolCall,
	value any,
	outcome ToolResultOutcome,
	materialized MaterializedToolResult,
) error {
	if !json.Valid(materialized.JSON) || materialized.Outcome != outcome ||
		(e.MaxToolResultBytes > 0 && int64(len(materialized.JSON)) > e.MaxToolResultBytes) {
		return e.largeToolResultError(call, errors.New("trusted materialized tool result is invalid"))
	}
	digest := sha256.Sum256(materialized.JSON)
	if materialized.SHA256 != hex.EncodeToString(digest[:]) {
		return e.largeToolResultError(call, errors.New("trusted materialized tool result digest is invalid"))
	}
	if materialized.ResultRef == "" {
		raw, err := json.Marshal(value)
		if err != nil || !bytes.Equal(raw, materialized.JSON) {
			return e.largeToolResultError(call, errors.New("trusted inline tool result conflicts with its value"))
		}
		return nil
	}
	descriptor, resultRef, found, err := toolcontract.DecodeExternalizedResult(materialized.JSON)
	if err != nil || !found || materialized.ResultRef != resultRef || descriptor.Outcome != string(outcome) {
		return e.largeToolResultError(call, errors.New("trusted externalized tool result descriptor is invalid"))
	}
	return nil
}

func validateToolCall(call ToolCall) error {
	if strings.TrimSpace(call.Name) == "" {
		return errors.New("tool call name is required")
	}
	arguments := bytes.TrimSpace(call.Arguments)
	if len(arguments) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(arguments, &decoded); err != nil {
		return fmt.Errorf("tool call arguments must be valid JSON: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return errors.New("tool call arguments must be a JSON object")
	}
	return nil
}

func (e Engine) emit(event Event) error {
	if e.OnEventError != nil {
		if err := e.OnEventError(event); err != nil {
			return err
		}
	}
	if e.OnEvent != nil {
		e.OnEvent(event)
	}
	return nil
}

func (e Engine) emitLifecycleEvent(ctx context.Context, event Event) error {
	if err := e.emit(event); err != nil {
		if contextErr := context.Cause(ctx); contextErr != nil {
			return contextErr
		}
		// Tool completion, failure, and pause checkpoints are part of the
		// provider protocol, not best-effort telemetry. Continuing after one
		// of these callbacks fails lets the model observe a result that the
		// durable transcript never recorded, leaving open batches or approval
		// requests behind a seemingly completed task.
		return err
	}
	return nil
}
