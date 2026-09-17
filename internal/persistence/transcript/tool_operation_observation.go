package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const ToolOperationObservationEventType = "tool_operation_observation"

// ToolOperationObserver is a scoped observation authority, not an execution
// lease. Only a live runner can create it for an admitted background call.
// It cannot append model text, receipts, or change a runner/task lifecycle.
type ToolOperationObserver struct {
	StreamUID     string
	OwnerID       string
	CallID        string
	ToolName      string
	OperationID   string
	BootID        string
	Attempt       int64
	SourceEventID int64
}

func (r *Repository) BeginToolOperationObservation(ctx context.Context, claim RunnerClaim, callID, toolName, bootID string) (ToolOperationObserver, bool, error) {
	var observer ToolOperationObserver
	created := false
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := validateClaimConn(ctx, conn, claim, r.now().UTC(), true)
		if err != nil {
			return err
		}
		if callID == "" || toolName == "" || bootID == "" {
			return ErrEventConflict
		}
		var input []byte
		var attempt, sourceID int64
		err = conn.QueryRowContext(ctx, `SELECT item.arguments_json,event.runner_attempt,item.started_event_id
   FROM transcript_tool_call_items item JOIN transcript_tool_call_batches batch ON batch.batch_id=item.batch_id
   JOIN transcript_branch_state branch ON branch.stream_uid=batch.stream_uid AND branch.active_branch_id=batch.branch_id AND branch.generation=batch.branch_generation
   JOIN transcript_events event ON event.stream_uid=item.stream_uid AND event.event_id=item.started_event_id
   WHERE item.stream_uid=? AND batch.owner_user_id=? AND item.tool_call_id=? AND item.tool_name=?
    AND item.ordinal=batch.next_ordinal AND item.state IN ('running','waiting')`, claim.StreamUID, claim.OwnerID, callID, toolName).Scan(&input, &attempt, &sourceID)
		if err != nil {
			return err
		}
		var arguments map[string]any
		if json.Unmarshal(input, &arguments) != nil || arguments["background"] != true {
			return ErrEventConflict
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", stream.UID, sourceID, callID)))
		observer = ToolOperationObserver{StreamUID: stream.UID, OwnerID: stream.OwnerID, CallID: callID, ToolName: toolName, OperationID: "tool-operation-" + hex.EncodeToString(digest[:16]), BootID: bootID, Attempt: attempt, SourceEventID: sourceID}
		clientID := observer.OperationID + ":0"
		if existing, found, err := findEventByClientID(ctx, conn, stream.UID, clientID); err != nil {
			return err
		} else if found {
			var payload map[string]any
			if json.Unmarshal(existing.PayloadJSON, &payload) != nil || payload["toolCallId"] != callID || payload["toolName"] != toolName {
				return ErrEventConflict
			}
			observer.BootID, _ = payload["observerBootId"].(string)
			return nil
		}
		payload := observer.payload("running", 0, map[string]any{"toolInput": arguments, "progress": map[string]any{"phase": "queued", "indeterminate": true, "elapsedMs": 0}})
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, created, err = appendEventConn(ctx, conn, stream, eventRecord{clientMessageID: clientID, eventType: ToolOperationObservationEventType, source: EventSourcePayload, runnerAttempt: &attempt, payloadJSON: raw, destinations: []string{"ws"}, createdAt: r.now().UTC()})
		return err
	})
	return observer, created, schemaError(err)
}

func (o ToolOperationObserver) payload(status string, ordinal int64, details map[string]any) map[string]any {
	payload := map[string]any{"version": 1, "toolCallId": o.CallID, "toolName": o.ToolName, "status": status, "operationId": o.OperationID, "observerBootId": o.BootID, "observerOwnerId": o.OwnerID, "observationOrdinal": ordinal, "sourceEventId": o.SourceEventID}
	for key, value := range details {
		if key == "toolInput" || key == "toolResult" || key == "progress" {
			payload[key] = value
		}
	}
	if status == "running" {
		payload["toolPhase"] = fmt.Sprintf("progress-%06d", ordinal+1)
	} else {
		payload["toolPhase"] = status
	}
	return payload
}

func (r *Repository) AppendToolOperationObservation(ctx context.Context, o ToolOperationObserver, status string, ordinal int64, details map[string]any) (Event, error) {
	var event Event
	if status != "running" && status != "completed" && status != "failed" && status != "cancelled" {
		return event, errors.New("invalid operation observation status")
	}
	if ordinal <= 0 || strings.TrimSpace(o.OperationID) == "" {
		return event, ErrEventConflict
	}
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		stream, err := getStreamConn(ctx, conn, o.StreamUID, o.OwnerID)
		if err != nil {
			return err
		}
		seed, found, err := findEventByClientID(ctx, conn, o.StreamUID, o.OperationID+":0")
		if err != nil {
			return err
		}
		if !found {
			return ErrEventConflict
		}
		var initial map[string]any
		if json.Unmarshal(seed.PayloadJSON, &initial) != nil || seed.Type != ToolOperationObservationEventType || seed.RunnerAttempt == nil || *seed.RunnerAttempt != o.Attempt || initial["toolCallId"] != o.CallID || initial["toolName"] != o.ToolName || initial["observerBootId"] != o.BootID || initial["sourceEventId"] != float64(o.SourceEventID) {
			return ErrEventConflict
		}
		if err := validateExistingEventBranchMembership(ctx, conn, o.StreamUID, seed.EventID); err != nil {
			return err
		}
		previous, found, err := findEventByClientID(ctx, conn, o.StreamUID, fmt.Sprintf("%s:%d", o.OperationID, ordinal-1))
		if err != nil {
			return err
		}
		if !found {
			return ErrEventConflict
		}
		var previousPayload map[string]any
		if json.Unmarshal(previous.PayloadJSON, &previousPayload) != nil || previousPayload["status"] != "running" {
			return ErrEventConflict
		}
		raw, err := json.Marshal(o.payload(status, ordinal, details))
		if err != nil {
			return err
		}
		event, _, err = appendEventConn(ctx, conn, stream, eventRecord{clientMessageID: fmt.Sprintf("%s:%d", o.OperationID, ordinal), eventType: ToolOperationObservationEventType, source: EventSourcePayload, runnerAttempt: &o.Attempt, payloadJSON: raw, destinations: []string{"ws"}, createdAt: r.now().UTC()})
		return err
	})
	return event, schemaError(err)
}
