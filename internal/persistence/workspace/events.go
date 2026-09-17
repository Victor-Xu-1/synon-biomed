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

type FrameEvent struct {
	ID        string         `json:"id"`
	FrameID   string         `json:"frameId"`
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"createdAt"`
}

type FrameEventInput struct {
	ID      string
	FrameID string
	Type    string
	Payload map[string]any
}

func (s *Store) AppendFrameEvent(input FrameEventInput) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(input.FrameID) == "" || strings.TrimSpace(input.Type) == "" {
		return FrameEvent{}, errors.New("frame event frame id and type are required")
	}
	input.ID = strings.TrimSpace(input.ID)
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("marshal frame event payload: %w", err)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("begin frame event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if input.ID != "" {
		existing, storedPayload, found, err := frameEventByID(ctx, tx, input.ID)
		if err != nil {
			return FrameEvent{}, err
		}
		if found {
			if existing.FrameID != input.FrameID || existing.Type != input.Type || storedPayload != string(rawPayload) {
				return FrameEvent{}, fmt.Errorf("frame event id %q already identifies a different frame event", input.ID)
			}
			if err := tx.Commit(); err != nil {
				return FrameEvent{}, fmt.Errorf("commit idempotent frame event lookup: %w", err)
			}
			return existing, nil
		}
	}
	var frameID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, input.FrameID).Scan(&frameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FrameEvent{}, fmt.Errorf("frame %q does not exist", input.FrameID)
		}
		return FrameEvent{}, fmt.Errorf("look up event frame: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`, input.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate frame event sequence: %w", err)
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	event := FrameEvent{ID: input.ID, FrameID: input.FrameID, Sequence: sequence, Type: input.Type, Payload: payload, CreatedAt: s.now().UTC()}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt); err != nil {
		return FrameEvent{}, fmt.Errorf("insert frame event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, fmt.Errorf("commit frame event: %w", err)
	}
	return event, nil
}

func frameEventByID(ctx context.Context, tx workspaceTransaction, id string) (FrameEvent, string, bool, error) {
	var event FrameEvent
	var rawPayload string
	err := tx.QueryRowContext(ctx, `
		SELECT id, frame_id, sequence, event_type, payload, created_at
		FROM frame_events WHERE id = ?`, id).Scan(
		&event.ID, &event.FrameID, &event.Sequence, &event.Type, &rawPayload, &event.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameEvent{}, "", false, nil
	}
	if err != nil {
		return FrameEvent{}, "", false, fmt.Errorf("get frame event by id: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return FrameEvent{}, "", false, fmt.Errorf("decode frame event by id: %w", err)
	}
	return event, rawPayload, true, nil
}

func (s *Store) ListFrameEvents(frameID string, afterSequence int64, limit int) ([]FrameEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(frameID) == "" {
		return nil, errors.New("frame id is required")
	}
	if afterSequence < 0 {
		return nil, errors.New("event cursor must be non-negative")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, frame_id, sequence, event_type, payload, created_at
		FROM frame_events WHERE frame_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, frameID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("query frame events: %w", err)
	}
	defer rows.Close()
	events := make([]FrameEvent, 0)
	for rows.Next() {
		var event FrameEvent
		var rawPayload string
		if err := rows.Scan(&event.ID, &event.FrameID, &event.Sequence, &event.Type, &rawPayload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan frame event: %w", err)
		}
		if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode frame event payload: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate frame events: %w", err)
	}
	return events, nil
}
