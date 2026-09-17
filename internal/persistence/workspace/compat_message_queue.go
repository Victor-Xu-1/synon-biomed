package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const CompatibilityMessageQueueLimit = 64

var (
	ErrCompatibilityIntentUsed            = errors.New("compatibility intent was already used")
	ErrCompatibilityIntentPayloadMismatch = errors.New("compatibility intent payload mismatch")
	ErrCompatibilityMessageQueueFull      = errors.New("compatibility message queue is full")
	ErrCompatibilityQueuedMessageTooLate  = errors.New("compatibility queued message was already delivered")
)

type CompatibilityMessageIntent struct {
	Sequence        int64
	IntentID        string
	FrameID         string
	DedupePayload   map[string]any
	DeliveryPayload map[string]any
	QueuedEventID   string
	State           string
	CreatedAt       time.Time
	ResolvedAt      *time.Time
}

type compatibilityQueueEnvelope struct {
	Version         int            `json:"_synonGoQueueVersion"`
	DedupePayload   map[string]any `json:"dedupe"`
	DeliveryPayload map[string]any `json:"delivery"`
	QueuedEventID   string         `json:"queuedEventId,omitempty"`
}

type compatibilityIntentScanner interface {
	Scan(...any) error
}

const compatibilityIntentSelect = `
	SELECT sequence, intent_id, frame_id, payload, state, created_at, resolved_at
	FROM queued_user_messages `

func (s *Store) GetCompatibilityMessageIntent(intentID string) (CompatibilityMessageIntent, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityMessageIntent{}, false, errors.New("workspace store is closed")
	}
	intentID = strings.TrimSpace(intentID)
	if intentID == "" {
		return CompatibilityMessageIntent{}, false, errors.New("intent id is required")
	}
	record, err := scanCompatibilityMessageIntent(s.db.QueryRowContext(
		context.Background(), compatibilityIntentSelect+` WHERE intent_id = ?`, intentID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityMessageIntent{}, false, nil
	}
	if err != nil {
		return CompatibilityMessageIntent{}, false, fmt.Errorf("get compatibility message intent: %w", err)
	}
	return record, true, nil
}

func (s *Store) ResolveCompatibilityMessageIntent(
	frameID string,
	intentID string,
	dedupePayload map[string]any,
) (CompatibilityMessageIntent, bool, error) {
	rawDedupe, _, err := marshalCompatibilityQueuePayloads(dedupePayload, map[string]any{})
	if err != nil {
		return CompatibilityMessageIntent{}, false, err
	}
	record, found, err := s.GetCompatibilityMessageIntent(intentID)
	if err != nil || !found {
		return record, found, err
	}
	if err := validateCompatibilityIntent(record, strings.TrimSpace(frameID), rawDedupe); err != nil {
		return CompatibilityMessageIntent{}, false, err
	}
	return record, true, nil
}

