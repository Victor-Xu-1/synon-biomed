package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// HasFrameToolCallBatch identifies execution ownership, including historical
// branches. An approval adapter must never execute a runner-owned call itself.
// The live runner still validates the active branch and claim before resuming.
func (s *Store) HasFrameToolCallBatch(ctx context.Context, frameID, callID, toolName string) (bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return false, errors.New("workspace authority is unavailable")
	}
	if strings.TrimSpace(frameID) == "" || strings.TrimSpace(callID) == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_tool_call_items item
 JOIN transcript_streams stream ON stream.stream_uid=item.stream_uid
 WHERE stream.kind='frame_ref' AND stream.frame_id=? AND item.tool_call_id=? AND item.tool_name=?`, frameID, callID, toolName).Scan(&count)
	return count > 0, err
}

// ToolCallOriginAttempt keeps one public coordinate across runner handoffs.
func (s *Store) ToolCallOriginAttempt(ctx context.Context, batchID, callID string) (int64, bool, error) {
	if batchID == "" {
		return 0, false, nil
	}
	var attempt int64
	err := s.db.QueryRowContext(ctx, `SELECT event.runner_attempt FROM transcript_tool_call_items item
	 JOIN transcript_events event ON event.stream_uid=item.stream_uid AND event.event_id=item.started_event_id
	 WHERE item.batch_id=? AND item.tool_call_id=?`, batchID, callID).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return attempt, err == nil, err
}

// ValidateToolCallBatchResumeTx anchors the execution handoff to the live
// claim and immutable waiting call. Waiting remains the protocol state until
// its receipt settles; the approval record owns the one-shot execution claim.
func (s *Store) ValidateToolCallBatchResumeTx(ctx context.Context, tx *transcriptstore.ImmediateTransaction, input StartToolCallBatchItemInput) (ToolCallBatch, ToolCallBatchItem, error) {
	batch, item, err := loadToolCallBatchMutation(ctx, tx, input.Claim, input.BatchID, input.Ordinal, input.ExpectedBatchStateVersion, input.ExpectedItemStateVersion)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if batch.State != ToolCallBatchStateWaiting || item.State != ToolCallBatchItemStateWaiting {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	_, _, _, err = validateToolCallBatchCheckpointEvent(ctx, tx, batch, item, input.Claim, input.StartedEvent, "approval_resumed", false)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	return batch, item, nil
}
