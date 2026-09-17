package transcript

import (
	"context"
	"encoding/json"
)

// AppendNextToolOperationObservation derives the next sequence under the same
// write transaction as the durable owner's lease check or result settlement.
// An observer is not permission to execute or to bypass its owner's fencing.
func (tx *ImmediateTransaction) AppendNextToolOperationObservation(ctx context.Context, observer ToolOperationObserver, status string, details map[string]any) (Event, error) {
	var raw []byte
	err := tx.conn.QueryRowContext(ctx, `SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND event_type=? AND json_extract(payload_json,'$.operationId')=?
		ORDER BY event_id DESC LIMIT 1`, observer.StreamUID, ToolOperationObservationEventType, observer.OperationID).Scan(&raw)
	if err != nil {
		return Event{}, err
	}
	var previous struct {
		Ordinal int64 `json:"observationOrdinal"`
	}
	if err := json.Unmarshal(raw, &previous); err != nil || previous.Ordinal < 0 {
		return Event{}, ErrEventConflict
	}
	return tx.AppendToolOperationObservation(ctx, observer, status, previous.Ordinal+1, details)
}