func (s *Store) QueueCompatibilityMessage(
	frameID string,
	intentID string,
	dedupePayload map[string]any,
	deliveryPayload map[string]any,
) (CompatibilityMessageIntent, FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, errors.New("workspace store is closed")
	}
	frameID, intentID = strings.TrimSpace(frameID), strings.TrimSpace(intentID)
	if frameID == "" || intentID == "" {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, errors.New("frame id and intent id are required")
	}
	rawDedupe, _, err := marshalCompatibilityQueuePayloads(dedupePayload, deliveryPayload)
	if err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("begin compatibility message queue: %w", err)
	}
	defer tx.Rollback()

	existing, err := scanCompatibilityMessageIntent(tx.QueryRowContext(ctx, compatibilityIntentSelect+` WHERE intent_id = ?`, intentID))
	if err == nil {
		if err := validateCompatibilityIntent(existing, frameID, rawDedupe); err != nil {
			return CompatibilityMessageIntent{}, FrameEvent{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("commit compatibility intent lookup: %w", err)
		}
		return existing, FrameEvent{}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("lookup compatibility queue intent: %w", err)
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM queued_user_messages WHERE frame_id = ? AND state = 'queued'`, frameID,
	).Scan(&pending); err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("count compatibility message queue: %w", err)
	}
	if pending >= CompatibilityMessageQueueLimit {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf(
			"%w: queue for frame %s has %d pending messages", ErrCompatibilityMessageQueueFull, frameID, pending,
		)
	}
	now := s.now().UTC()
	event, err := appendFrameLifecycleEvent(ctx, tx, frameID, "queued_message", deliveryPayload, now)
	if err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM queued_user_messages`).Scan(&sequence); err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("allocate compatibility message sequence: %w", err)
	}
	rawEnvelope, err := marshalCompatibilityQueueEnvelope(dedupePayload, deliveryPayload, event.ID)
	if err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO queued_user_messages (
			sequence, frame_id, payload, intent_id, state, resolved_at, created_at
		) VALUES (?, ?, ?, ?, 'queued', NULL, ?)`,
		sequence, frameID, rawEnvelope, intentID, now,
	)
	if err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("insert compatibility message queue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityMessageIntent{}, FrameEvent{}, false, fmt.Errorf("commit compatibility message queue: %w", err)
	}
	return CompatibilityMessageIntent{
		Sequence: sequence, IntentID: intentID, FrameID: frameID,
		DedupePayload: dedupePayload, DeliveryPayload: deliveryPayload,
		QueuedEventID: event.ID, State: "queued", CreatedAt: now,
	}, event, false, nil
}

func (s *Store) RecordCompatibilityDeliveredIntent(
	frameID string,
	intentID string,
	dedupePayload map[string]any,
	deliveryPayload map[string]any,
) (CompatibilityMessageIntent, error) {
	if s == nil || s.db == nil {
		return CompatibilityMessageIntent{}, errors.New("workspace store is closed")
	}
	frameID, intentID = strings.TrimSpace(frameID), strings.TrimSpace(intentID)
	if frameID == "" || intentID == "" {
		return CompatibilityMessageIntent{}, errors.New("frame id and intent id are required")
	}
	rawDedupe, _, err := marshalCompatibilityQueuePayloads(dedupePayload, deliveryPayload)
	if err != nil {
		return CompatibilityMessageIntent{}, err
	}
	now := s.now().UTC()
	rawEnvelope, err := marshalCompatibilityQueueEnvelope(dedupePayload, deliveryPayload, "")
	if err != nil {
		return CompatibilityMessageIntent{}, err
	}
	_, err = s.db.ExecContext(context.Background(), `
		INSERT OR IGNORE INTO queued_user_messages (
			sequence, frame_id, payload, intent_id, state, resolved_at, created_at
		) VALUES ((SELECT COALESCE(MAX(sequence), 0) + 1 FROM queued_user_messages), ?, ?, ?, 'drained', ?, ?)`,
		frameID, rawEnvelope, intentID, now, now)
	if err != nil {
		return CompatibilityMessageIntent{}, fmt.Errorf("record delivered compatibility intent: %w", err)
	}
	record, found, err := s.GetCompatibilityMessageIntent(intentID)
	if err != nil {
		return CompatibilityMessageIntent{}, err
	}
	if !found {
		return CompatibilityMessageIntent{}, errors.New("delivered compatibility intent was not persisted")
	}
	if err := validateCompatibilityIntent(record, frameID, rawDedupe); err != nil {
		return CompatibilityMessageIntent{}, err
	}
	return record, nil
}

func (s *Store) ClaimCompatibilityQueuedMessages(frameID string) ([]CompatibilityMessageIntent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return nil, errors.New("frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin compatibility queue claim: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, compatibilityIntentSelect+`
		WHERE frame_id = ? AND state = 'queued' ORDER BY sequence LIMIT 1`, frameID)
	if err != nil {
		return nil, fmt.Errorf("list compatibility queue claim: %w", err)
	}
	records := make([]CompatibilityMessageIntent, 0)
	for rows.Next() {
		record, err := scanCompatibilityMessageIntent(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan compatibility queue claim: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate compatibility queue claim: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close compatibility queue claim: %w", err)
	}
	for index := range records {
		record := records[index]
		result, err := tx.ExecContext(ctx, `
			UPDATE queued_user_messages SET state = 'delivering'
			WHERE sequence = ? AND state = 'queued'`, record.Sequence)
		if err != nil {
			return nil, fmt.Errorf("claim compatibility queued message: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return nil, fmt.Errorf("count compatibility queue claim: %w", err)
			}
			return nil, errors.New("compatibility queue changed concurrently")
		}
		records[index].State = "delivering"
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit compatibility queue claim: %w", err)
	}
	return records, nil
}

func (s *Store) CompatibilityQueuedFrameIDs() ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT frame_id FROM queued_user_messages
		WHERE state = 'queued' GROUP BY frame_id ORDER BY MIN(sequence)`)
	if err != nil {
		return nil, fmt.Errorf("list compatibility queued frames: %w", err)
	}
	defer rows.Close()
	frameIDs := make([]string, 0)
	for rows.Next() {
		var frameID string
		if err := rows.Scan(&frameID); err != nil {
			return nil, fmt.Errorf("scan compatibility queued frame: %w", err)
		}
		frameIDs = append(frameIDs, frameID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility queued frames: %w", err)
	}
	return frameIDs, nil
}

