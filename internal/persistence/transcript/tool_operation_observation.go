package transcript

import (
	"context"
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
	// DurableOperationID binds observation to its outbox owner. Service boot
	// changes do not terminate an operation owned by a renewable durable claim.
	DurableOperationID string
}

func (o ToolOperationObserver) payload(status string, ordinal int64, details map[string]any) map[string]any {
	payload := map[string]any{"version": 1, "toolCallId": o.CallID, "toolName": o.ToolName, "status": status, "operationId": o.OperationID, "observerBootId": o.BootID, "observerOwnerId": o.OwnerID, "observationOrdinal": ordinal, "sourceEventId": o.SourceEventID}
	if o.DurableOperationID != "" {
		payload["durableOperationId"] = o.DurableOperationID
	}
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
	if o.DurableOperationID != "" {
		return Event{}, ErrEventConflict // Durable progress requires its owner's transaction and lease.
	}
	var event Event
	err := r.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		var err error
		event, err = tx.AppendToolOperationObservation(ctx, o, status, ordinal, details)
		return err
	})
	return event, schemaError(err)
}

func (tx *ImmediateTransaction) AppendToolOperationObservation(ctx context.Context, o ToolOperationObserver, status string, ordinal int64, details map[string]any) (Event, error) {
	r, conn := tx.repository, tx.conn
	var event Event
	if status != "running" && status != "completed" && status != "failed" && status != "cancelled" {
		return event, errors.New("invalid operation observation status")
	}
	if ordinal <= 0 || strings.TrimSpace(o.OperationID) == "" {
		return event, ErrEventConflict
	}
	err := func() error {
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
		if bound, _ := initial["durableOperationId"].(string); bound != o.DurableOperationID {
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
	}()
	return event, err
}
