package transcript

import (
	"context"
	"encoding/json"
	"errors"
)

// RecoverInterruptedToolObservations settles only active-branch observations
// belonging to a previous service lifetime. It never retries a mutation whose
// outcome is unknown. The caller may inspect the environment before recovery.
func (r *Repository) RecoverInterruptedToolObservations(ctx context.Context, bootID string) ([]Event, error) {
	if bootID == "" {
		return nil, ErrEventConflict
	}
	rows, err := r.db.QueryContext(ctx, `SELECT event.stream_uid,stream.owner_id,event.runner_attempt,event.payload_json
 FROM transcript_events event JOIN transcript_streams stream ON stream.stream_uid=event.stream_uid
 JOIN transcript_branch_state branch ON branch.stream_uid=event.stream_uid
 JOIN transcript_branch_events membership ON membership.stream_uid=event.stream_uid AND membership.branch_id=branch.active_branch_id AND membership.event_id=event.event_id
 WHERE event.event_type=? AND json_extract(event.payload_json,'$.observerBootId')!=?
 AND json_extract(event.payload_json,'$.status')='running'
 AND NOT EXISTS(SELECT 1 FROM transcript_events later WHERE later.stream_uid=event.stream_uid AND later.event_type=event.event_type AND later.event_id>event.event_id AND json_extract(later.payload_json,'$.operationId')=json_extract(event.payload_json,'$.operationId'))
 ORDER BY event.event_id LIMIT 100`, ToolOperationObservationEventType, bootID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		observer ToolOperationObserver
		ordinal  int64
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		var raw []byte
		if err := rows.Scan(&item.observer.StreamUID, &item.observer.OwnerID, &item.observer.Attempt, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var payload struct {
			CallID        string `json:"toolCallId"`
			ToolName      string `json:"toolName"`
			OperationID   string `json:"operationId"`
			BootID        string `json:"observerBootId"`
			SourceEventID int64  `json:"sourceEventId"`
			Ordinal       int64  `json:"observationOrdinal"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		item.observer.CallID = payload.CallID
		item.observer.ToolName = payload.ToolName
		item.observer.OperationID = payload.OperationID
		item.observer.BootID = payload.BootID
		item.observer.SourceEventID = payload.SourceEventID
		item.ordinal = payload.Ordinal
		items = append(items, item)
	}
	scanErr := rows.Err()
	rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	events := []Event{}
	for _, item := range items {
		result := map[string]any{"ok": false, "status": "outcome_unknown", "operation_id": item.observer.OperationID, "error": "The background operation lost its service observer before a durable result was recorded. Inspect the environment before retrying."}
		event, err := r.AppendToolOperationObservation(ctx, item.observer, "failed", item.ordinal+1, map[string]any{"toolResult": result, "progress": map[string]any{"phase": "operation_interrupted", "indeterminate": true}})
		if errors.Is(err, ErrEventConflict) {
			continue
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}
