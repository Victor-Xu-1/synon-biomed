package transcript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ToolOperationAdmission is transient authority. Runner claims must never be
// serialized into an operation request, observation event or public receipt.
type ToolOperationAdmission struct {
	Claim                                        RunnerClaim
	CallID, ToolName, BootID, DurableOperationID string
}

func (r *Repository) BeginToolOperationObservation(ctx context.Context, claim RunnerClaim, callID, toolName, bootID string) (ToolOperationObserver, bool, error) {
	var observer ToolOperationObserver
	var created bool
	err := r.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		var err error
		observer, created, err = tx.BeginToolOperationObservation(ctx, ToolOperationAdmission{Claim: claim, CallID: callID, ToolName: toolName, BootID: bootID})
		return err
	})
	return observer, created, schemaError(err)
}

// BeginToolOperationObservation shares admission with the owning workspace
// operation transaction; rollback cannot leave an unowned spinning timeline row.
func (tx *ImmediateTransaction) BeginToolOperationObservation(ctx context.Context, admission ToolOperationAdmission) (ToolOperationObserver, bool, error) {
	r, conn := tx.repository, tx.conn
	claim, callID, toolName, bootID := admission.Claim, admission.CallID, admission.ToolName, admission.BootID
	var observer ToolOperationObserver
	created := false
	err := func() error {
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
		observer = ToolOperationObserver{StreamUID: stream.UID, OwnerID: stream.OwnerID, CallID: callID, ToolName: toolName, OperationID: "tool-operation-" + hex.EncodeToString(digest[:16]), BootID: bootID, Attempt: attempt, SourceEventID: sourceID, DurableOperationID: admission.DurableOperationID}
		clientID := observer.OperationID + ":0"
		if existing, found, err := findEventByClientID(ctx, conn, stream.UID, clientID); err != nil {
			return err
		} else if found {
			var payload map[string]any
			if json.Unmarshal(existing.PayloadJSON, &payload) != nil || payload["toolCallId"] != callID || payload["toolName"] != toolName {
				return ErrEventConflict
			}
			if bound, _ := payload["durableOperationId"].(string); bound != admission.DurableOperationID {
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
	}()
	return observer, created, err
}
