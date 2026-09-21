package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/realtime"
)

const (
	RealtimeOutboxTopic   = "workspace.realtime"
	realtimeEnvelopeV1    = 1
	realtimeOutboxMaxSend = 16
)

// RealtimeOutboxEnvelope is the typed durable contract between a workspace
// mutation and the realtime materializer. Event.ID is stable across retries.
type RealtimeOutboxEnvelope struct {
	Version                   int                `json:"version"`
	Event                     RealtimeEventInput `json:"event"`
	FrameEventID              string             `json:"frameEventId,omitempty"`
	FrameIncarnationID        string             `json:"frameIncarnationId,omitempty"`
	RetiredFrameIncarnationID string             `json:"retiredFrameIncarnationId,omitempty"`
}

// EnqueueRealtimeOutboxTx validates the 47-event catalog before commit. This
// prevents a business mutation from committing an envelope that can never be
// delivered by a later process.
func (s *Store) EnqueueRealtimeOutboxTx(ctx context.Context, tx *sql.Tx, input RealtimeEventInput, frameEventID string) (OutboxEvent, error) {
	if tx == nil {
		return OutboxEvent{}, errors.New("realtime outbox transaction is required")
	}
	return s.enqueueRealtimeOutboxTransaction(ctx, tx, input, frameEventID, "")
}

func (s *Store) enqueueRealtimeFrameRetirementTx(
	ctx context.Context,
	tx *sql.Tx,
	input RealtimeEventInput,
	incarnationID string,
) (OutboxEvent, error) {
	incarnationID = strings.TrimSpace(incarnationID)
	if incarnationID == "" {
		return OutboxEvent{}, errors.New("retired frame incarnation id is required")
	}
	return s.enqueueRealtimeOutboxTransaction(ctx, tx, input, "", incarnationID)
}

func (s *Store) enqueueRealtimeOutboxTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	input RealtimeEventInput,
	frameEventID, retiredFrameIncarnationID string,
) (OutboxEvent, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.Type = strings.TrimSpace(input.Type)
	frameEventID = strings.TrimSpace(frameEventID)
	retiredFrameIncarnationID = strings.TrimSpace(retiredFrameIncarnationID)
	if input.ID == "" {
		return OutboxEvent{}, errors.New("realtime outbox event id is required")
	}
	if _, found := realtime.LookupEvent(input.Type); !found {
		return OutboxEvent{}, fmt.Errorf("%w: %s", realtime.ErrUnknownEvent, input.Type)
	}
	if input.Payload == nil {
		input.Payload = map[string]any{}
	}
	rawPayload, err := json.Marshal(input.Payload)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("encode realtime outbox payload: %w", err)
	}
	if len(rawPayload) > maxRealtimeEventPayloadBytes {
		return OutboxEvent{}, fmt.Errorf("realtime event payload exceeds %d bytes", maxRealtimeEventPayloadBytes)
	}
	frameIncarnationID := ""
	if frameEventID != "" {
		frameEvent, _, found, err := frameEventByID(ctx, tx, frameEventID)
		if err != nil {
			return OutboxEvent{}, err
		}
		if !found || frameEvent.FrameID != input.FrameID {
			return OutboxEvent{}, fmt.Errorf("frame event %q does not belong to realtime frame %q", frameEventID, input.FrameID)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT incarnation_id FROM frames WHERE id = ?`, input.FrameID,
		).Scan(&frameIncarnationID); err != nil {
			return OutboxEvent{}, fmt.Errorf("resolve realtime frame incarnation: %w", err)
		}
	}
	if retiredFrameIncarnationID != "" && frameEventID != "" {
		return OutboxEvent{}, errors.New("realtime outbox event cannot publish and retire a frame incarnation")
	}
	envelope, err := json.Marshal(RealtimeOutboxEnvelope{
		Version: realtimeEnvelopeV1, Event: input, FrameEventID: frameEventID,
		FrameIncarnationID: frameIncarnationID, RetiredFrameIncarnationID: retiredFrameIncarnationID,
	})
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("encode realtime outbox envelope: %w", err)
	}
	partition := "global"
	if input.UserID != "" {
		partition = "user:" + input.UserID
	}
	aggregateType, aggregateID := "realtime", input.ID
	if input.FrameID != "" {
		aggregateType, aggregateID = "frame", input.FrameID
	} else if input.ProjectID != "" {
		aggregateType, aggregateID = "project", input.ProjectID
	}
	return s.enqueueOutboxTransaction(ctx, tx, EnqueueOutboxInput{
		IdempotencyKey: input.ID, Topic: RealtimeOutboxTopic, PartitionKey: partition,
		Type: input.Type, AggregateType: aggregateType, AggregateID: aggregateID,
		Payload: envelope, MaxAttempts: realtimeOutboxMaxSend,
	})
}

var realtimeMessageFrameEventTypes = map[string]struct{}{
	"assistant_message":       {},
	"ask_user_answer":         {},
	"goal_driver":             {},
	"internal_task":           {},
	"input_requests_resolved": {},
	"message_retracted":       {},
	"permission_response":     {},
	"plan_approved":           {},
	"plan_discarded":          {},
	"queued_message":          {},
	"send_message":            {},
	"stop_generation":         {},
	"user_message":            {},
}

var realtimeFrameEventOverrides = map[string]string{
	"content_delta":     "text_chunk",
	"content_reset":     "text_reset",
	"rate_limit":        "rate_limit_notice",
	"runner_checkpoint": "frame_activity",
	"runner_finished":   "frame_activity",
	"session_compact":   "compaction_status",
	"tool_stdout":       "tool_stdout_chunk",
}

func RealtimeTypeForFrameEvent(sourceType string) string {
	sourceType = strings.TrimSpace(sourceType)
	if _, isMessage := realtimeMessageFrameEventTypes[sourceType]; isMessage {
		return "frame_messages_delta"
	}
	if eventType := realtimeFrameEventOverrides[sourceType]; eventType != "" {
		return eventType
	}
	if _, found := realtime.LookupEvent(sourceType); found {
		return sourceType
	}
	return "frame_update"
}

func FrameRealtimeEventInput(eventID, userID string, frame Frame, source FrameEvent) RealtimeEventInput {
	payload := make(map[string]any, len(source.Payload))
	for key, value := range source.Payload {
		payload[key] = value
	}
	payload["project_id"] = frame.ProjectID
	payload["root_frame_id"] = frame.RootFrameID
	payload["frame_id"] = frame.ID
	payload["source_event_id"] = source.ID
	payload["source_event_sequence"] = source.Sequence
	payload["source_event_type"] = source.Type
	payload["frame_status"] = frame.Status
	payload["agent_name"] = frame.AgentName
	return RealtimeEventInput{
		ID: eventID, UserID: userID, ProjectID: frame.ProjectID, RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Type: RealtimeTypeForFrameEvent(source.Type), Payload: payload,
	}
}

func DecodeRealtimeOutboxEnvelope(event OutboxEvent) (RealtimeOutboxEnvelope, error) {
	if event.Topic != RealtimeOutboxTopic {
		return RealtimeOutboxEnvelope{}, fmt.Errorf("unsupported realtime outbox topic %q", event.Topic)
	}
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	var envelope RealtimeOutboxEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return RealtimeOutboxEnvelope{}, fmt.Errorf("decode realtime outbox envelope: %w", err)
	}
	if envelope.Version != realtimeEnvelopeV1 {
		return RealtimeOutboxEnvelope{}, fmt.Errorf("unsupported realtime outbox envelope version %d", envelope.Version)
	}
	if strings.TrimSpace(envelope.Event.ID) == "" || strings.TrimSpace(envelope.Event.Type) == "" {
		return RealtimeOutboxEnvelope{}, errors.New("realtime outbox envelope event id and type are required")
	}
	if envelope.Event.Type != event.Type {
		return RealtimeOutboxEnvelope{}, fmt.Errorf("realtime outbox type %q does not match envelope %q", event.Type, envelope.Event.Type)
	}
	return envelope, nil
}

// RealtimeOutboxSourceRetired reports whether a later, already committed
// delete mutation makes a missing frame-event source obsolete. The delete
// outbox row is written in the same transaction as the project/frame delete,
// so it is durable evidence rather than an absence-based guess. Sequence and
// partition fencing prevent an unrelated owner or an older delete from
// suppressing delivery.
func (s *Store) RealtimeOutboxSourceRetired(
	ctx context.Context,
	event OutboxEvent,
	envelope RealtimeOutboxEnvelope,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if event.Topic != RealtimeOutboxTopic || event.Sequence <= 0 || strings.TrimSpace(event.PartitionKey) == "" {
		return false, errors.New("valid realtime outbox event is required")
	}
	frameID := strings.TrimSpace(envelope.Event.FrameID)
	projectID := strings.TrimSpace(envelope.Event.ProjectID)
	rootFrameID := strings.TrimSpace(envelope.Event.RootFrameID)
	incarnationID := strings.TrimSpace(envelope.FrameIncarnationID)
	if frameID == "" || projectID == "" || rootFrameID == "" || incarnationID == "" {
		return false, nil
	}
	var retired bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM workspace_outbox AS successor
			WHERE successor.sequence > ? AND successor.partition_key = ? AND successor.topic = ?
				AND successor.event_type = 'frame_update'
				AND successor.aggregate_type = 'frame' AND successor.aggregate_id = ?
				AND json_extract(successor.payload_json, '$.event.projectId') = ?
				AND json_extract(successor.payload_json, '$.event.rootFrameId') = ?
				AND json_extract(successor.payload_json, '$.event.payload.action') = 'deleted'
				AND json_extract(successor.payload_json, '$.retiredFrameIncarnationId') = ?
		)`, event.Sequence, event.PartitionKey, RealtimeOutboxTopic,
		frameID, projectID, rootFrameID, incarnationID,
	).Scan(&retired)
	if err != nil {
		return false, fmt.Errorf("check realtime outbox source retirement: %w", err)
	}
	return retired, nil
}

func (s *Store) CountUndeliveredRealtimeOutbox(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workspace_outbox
		WHERE topic = ? AND status NOT IN (?, ?)`, RealtimeOutboxTopic,
		OutboxStatusDelivered, OutboxStatusDeadLetter).Scan(&count); err != nil {
		return 0, fmt.Errorf("count undelivered realtime outbox events: %w", err)
	}
	return count, nil
}
