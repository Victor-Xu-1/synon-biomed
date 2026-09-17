package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) ApplyFrameControl(frameID, eventType, clientMessageID string, payload map[string]any) (Frame, FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return Frame{}, FrameEvent{}, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	eventType = strings.TrimSpace(eventType)
	clientMessageID = strings.TrimSpace(clientMessageID)
	if frameID == "" || eventType == "" {
		return Frame{}, FrameEvent{}, false, errors.New("frame id and control event type are required")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload = cloneBranchPayload(payload)
	if clientMessageID != "" {
		payload["clientMessageId"] = clientMessageID
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("begin frame control transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	frame, err := branchFrameByID(ctx, tx, frameID)
	if err != nil {
		return Frame{}, FrameEvent{}, false, err
	}
	if clientMessageID != "" {
		existing, found, err := existingFrameControlEvent(ctx, tx, frameID, eventType, clientMessageID)
		if err != nil {
			return Frame{}, FrameEvent{}, false, err
		}
		if found {
			if err := tx.Commit(); err != nil {
				return Frame{}, FrameEvent{}, false, err
			}
			return frame, existing, true, nil
		}
	}
	now := s.now().UTC()
	var sequence int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?", frameID).Scan(&sequence); err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("allocate frame control sequence: %w", err)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("marshal frame control payload: %w", err)
	}
	event := FrameEvent{
		ID: uuid.NewString(), FrameID: frameID, Sequence: sequence,
		Type: eventType, Payload: payload, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
	); err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("insert frame control event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE frames SET status = 'processing', updated_at = ? WHERE id = ?", now, frameID); err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("resume controlled frame: %w", err)
	}
	frame.Status = FrameStatusProcessing
	frame.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return Frame{}, FrameEvent{}, false, fmt.Errorf("commit frame control: %w", err)
	}
	return frame, event, false, nil
}

func existingFrameControlEvent(ctx context.Context, tx workspaceTransaction, frameID, eventType, clientMessageID string) (FrameEvent, bool, error) {
	var event FrameEvent
	var rawPayload string
	err := tx.QueryRowContext(ctx,
		"SELECT id, frame_id, sequence, event_type, payload, created_at FROM frame_events WHERE frame_id = ? AND event_type = ? AND json_extract(payload, '$.clientMessageId') = ? ORDER BY sequence LIMIT 1",
		frameID, eventType, clientMessageID,
	).Scan(&event.ID, &event.FrameID, &event.Sequence, &event.Type, &rawPayload, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameEvent{}, false, nil
	}
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("find existing frame control event: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return FrameEvent{}, false, fmt.Errorf("decode existing frame control event: %w", err)
	}
	return event, true, nil
}
