package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *Store) RetractQueuedMessage(frameID, messageUUID string) (FrameEvent, error) {
	if strings.TrimSpace(frameID) == "" || strings.TrimSpace(messageUUID) == "" {
		return FrameEvent{}, errors.New("frame id and message uuid are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("begin queued message retraction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var event FrameEvent
	var rawPayload string
	err = tx.QueryRowContext(ctx, `
		SELECT id, frame_id, sequence, event_type, payload, created_at
		FROM frame_events
		WHERE frame_id = ? AND (
			id = ? OR json_extract(payload, '$.messageUuid') = ?
			OR json_extract(payload, '$.message_uuid') = ?
		)
		ORDER BY sequence LIMIT 1`,
		frameID, messageUUID, messageUUID, messageUUID,
	).Scan(&event.ID, &event.FrameID, &event.Sequence, &event.Type, &rawPayload, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameEvent{}, fmt.Errorf("queued message %q does not exist", messageUUID)
	}
	if err != nil {
		return FrameEvent{}, fmt.Errorf("find queued message: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return FrameEvent{}, fmt.Errorf("decode queued message: %w", err)
	}
	if event.Type == "message_retracted" {
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, err
		}
		return event, nil
	}
	if event.Type != "queued_message" {
		return FrameEvent{}, fmt.Errorf("message %q is %q, not queued", messageUUID, event.Type)
	}
	retractedAt := s.now().UTC()
	payload := map[string]any{
		"messageUuid": messageUUID, "originalType": event.Type,
		"originalPayload": event.Payload, "retractedAt": retractedAt,
	}
	rawRetracted, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("encode message retraction: %w", err)
	}
	var nextSequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`,
		frameID,
	).Scan(&nextSequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate message retraction sequence: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM frame_events WHERE id = ? AND event_type = 'queued_message'`, event.ID)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("remove queued message: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return FrameEvent{}, errors.New("queued message changed concurrently")
	}
	event = FrameEvent{
		ID: uuid.NewString(), FrameID: frameID, Sequence: nextSequence,
		Type: "message_retracted", Payload: payload, CreatedAt: retractedAt,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawRetracted), event.CreatedAt,
	); err != nil {
		return FrameEvent{}, fmt.Errorf("insert message retraction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, fmt.Errorf("commit message retraction: %w", err)
	}
	return event, nil
}

func (s *Store) MoveFrameToProject(frameID, targetProjectID string) (Frame, *FrameEvent, error) {
	if strings.TrimSpace(frameID) == "" || strings.TrimSpace(targetProjectID) == "" {
		return Frame{}, nil, errors.New("frame id and target project id are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Frame{}, nil, fmt.Errorf("begin frame move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rootFrameID, sourceProjectID string
	if err := tx.QueryRowContext(ctx,
		`SELECT root_frame_id, project_id FROM frames WHERE id = ?`, frameID,
	).Scan(&rootFrameID, &sourceProjectID); err != nil {
		return Frame{}, nil, fmt.Errorf("look up frame for move: %w", err)
	}
	var targetExists string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE id = ?`, targetProjectID,
	).Scan(&targetExists); err != nil {
		return Frame{}, nil, fmt.Errorf("target project %q does not exist", targetProjectID)
	}
	if sourceProjectID == targetProjectID {
		if err := tx.Commit(); err != nil {
			return Frame{}, nil, err
		}
		frame, _, err := s.GetFrame(frameID)
		return frame, nil, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE frames SET project_id = ?, updated_at = ?
		WHERE root_frame_id = ?`, targetProjectID, now, rootFrameID); err != nil {
		return Frame{}, nil, fmt.Errorf("move frame tree: %w", err)
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, rootFrameID, "frame_moved", map[string]any{
		"sourceProjectId": sourceProjectID, "targetProjectId": targetProjectID,
	}, now)
	if err != nil {
		return Frame{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Frame{}, nil, fmt.Errorf("commit frame move: %w", err)
	}
	frame, found, err := s.GetFrame(frameID)
	if err != nil {
		return Frame{}, nil, err
	}
	if !found {
		return Frame{}, nil, errors.New("frame disappeared after move")
	}
	return frame, &event, nil
}

func (s *Store) ResumeFrame(frameID string) (Frame, *FrameEvent, error) {
	if strings.TrimSpace(frameID) == "" {
		return Frame{}, nil, errors.New("frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Frame{}, nil, fmt.Errorf("begin frame resume: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM frames WHERE id = ?`, frameID).Scan(&status); err != nil {
		return Frame{}, nil, fmt.Errorf("look up frame for resume: %w", err)
	}
	if status == FrameStatusProcessing {
		if err := tx.Commit(); err != nil {
			return Frame{}, nil, err
		}
		frame, _, err := s.GetFrame(frameID)
		return frame, nil, err
	}
	if status == "completed" {
		return Frame{}, nil, fmt.Errorf("completed frame %q cannot be resumed", frameID)
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE frames SET status = 'processing', updated_at = ? WHERE id = ?`,
		now, frameID,
	); err != nil {
		return Frame{}, nil, fmt.Errorf("resume frame: %w", err)
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, frameID, "frame_resumed", map[string]any{
		"previousStatus": status,
	}, now)
	if err != nil {
		return Frame{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Frame{}, nil, fmt.Errorf("commit frame resume: %w", err)
	}
	frame, found, err := s.GetFrame(frameID)
	if err != nil {
		return Frame{}, nil, fmt.Errorf("get resumed frame: %w", err)
	}
	if !found {
		return Frame{}, nil, errors.New("frame disappeared after resume")
	}
	return frame, &event, nil
}

func appendFrameLifecycleEvent(ctx context.Context, tx workspaceTransaction, frameID, eventType string, payload map[string]any, now time.Time) (FrameEvent, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`,
		frameID,
	).Scan(&sequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate %s event sequence: %w", eventType, err)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("encode %s event: %w", eventType, err)
	}
	event := FrameEvent{
		ID: uuid.NewString(), FrameID: frameID, Sequence: sequence,
		Type: eventType, Payload: payload, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
	); err != nil {
		return FrameEvent{}, fmt.Errorf("insert %s event: %w", eventType, err)
	}
	return event, nil
}
