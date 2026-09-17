package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ReadToolCallBatchWaitingResult reads the immutable checkpoint referenced by
// this exact waiting item, not a reconstructed invocation or a global search.
// Approval-specific parsing belongs to the caller; other waiting tools use
// the same batch protocol without carrying an approval reference.
func (s *Store) ReadToolCallBatchWaitingResult(ctx context.Context, item ToolCallBatchItem) (json.RawMessage, string, error) {
	if s == nil || s.db == nil || ctx == nil || item.BatchID == "" || item.StreamUID == "" || item.WaitingEventID <= 0 {
		return nil, "", ErrToolCallBatchConflict
	}
	var raw []byte
	var frameID string
	err := s.db.QueryRowContext(ctx, `SELECT event.payload_json,stream.frame_id
 FROM transcript_tool_call_items stored
 JOIN transcript_tool_call_batches batch ON batch.batch_id=stored.batch_id AND batch.stream_uid=stored.stream_uid
 JOIN transcript_streams stream ON stream.stream_uid=batch.stream_uid AND stream.owner_id=batch.owner_user_id
 JOIN transcript_events event ON event.stream_uid=stored.stream_uid AND event.event_id=stored.waiting_event_id
 JOIN transcript_branch_events membership ON membership.stream_uid=event.stream_uid AND membership.branch_id=batch.branch_id AND membership.event_id=event.event_id
 WHERE stored.batch_id=? AND stored.ordinal=? AND stored.stream_uid=? AND stored.tool_call_id=? AND stored.tool_name=?
 AND stored.waiting_event_id=? AND stored.state='waiting' AND event.event_type='runner_checkpoint' AND event.source='payload'`, item.BatchID, item.Ordinal, item.StreamUID, item.ToolCallID, item.ToolName, item.WaitingEventID).Scan(&raw, &frameID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrToolCallBatchConflict
	}
	if err != nil {
		return nil, "", err
	}
	var checkpoint struct {
		CallID string          `json:"toolCallId"`
		Name   string          `json:"toolName"`
		Phase  string          `json:"toolPhase"`
		Result json.RawMessage `json:"toolResult"`
	}
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.CallID != item.ToolCallID || checkpoint.Name != item.ToolName || checkpoint.Phase != "waiting" {
		return nil, "", ErrToolCallBatchConflict
	}
	return checkpoint.Result, frameID, nil
}
