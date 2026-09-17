package transcript

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AppendFrameUserEventToBranch atomically makes an existing canonical branch
// active and appends the next user input to that branch. A retry carrying the
// same client message id converges on the original event even if another
// branch has since become active.
type AppendFrameUserEventToBranchInput struct {
	AppendFrameUserEventInput
	TargetBranchID         string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
	ClientMutationID       string
}

type AppendFrameUserEventToBranchResult struct {
	Event              Event
	FrameEvent         FrameReferenceEvent
	Created            bool
	BranchGeneration   int64
	Switched           bool
	RunnerCancellation CancelRunnerResult
}

func (r *Repository) AppendFrameUserEventToBranch(
	ctx context.Context,
	input AppendFrameUserEventToBranchInput,
) (AppendFrameUserEventToBranchResult, error) {
	if r == nil || r.db == nil {
		return AppendFrameUserEventToBranchResult{}, ErrSchemaUnavailable
	}
	normalized, err := normalizeAppendFrameUserEventInput(input.AppendFrameUserEventInput)
	if err != nil {
		return AppendFrameUserEventToBranchResult{}, err
	}
	input.TargetBranchID = strings.TrimSpace(input.TargetBranchID)
	input.ExpectedActiveBranchID = strings.TrimSpace(input.ExpectedActiveBranchID)
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	if !validTranscriptBranchID(input.TargetBranchID) || !validTranscriptBranchID(input.ExpectedActiveBranchID) ||
		input.ExpectedGeneration <= 0 || !validBranchMutationID(input.ClientMutationID) {
		return AppendFrameUserEventToBranchResult{}, fmt.Errorf("%w: target branch is invalid", ErrBranchRequestInvalid)
	}
	expectedMessageID := "branch-continue:" + input.ClientMutationID
	if normalized.ClientMessageID != expectedMessageID || normalized.MessageUUID != expectedMessageID ||
		(normalized.MessageOrigin != "" && normalized.MessageOrigin != "task_intent") {
		return AppendFrameUserEventToBranchResult{}, fmt.Errorf("%w: continuation identity is invalid", ErrBranchRequestInvalid)
	}
	normalized.MessageOrigin = "task_intent"

	var result AppendFrameUserEventToBranchResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		stream, err := getStreamConn(ctx, conn, normalized.StreamUID, normalized.OwnerID)
		if err != nil {
			return err
		}
		if stream.Kind != StreamKindFrameRef || stream.FrameID == "" || stream.FrameID != stream.RootFrameID {
			return ErrEventConflict
		}
		expectedFrameEventID := "frame-branch-continue:" + stream.FrameID + ":" + input.TargetBranchID + ":" + input.ClientMutationID
		if normalized.FrameEventID != expectedFrameEventID {
			return ErrEventConflict
		}
		var targetCount int
		if err := conn.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=? AND branch_id=?`,
			stream.UID, input.TargetBranchID,
		).Scan(&targetCount); err != nil {
			return err
		}
		if targetCount != 1 {
			return ErrBranchTargetNotFound
		}

		existing, found, err := findEventByClientID(ctx, conn, stream.UID, normalized.ClientMessageID)
		if err != nil {
			return err
		}
		if found {
			var membership int
			if err := conn.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM transcript_branch_events
				WHERE stream_uid=? AND branch_id=? AND event_id=?`,
				stream.UID, input.TargetBranchID, existing.EventID,
			).Scan(&membership); err != nil {
				return err
			}
			if membership != 1 {
				return ErrEventConflict
			}
			validated, frameEvent, created, err := appendFrameUserEventConn(ctx, conn, normalized, existing.CreatedAt)
			if err != nil || created || validated.EventID != existing.EventID {
				if err != nil {
					return err
				}
				return ErrEventConflict
			}
			var generation int64
			if err := conn.QueryRowContext(ctx, `
				SELECT generation FROM transcript_branch_state WHERE stream_uid=?`, stream.UID,
			).Scan(&generation); err != nil {
				return err
			}
			result = AppendFrameUserEventToBranchResult{
				Event: validated, FrameEvent: frameEvent, BranchGeneration: generation,
			}
			return nil
		}
		var activeBranchID string
		var generation int64
		if err := conn.QueryRowContext(ctx, `
			SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, stream.UID,
		).Scan(&activeBranchID, &generation); err != nil {
			return err
		}
		if activeBranchID != input.ExpectedActiveBranchID || generation != input.ExpectedGeneration {
			return ErrBranchStateStale
		}
		var frameStatus string
		if err := conn.QueryRowContext(ctx, `SELECT status FROM frames WHERE id=?`, stream.FrameID).Scan(&frameStatus); err != nil {
			return err
		}
		if activeBranchID == input.TargetBranchID &&
			(frameStatus == "awaiting_user_response" || frameStatus == "awaiting_plan_approval") {
			return ErrBranchStateStale
		}
		cancellation, err := cancelRunningRunnerForBranchConn(ctx, conn, stream, input.ClientMutationID, now)
		if err != nil {
			return fmt.Errorf("cancel active branch runner: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE transcript_streams
			SET consumed_input_revision=MAX(consumed_input_revision,input_revision),updated_at=?
			WHERE stream_uid=?`, now, stream.UID); err != nil {
			return err
		}
		if err := clearSupersededFrameInputStateConn(ctx, conn, stream); err != nil {
			return fmt.Errorf("clear superseded frame input: %w", err)
		}
		result.RunnerCancellation = cancellation
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_branch_state SET active_branch_id=?,generation=generation+1,updated_at=?
			WHERE stream_uid=? AND active_branch_id=? AND generation=?`,
			input.TargetBranchID, now, stream.UID, activeBranchID, generation,
		)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrBranchStateStale
		}
		generation++
		result.Switched = activeBranchID != input.TargetBranchID

		event, frameEvent, created, err := appendFrameUserEventConn(ctx, conn, normalized, now)
		if err != nil {
			return fmt.Errorf("append branch continuation: %w", err)
		}
		if !created {
			return ErrEventConflict
		}
		result.Event = event
		result.FrameEvent = frameEvent
		result.Created = true
		result.BranchGeneration = generation
		return nil
	})
	return result, schemaError(err)
}
