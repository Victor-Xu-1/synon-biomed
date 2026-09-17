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

	transcriptstore "synon-go/internal/persistence/transcript"
)

var ErrReadCursorConflict = errors.New("read cursor authority conflict")

type FrameMessagePage struct {
	Messages     []FrameEvent `json:"messages"`
	NextSequence int64        `json:"nextSequence"`
	HasMore      bool         `json:"hasMore"`
}

type FrameReadCursor struct {
	RootFrameID  string    `json:"rootFrameId"`
	MessageUUID  string    `json:"messageUuid,omitempty"`
	MessageIndex int       `json:"messageIndex"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (s *Store) GetFrameMessagesPage(frameID string, afterSequence int64, limit int) (FrameMessagePage, error) {
	if strings.TrimSpace(frameID) == "" {
		return FrameMessagePage{}, errors.New("frame id is required")
	}
	if afterSequence < 0 {
		return FrameMessagePage{}, errors.New("message cursor must be non-negative")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	events, err := s.ListFrameEvents(frameID, afterSequence, limit+1)
	if err != nil {
		return FrameMessagePage{}, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	next := afterSequence
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	return FrameMessagePage{Messages: events, NextSequence: next, HasMore: hasMore}, nil
}

func (s *Store) LocateFrameMessage(frameID, messageUUID string) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(frameID) == "" || strings.TrimSpace(messageUUID) == "" {
		return FrameEvent{}, false, errors.New("frame id and message uuid are required")
	}
	var event FrameEvent
	var rawPayload string
	err := s.db.QueryRowContext(context.Background(), `
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
		return FrameEvent{}, false, nil
	}
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("locate frame message: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return FrameEvent{}, false, fmt.Errorf("decode located frame message: %w", err)
	}
	return event, true, nil
}

func (s *Store) GetReadCursor(frameID string) (FrameReadCursor, bool, error) {
	if s == nil || s.db == nil {
		return FrameReadCursor{}, false, errors.New("workspace store is closed")
	}
	rootFrameID, err := s.resolveRootFrameID(frameID)
	if err != nil {
		return FrameReadCursor{}, false, err
	}
	var cursor FrameReadCursor
	var messageUUID sql.NullString
	err = s.db.QueryRowContext(context.Background(), `
		SELECT root_frame_id, message_uuid, message_index, updated_at
		FROM frame_read_cursors WHERE root_frame_id = ?`, rootFrameID,
	).Scan(&cursor.RootFrameID, &messageUUID, &cursor.MessageIndex, &cursor.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameReadCursor{}, false, nil
	}
	if err != nil {
		return FrameReadCursor{}, false, fmt.Errorf("get frame read cursor: %w", err)
	}
	cursor.MessageUUID = messageUUID.String
	return cursor, true, nil
}

func (s *Store) PutReadCursor(frameID, messageUUID string, messageIndex int) (FrameReadCursor, error) {
	if s == nil || s.db == nil {
		return FrameReadCursor{}, errors.New("workspace store is closed")
	}
	if messageIndex < 0 {
		return FrameReadCursor{}, errors.New("message index must be non-negative")
	}
	rootFrameID, err := s.resolveRootFrameID(frameID)
	if err != nil {
		return FrameReadCursor{}, err
	}
	if strings.TrimSpace(messageUUID) != "" {
		if _, found, err := s.LocateFrameMessage(rootFrameID, messageUUID); err != nil {
			return FrameReadCursor{}, err
		} else if !found {
			return FrameReadCursor{}, fmt.Errorf("message %q does not exist in root frame %q", messageUUID, rootFrameID)
		}
	}
	return s.putReadCursor(frameID, messageUUID, messageIndex)
}

// PutStableReadCursor persists a cursor after the caller has validated the
// stable message ID against the active canonical history authority. It avoids
// re-validating that ID against legacy frame_events after transcript cutover.
func (s *Store) PutStableReadCursor(
	frameID, messageUUID string, messageIndex int, observedMessageUUID string, repair bool,
) (FrameReadCursor, error) {
	return s.putStableReadCursor(frameID, messageUUID, messageIndex, observedMessageUUID, nil, repair)
}

func (s *Store) PutStableReadCursorObserved(
	frameID, messageUUID string, messageIndex int,
	observedMessageUUID string, observedMessageIndex int, repair bool,
) (FrameReadCursor, error) {
	return s.putStableReadCursor(
		frameID, messageUUID, messageIndex, observedMessageUUID, &observedMessageIndex, repair,
	)
}

