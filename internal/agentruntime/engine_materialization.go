package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"unicode/utf8"

	"synon-go/internal/toolcontract"
)

// MaterializeToolResult applies the same inline budget, immutable large-result
// authority, media extraction, and outcome contract used by live tool
// execution. Durable recovery must call this method rather than constructing a
// second ad-hoc model result.
func (e Engine) MaterializeToolResult(
	ctx context.Context,
	call ToolCall,
	value any,
	outcome ToolResultOutcome,
) (MaterializedToolResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		outcome = ToolResultFailed
	}
	return e.MaterializeRawToolResult(ctx, call, raw, outcome)
}

// MaterializeRawToolResult materializes already-canonical JSON without
// reparsing and re-encoding it. Durable recovery uses this entry point so an
// exact result persisted before a lifecycle interruption can be replayed
// through the same large-result authority and descriptor validation as live
// execution.
func (e Engine) MaterializeRawToolResult(
	ctx context.Context,
	call ToolCall,
	raw json.RawMessage,
	outcome ToolResultOutcome,
) (MaterializedToolResult, error) {
	if !json.Valid(raw) {
		return MaterializedToolResult{}, e.largeToolResultError(call, errors.New("tool result JSON is invalid"))
	}
	resultRef := ""
	if e.MaxToolResultBytes > 0 && int64(len(raw)) > e.MaxToolResultBytes {
		if e.LargeToolResults == nil {
			return MaterializedToolResult{}, e.largeToolResultError(call, ErrLargeToolResultAuthorityUnavailable)
		}
		descriptor, authorityErr := e.LargeToolResults.Externalize(ctx, LargeToolResultInput{
			ToolCall: call, RawJSON: raw, Outcome: outcome,
			MaxInlineBytes: e.MaxToolResultBytes,
		})
		if authorityErr != nil {
			return MaterializedToolResult{}, e.largeToolResultError(call, authorityErr)
		}
		encodedDescriptor, err := json.Marshal(descriptor)
		if err != nil {
			return MaterializedToolResult{}, e.largeToolResultError(call, fmt.Errorf("marshal descriptor: %w", err))
		}
		if int64(len(encodedDescriptor)) > e.MaxToolResultBytes {
			return MaterializedToolResult{}, e.largeToolResultError(call, fmt.Errorf(
				"large tool result descriptor exceeds %d byte inline budget", e.MaxToolResultBytes,
			))
		}
		if descriptorErr := e.validateLargeToolResultDescriptor(ctx, call, descriptor, raw, outcome); descriptorErr != nil {
			return MaterializedToolResult{}, e.largeToolResultError(call, descriptorErr)
		}
		raw = encodedDescriptor
		resultRef = "artifact-version:" + descriptor.VersionID
	}
	digest := sha256.Sum256(raw)
	return MaterializedToolResult{
		JSON: append(json.RawMessage(nil), raw...), SHA256: hex.EncodeToString(digest[:]),
		ResultRef: resultRef, Outcome: outcome,
	}, nil
}

func (e Engine) toolResultMessage(ctx context.Context, call ToolCall, value any, outcome ToolResultOutcome) (Message, error) {
	materialized, err := e.MaterializeToolResult(ctx, call, value, outcome)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    string(materialized.JSON),
	}, nil
}

func (e Engine) largeToolResultError(call ToolCall, cause error) error {
	return &LargeToolResultInfrastructureError{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Code:       largeToolResultFailureCode(cause),
		cause:      cause,
	}
}

func largeToolResultFailureCode(cause error) string {
	if errors.Is(cause, ErrLargeToolResultAuthorityUnavailable) {
		return "large_tool_result_authority_unavailable"
	}
	if cause == nil {
		return "large_tool_result_unknown"
	}
	switch cause.Error() {
	case "tool result JSON is invalid":
		return "large_tool_result_invalid_json"
	case "trusted materialized tool result is invalid":
		return "large_tool_result_trusted_envelope_invalid"
	case "trusted materialized tool result digest is invalid":
		return "large_tool_result_trusted_digest_mismatch"
	case "trusted inline tool result conflicts with its value":
		return "large_tool_result_trusted_inline_conflict"
	case "trusted externalized tool result descriptor is invalid":
		return "large_tool_result_trusted_descriptor_invalid"
	default:
		return "large_tool_result_authority_failed"
	}
}

func (e Engine) validateLargeToolResultDescriptor(ctx context.Context, call ToolCall, descriptor LargeToolResultDescriptor, raw []byte, outcome ToolResultOutcome) error {
	if err := toolcontract.ValidateExternalizedResultDescriptor(toolcontract.ExternalizedResultDescriptor{
		ArtifactID: descriptor.ArtifactID, VersionID: descriptor.VersionID, SHA256: descriptor.SHA256,
		SizeBytes: descriptor.SizeBytes, ContentType: descriptor.ContentType, Outcome: string(descriptor.Outcome),
		ContentURL: descriptor.ContentURL, ReadWith: descriptor.ReadWith,
		Preview: descriptor.Preview, Truncated: descriptor.Truncated,
	}); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if descriptor.SHA256 != hex.EncodeToString(digest[:]) || descriptor.SizeBytes != int64(len(raw)) ||
		descriptor.Outcome != outcome ||
		!utf8.ValidString(descriptor.Preview) {
		return errors.New("large tool result authority returned an invalid descriptor")
	}
	if !bytes.HasPrefix(raw, []byte(descriptor.Preview)) {
		validator, trusted := e.LargeToolResults.(LargeToolResultPreviewValidator)
		if !trusted {
			return errors.New("large tool result authority returned an unverified preview")
		}
		if err := validator.ValidatePreview(ctx, LargeToolResultInput{
			ToolCall: call, RawJSON: raw, Outcome: outcome, MaxInlineBytes: e.MaxToolResultBytes,
		}, descriptor); err != nil {
			return err
		}
	}
	return nil
}