func (s *Store) CompleteCompatibilityQueuedMessageDelivery(intentID string) error {
	return s.resolveCompatibilityQueuedMessageDelivery(intentID, true)
}

func (s *Store) ReleaseCompatibilityQueuedMessageDelivery(intentID string) error {
	return s.resolveCompatibilityQueuedMessageDelivery(intentID, false)
}

func (s *Store) resolveCompatibilityQueuedMessageDelivery(intentID string, delivered bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	intentID = strings.TrimSpace(intentID)
	if intentID == "" {
		return errors.New("intent id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin compatibility queue resolution: %w", err)
	}
	defer tx.Rollback()
	record, err := scanCompatibilityMessageIntent(tx.QueryRowContext(ctx, compatibilityIntentSelect+` WHERE intent_id = ?`, intentID))
	if err != nil {
		return fmt.Errorf("load compatibility queue resolution: %w", err)
	}
	if delivered && record.State == "drained" || !delivered && record.State == "queued" {
		return tx.Commit()
	}
	if record.State != "delivering" {
		return fmt.Errorf("compatibility intent %s is %s, not delivering", intentID, record.State)
	}
	if delivered {
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE queued_user_messages
			SET state = 'drained', resolved_at = ? WHERE intent_id = ? AND state = 'delivering'`, now, intentID); err != nil {
			return fmt.Errorf("complete compatibility queue delivery: %w", err)
		}
		if record.QueuedEventID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM frame_events WHERE id = ? AND event_type = 'queued_message'`, record.QueuedEventID); err != nil {
				return fmt.Errorf("remove delivered compatibility queue event: %w", err)
			}
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE queued_user_messages
		SET state = 'queued', resolved_at = NULL WHERE intent_id = ? AND state = 'delivering'`, intentID); err != nil {
		return fmt.Errorf("release compatibility queue delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit compatibility queue resolution: %w", err)
	}
	return nil
}

func (s *Store) retractCompatibilityQueuedMessage(frameID, messageUUID string) (FrameEvent, error) {
	frameID, messageUUID = strings.TrimSpace(frameID), strings.TrimSpace(messageUUID)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("begin compatibility queued message retraction: %w", err)
	}
	defer tx.Rollback()
	record, err := scanCompatibilityMessageIntent(tx.QueryRowContext(ctx, compatibilityIntentSelect+`
		WHERE intent_id = ? AND frame_id = ?`, messageUUID, frameID))
	if errors.Is(err, sql.ErrNoRows) {
		record, err = importLegacyCompatibilityQueuedMessage(ctx, tx, frameID, messageUUID, s.now().UTC())
	}
	if errors.Is(err, sql.ErrNoRows) || err == nil && record.State == "retracted" {
		return FrameEvent{}, fmt.Errorf("queued message %q does not exist", messageUUID)
	}
	if err != nil {
		return FrameEvent{}, fmt.Errorf("find compatibility queued message: %w", err)
	}
	if record.State == "delivering" || record.State == "drained" {
		return FrameEvent{}, fmt.Errorf("%w: intent %s on frame %s", ErrCompatibilityQueuedMessageTooLate, messageUUID, frameID)
	}
	if record.State != "queued" {
		return FrameEvent{}, fmt.Errorf("queued message %q does not exist", messageUUID)
	}
	now := s.now().UTC()
	event, err := appendFrameLifecycleEvent(ctx, tx, frameID, "message_retracted", map[string]any{
		"messageUuid": messageUUID, "originalType": "queued_message",
		"originalPayload": record.DeliveryPayload, "retractedAt": now,
	}, now)
	if err != nil {
		return FrameEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE queued_user_messages SET state = 'retracted', resolved_at = ?
		WHERE intent_id = ? AND frame_id = ? AND state = 'queued'`, now, messageUUID, frameID); err != nil {
		return FrameEvent{}, fmt.Errorf("mark compatibility queued message retracted: %w", err)
	}
	if record.QueuedEventID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM frame_events WHERE id = ? AND event_type = 'queued_message'`, record.QueuedEventID); err != nil {
			return FrameEvent{}, fmt.Errorf("remove compatibility queued message: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, fmt.Errorf("commit compatibility queued message retraction: %w", err)
	}
	return event, nil
}

func importLegacyCompatibilityQueuedMessage(
	ctx context.Context,
	tx *sql.Tx,
	frameID string,
	intentID string,
	now time.Time,
) (CompatibilityMessageIntent, error) {
	var eventID, rawPayload string
	err := tx.QueryRowContext(ctx, `
		SELECT id, payload FROM frame_events
		WHERE frame_id = ? AND event_type = 'queued_message' AND (
			lower(id) = lower(?) OR lower(json_extract(payload, '$.messageUuid')) = lower(?)
			OR lower(json_extract(payload, '$.message_uuid')) = lower(?)
		) ORDER BY sequence LIMIT 1`, frameID, intentID, intentID, intentID,
	).Scan(&eventID, &rawPayload)
	if err != nil {
		return CompatibilityMessageIntent{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		return CompatibilityMessageIntent{}, fmt.Errorf("decode legacy compatibility queue payload: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM queued_user_messages`).Scan(&sequence); err != nil {
		return CompatibilityMessageIntent{}, fmt.Errorf("allocate legacy compatibility queue sequence: %w", err)
	}
	dedupePayload := legacyCompatibilityDedupePayload(payload)
	rawEnvelope, err := marshalCompatibilityQueueEnvelope(dedupePayload, payload, eventID)
	if err != nil {
		return CompatibilityMessageIntent{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO queued_user_messages (
			sequence, frame_id, payload, intent_id, state, resolved_at, created_at
		) VALUES (?, ?, ?, ?, 'queued', NULL, ?)`, sequence, frameID, rawEnvelope, intentID, now)
	if err != nil {
		return CompatibilityMessageIntent{}, fmt.Errorf("import legacy compatibility queued message: %w", err)
	}
	return CompatibilityMessageIntent{
		Sequence: sequence, IntentID: intentID, FrameID: frameID,
		DedupePayload: dedupePayload, DeliveryPayload: payload, QueuedEventID: eventID,
		State: "queued", CreatedAt: now,
	}, nil
}

func validateCompatibilityIntent(record CompatibilityMessageIntent, frameID, rawDedupe string) error {
	if record.FrameID != frameID {
		return fmt.Errorf("%w: intent %s belongs to frame %s", ErrCompatibilityIntentUsed, record.IntentID, record.FrameID)
	}
	stored, err := json.Marshal(record.DedupePayload)
	if err != nil {
		return fmt.Errorf("encode stored compatibility intent payload: %w", err)
	}
	if string(stored) != rawDedupe {
		return fmt.Errorf("%w: intent %s", ErrCompatibilityIntentPayloadMismatch, record.IntentID)
	}
	return nil
}

func marshalCompatibilityQueuePayloads(dedupePayload, deliveryPayload map[string]any) (string, string, error) {
	if dedupePayload == nil || deliveryPayload == nil {
		return "", "", errors.New("compatibility queue dedupe and delivery payloads are required")
	}
	rawDedupe, err := json.Marshal(dedupePayload)
	if err != nil {
		return "", "", fmt.Errorf("encode compatibility queue dedupe payload: %w", err)
	}
	rawDelivery, err := json.Marshal(deliveryPayload)
	if err != nil {
		return "", "", fmt.Errorf("encode compatibility queue delivery payload: %w", err)
	}
	return string(rawDedupe), string(rawDelivery), nil
}

func marshalCompatibilityQueueEnvelope(dedupePayload, deliveryPayload map[string]any, queuedEventID string) (string, error) {
	if dedupePayload == nil || deliveryPayload == nil {
		return "", errors.New("compatibility queue dedupe and delivery payloads are required")
	}
	raw, err := json.Marshal(compatibilityQueueEnvelope{
		Version: 1, DedupePayload: dedupePayload,
		DeliveryPayload: deliveryPayload, QueuedEventID: queuedEventID,
	})
	if err != nil {
		return "", fmt.Errorf("encode compatibility queue envelope: %w", err)
	}
	return string(raw), nil
}

func scanCompatibilityMessageIntent(scanner compatibilityIntentScanner) (CompatibilityMessageIntent, error) {
	var record CompatibilityMessageIntent
	var rawPayload string
	var resolved sql.NullTime
	if err := scanner.Scan(
		&record.Sequence, &record.IntentID, &record.FrameID, &rawPayload,
		&record.State, &record.CreatedAt, &resolved,
	); err != nil {
		return CompatibilityMessageIntent{}, err
	}
	var envelope compatibilityQueueEnvelope
	if err := json.Unmarshal([]byte(rawPayload), &envelope); err == nil && envelope.Version == 1 {
		if envelope.DedupePayload == nil || envelope.DeliveryPayload == nil {
			return CompatibilityMessageIntent{}, errors.New("compatibility queue envelope is incomplete")
		}
		record.DedupePayload = envelope.DedupePayload
		record.DeliveryPayload = envelope.DeliveryPayload
		record.QueuedEventID = envelope.QueuedEventID
	} else {
		var legacy any
		if err := json.Unmarshal([]byte(rawPayload), &legacy); err != nil {
			return CompatibilityMessageIntent{}, fmt.Errorf("decode legacy compatibility queue payload: %w", err)
		}
		switch value := legacy.(type) {
		case string:
			record.DeliveryPayload = map[string]any{"text": value, "content": value, "role": "user"}
		case map[string]any:
			record.DeliveryPayload = value
		default:
			return CompatibilityMessageIntent{}, errors.New("legacy compatibility queue payload has unsupported shape")
		}
		record.DedupePayload = legacyCompatibilityDedupePayload(record.DeliveryPayload)
	}
	if _, ok := record.DeliveryPayload["messageUuid"]; !ok {
		record.DeliveryPayload["messageUuid"] = record.IntentID
	}
	if _, ok := record.DeliveryPayload["clientMessageId"]; !ok {
		record.DeliveryPayload["clientMessageId"] = record.IntentID
	}
	if _, ok := record.DeliveryPayload["role"]; !ok {
		record.DeliveryPayload["role"] = "user"
	}
	if resolved.Valid {
		value := resolved.Time
		record.ResolvedAt = &value
	}
	return record, nil
}

func legacyCompatibilityDedupePayload(payload map[string]any) map[string]any {
	text, _ := payload["text"].(string)
	if text == "" {
		text, _ = payload["content"].(string)
	}
	dedupe := map[string]any{
		"text": text, "plan_mode": nil, "ultra_mode": nil, "control": nil,
	}
	for _, key := range []string{"plan_mode", "ultra_mode", "control", "goal_text"} {
		if value, ok := payload[key]; ok {
			dedupe[key] = value
		}
	}
	return dedupe
}