func (s *Store) putStableReadCursor(
	frameID, messageUUID string, messageIndex int,
	observedMessageUUID string, observedMessageIndex *int, repair bool,
) (FrameReadCursor, error) {
	if s == nil || s.db == nil {
		return FrameReadCursor{}, errors.New("workspace store is closed")
	}
	if messageIndex < 0 {
		return FrameReadCursor{}, errors.New("message index must be non-negative")
	}
	rootFrameID, err := s.resolveRootFrameID(frameID)
	if err != nil {
		return FrameReadCursor{}, err
	}
	messageUUID = strings.TrimSpace(messageUUID)
	observedMessageUUID = strings.TrimSpace(observedMessageUUID)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return FrameReadCursor{}, fmt.Errorf("begin stable read cursor update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existing FrameReadCursor
	var existingUUID sql.NullString
	loadErr := tx.QueryRowContext(context.Background(), `SELECT root_frame_id,message_uuid,message_index,updated_at
		FROM frame_read_cursors WHERE root_frame_id=?`, rootFrameID).Scan(
		&existing.RootFrameID, &existingUUID, &existing.MessageIndex, &existing.UpdatedAt,
	)
	if loadErr == nil {
		existing.MessageUUID = existingUUID.String
		if repair {
			if observedMessageUUID == "" || existing.MessageUUID != observedMessageUUID ||
				(observedMessageIndex != nil && existing.MessageIndex != *observedMessageIndex) {
				return FrameReadCursor{}, fmt.Errorf("%w: repair authority changed", ErrReadCursorConflict)
			}
		} else if messageIndex < existing.MessageIndex {
			return existing, tx.Commit()
		} else if messageIndex == existing.MessageIndex {
			if messageUUID != existing.MessageUUID {
				return FrameReadCursor{}, fmt.Errorf("%w: identity differs at stored index", ErrReadCursorConflict)
			}
			return existing, tx.Commit()
		}
	} else if !errors.Is(loadErr, sql.ErrNoRows) {
		return FrameReadCursor{}, fmt.Errorf("load stable read cursor: %w", loadErr)
	} else if repair {
		return FrameReadCursor{}, fmt.Errorf("%w: repair target does not exist", ErrReadCursorConflict)
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO frame_read_cursors (root_frame_id, message_uuid, message_index, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(root_frame_id) DO UPDATE SET
			message_uuid = excluded.message_uuid,
			message_index = excluded.message_index,
			updated_at = excluded.updated_at`,
		rootFrameID, nullableString(messageUUID), messageIndex, now,
	); err != nil {
		return FrameReadCursor{}, fmt.Errorf("put frame read cursor: %w", err)
	}
	winner := FrameReadCursor{
		RootFrameID: rootFrameID, MessageUUID: messageUUID,
		MessageIndex: messageIndex, UpdatedAt: now,
	}
	if err := tx.Commit(); err != nil {
		return FrameReadCursor{}, fmt.Errorf("commit stable read cursor: %w", err)
	}
	return winner, nil
}

// PutStableReadCursorImmediate writes through the same BEGIN IMMEDIATE used to
// validate Transcript activation and branch authority. Repair compares both
// the observed identity and index, preventing same-ID ABA updates.
func (s *Store) PutStableReadCursorImmediate(
	ctx context.Context, tx *transcriptstore.ImmediateTransaction,
	frameID, messageUUID string, messageIndex int,
	observedMessageUUID string, observedMessageIndex int, repair bool,
) (FrameReadCursor, error) {
	if s == nil || s.db == nil || tx == nil {
		return FrameReadCursor{}, errors.New("workspace store or immediate transaction is unavailable")
	}
	if messageIndex < 0 {
		return FrameReadCursor{}, errors.New("message index must be non-negative")
	}
	var rootFrameID string
	if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id=?`, frameID).Scan(&rootFrameID); err != nil {
		return FrameReadCursor{}, fmt.Errorf("resolve root frame: %w", err)
	}
	messageUUID = strings.TrimSpace(messageUUID)
	observedMessageUUID = strings.TrimSpace(observedMessageUUID)
	var existing FrameReadCursor
	var existingUUID sql.NullString
	loadErr := tx.QueryRowContext(ctx, `SELECT root_frame_id,message_uuid,message_index,updated_at
		FROM frame_read_cursors WHERE root_frame_id=?`, rootFrameID).Scan(
		&existing.RootFrameID, &existingUUID, &existing.MessageIndex, &existing.UpdatedAt,
	)
	if loadErr == nil {
		existing.MessageUUID = existingUUID.String
		if repair {
			if observedMessageUUID == "" || existing.MessageUUID != observedMessageUUID ||
				existing.MessageIndex != observedMessageIndex {
				return FrameReadCursor{}, fmt.Errorf("%w: repair authority changed", ErrReadCursorConflict)
			}
		} else if messageIndex < existing.MessageIndex {
			return existing, nil
		} else if messageIndex == existing.MessageIndex {
			if messageUUID != existing.MessageUUID {
				return FrameReadCursor{}, fmt.Errorf("%w: identity differs at stored index", ErrReadCursorConflict)
			}
			return existing, nil
		}
	} else if !errors.Is(loadErr, sql.ErrNoRows) {
		return FrameReadCursor{}, fmt.Errorf("load stable read cursor: %w", loadErr)
	} else if repair {
		return FrameReadCursor{}, fmt.Errorf("%w: repair target does not exist", ErrReadCursorConflict)
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_read_cursors(root_frame_id,message_uuid,message_index,updated_at)
		VALUES(?,?,?,?) ON CONFLICT(root_frame_id) DO UPDATE SET
		message_uuid=excluded.message_uuid,message_index=excluded.message_index,updated_at=excluded.updated_at`,
		rootFrameID, nullableString(messageUUID), messageIndex, now); err != nil {
		return FrameReadCursor{}, fmt.Errorf("put frame read cursor: %w", err)
	}
	return FrameReadCursor{RootFrameID: rootFrameID, MessageUUID: messageUUID, MessageIndex: messageIndex, UpdatedAt: now}, nil
}

func (s *Store) putReadCursor(frameID, messageUUID string, messageIndex int) (FrameReadCursor, error) {
	rootFrameID, err := s.resolveRootFrameID(frameID)
	if err != nil {
		return FrameReadCursor{}, err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO frame_read_cursors (root_frame_id, message_uuid, message_index, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(root_frame_id) DO UPDATE SET
			message_uuid = excluded.message_uuid,
			message_index = excluded.message_index,
			updated_at = excluded.updated_at`,
		rootFrameID, nullableString(messageUUID), messageIndex, now,
	); err != nil {
		return FrameReadCursor{}, fmt.Errorf("put frame read cursor: %w", err)
	}
	return FrameReadCursor{RootFrameID: rootFrameID, MessageUUID: messageUUID, MessageIndex: messageIndex, UpdatedAt: now}, nil
}

func (s *Store) CancelFrame(frameID string) (Frame, *FrameEvent, error) {
	if s == nil || s.db == nil {
		return Frame{}, nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(frameID) == "" {
		return Frame{}, nil, errors.New("frame id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Frame{}, nil, fmt.Errorf("begin frame cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	frame, event, err := cancelFrameInTransaction(ctx, tx, frameID, s.now().UTC())
	if err != nil {
		return Frame{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Frame{}, nil, fmt.Errorf("commit frame cancellation: %w", err)
	}
	return frame, event, nil
}

func cancelFrameInTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	frameID string,
	now time.Time,
) (Frame, *FrameEvent, error) {
	var frame Frame
	if err := tx.QueryRowContext(ctx, `
		SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id, root_sequence,
			agent_name, status, conversation_type, name, created_at, updated_at
		FROM frames WHERE id = ?`, frameID,
	).Scan(
		&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
		&frame.RootSequence, &frame.AgentName, &frame.Status, &frame.ConversationType,
		&frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Frame{}, nil, fmt.Errorf("frame %q does not exist", frameID)
		}
		return Frame{}, nil, fmt.Errorf("load frame for cancellation: %w", err)
	}
	if frame.Status == "cancelled" {
		return frame, nil, nil
	}
	if frame.Status == "completed" || frame.Status == "failed" {
		return Frame{}, nil, fmt.Errorf("frame %q is already terminal with status %q", frameID, frame.Status)
	}
	previousStatus := frame.Status
	if _, err := tx.ExecContext(ctx,
		`UPDATE frames SET status = 'cancelled', updated_at = ? WHERE id = ?`,
		now, frameID,
	); err != nil {
		return Frame{}, nil, fmt.Errorf("cancel frame: %w", err)
	}
	if err := clearFramePendingInputsForCancellation(ctx, tx, frameID); err != nil {
		return Frame{}, nil, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`,
		frameID,
	).Scan(&sequence); err != nil {
		return Frame{}, nil, fmt.Errorf("allocate cancellation event sequence: %w", err)
	}
	payload := map[string]any{"previousStatus": previousStatus}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return Frame{}, nil, fmt.Errorf("encode cancellation event: %w", err)
	}
	event := &FrameEvent{
		ID: uuid.NewString(), FrameID: frameID, Sequence: sequence,
		Type: "frame_cancelled", Payload: payload, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
	); err != nil {
		return Frame{}, nil, fmt.Errorf("insert cancellation event: %w", err)
	}
	frame.Status = "cancelled"
	frame.UpdatedAt = now
	return frame, event, nil
}

func (s *Store) resolveRootFrameID(frameID string) (string, error) {
	var rootFrameID string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT root_frame_id FROM frames WHERE id = ?`, frameID,
	).Scan(&rootFrameID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("frame %q does not exist", frameID)
	}
	if err != nil {
		return "", fmt.Errorf("resolve root frame: %w", err)
	}
	return rootFrameID, nil
}
