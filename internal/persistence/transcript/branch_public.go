package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrBranchTargetNotFound = errors.New("branch target not found")
var ErrBranchRequestInvalid = errors.New("branch request is invalid")

// ForkFrameUserMessageAtIndex is the public mutation boundary used by HTTP
// adapters. It resolves the immutable source event inside the same SQLite
// write transaction as the branch CAS and replacement append.
type ForkFrameUserMessageAtIndexInput struct {
	StreamUID              string
	OwnerID                string
	SourceBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
	SourceMessageIndex     int64
	ExpectedSourceClientID string
	ReplacementText        string
	RuntimeConfig          map[string]any
	Destinations           []string
}

func (r *Repository) ForkFrameUserMessageAtIndex(
	ctx context.Context,
	input ForkFrameUserMessageAtIndexInput,
) (ForkFrameBranchResult, error) {
	if r == nil || r.db == nil {
		return ForkFrameBranchResult{}, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	if input.StreamUID == "" || input.OwnerID == "" || !validTranscriptBranchID(input.SourceBranchID) ||
		input.SourceMessageIndex < 0 {
		return ForkFrameBranchResult{}, fmt.Errorf("%w: stream, owner, source branch, and nonnegative message index are required", ErrBranchRequestInvalid)
	}
	var result ForkFrameBranchResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		target, err := resolveFrameUserMessageBranchTargetAtIndexConn(
			ctx, conn, stream, input.SourceBranchID, input.SourceMessageIndex,
		)
		if err != nil {
			return err
		}
		if expected := strings.TrimSpace(input.ExpectedSourceClientID); expected != "" && target.Event.ClientMessageID != expected {
			return ErrBranchTargetNotFound
		}
		canonical, requestDigest, branchID, err := normalizeFrameUserMessageBranchInput(ForkFrameUserMessageBranchInput{
			StreamUID: input.StreamUID, OwnerID: input.OwnerID,
			SourceBranchID: input.SourceBranchID, ExpectedActiveBranchID: input.ExpectedActiveBranchID,
			ExpectedGeneration: input.ExpectedGeneration, ClientMutationID: input.ClientMutationID,
			SourceClientMessageID: target.Event.ClientMessageID, SourceMessageIndex: input.SourceMessageIndex,
			ReplacementText: input.ReplacementText, RuntimeConfig: input.RuntimeConfig,
			Destinations: input.Destinations,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBranchRequestInvalid, err)
		}
		result, err = r.forkFrameUserMessageBranchConn(ctx, conn, canonical, requestDigest, branchID)
		return err
	})
	return result, schemaError(err)
}

func resolveFrameUserMessageBranchTargetAtIndexConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	branchID string,
	messageIndex int64,
) (branchSourceTarget, error) {
	var result branchSourceTarget
	found := false
	err := visitBranchWebMessagePositionsConn(ctx, conn, stream.UID, branchID, func(event Event, index int64) (bool, error) {
		if index != messageIndex || event.Type != "user_message" || strings.TrimSpace(event.ClientMessageID) == "" {
			return false, nil
		}
		target, err := resolveFrameUserMessageBranchTargetConn(
			ctx, conn, stream, branchID, event.ClientMessageID,
		)
		if errors.Is(err, ErrEventConflict) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if target.Event.EventID != event.EventID {
			return false, ErrEventConflict
		}
		result, found = target, true
		return true, nil
	})
	if err != nil {
		return branchSourceTarget{}, err
	}
	if !found {
		return branchSourceTarget{}, ErrBranchTargetNotFound
	}
	return result, nil
}

// ForkFrameAskUserAnswerAtTool resolves the exact AskUser tool/result pair and
// visible message index in the same transaction as the branch mutation.
type ForkFrameAskUserAnswerAtToolInput struct {
	StreamUID              string
	OwnerID                string
	SourceBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
	ToolUseID              string
	Response               AskUserBranchResponse
	RuntimeConfig          map[string]any
	Destinations           []string
}

func (r *Repository) ForkFrameAskUserAnswerAtTool(
	ctx context.Context,
	input ForkFrameAskUserAnswerAtToolInput,
) (ForkFrameBranchResult, error) {
	if r == nil || r.db == nil {
		return ForkFrameBranchResult{}, ErrSchemaUnavailable
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	if input.StreamUID == "" || input.OwnerID == "" || !validTranscriptBranchID(input.SourceBranchID) ||
		input.ToolUseID == "" {
		return ForkFrameBranchResult{}, fmt.Errorf("%w: stream, owner, source branch, and AskUser tool are required", ErrBranchRequestInvalid)
	}
	var result ForkFrameBranchResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		target, err := resolveAskUserBranchTargetConn(ctx, conn, stream, input.SourceBranchID, input.ToolUseID)
		if errors.Is(err, ErrEventConflict) {
			return ErrBranchTargetNotFound
		}
		if err != nil {
			return err
		}
		messageIndex, err := branchWebMessageIndexConn(
			ctx, conn, stream.UID, input.SourceBranchID, target.ToolEvent.EventID,
		)
		if err != nil {
			return err
		}
		canonical, requestDigest, branchID, err := normalizeFrameAskUserAnswerBranchInput(ForkFrameAskUserAnswerBranchInput{
			StreamUID: input.StreamUID, OwnerID: input.OwnerID,
			SourceBranchID: input.SourceBranchID, ExpectedActiveBranchID: input.ExpectedActiveBranchID,
			ExpectedGeneration: input.ExpectedGeneration, ClientMutationID: input.ClientMutationID,
			ToolUseID: input.ToolUseID, SourceMessageIndex: messageIndex,
			Response: input.Response, RuntimeConfig: input.RuntimeConfig, Destinations: input.Destinations,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBranchRequestInvalid, err)
		}
		if _, _, err := buildAskUserBranchResult(
			canonical.Response, target.Questions, target.ImplementationLabels,
		); err != nil {
			return fmt.Errorf("%w: %v", ErrBranchRequestInvalid, err)
		}
		result, err = r.forkFrameAskUserAnswerBranchConn(ctx, conn, canonical, requestDigest, branchID)
		return err
	})
	return result, schemaError(err)
}
