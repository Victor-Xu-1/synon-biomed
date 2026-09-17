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

	"synon-go/internal/realtime"
)

const maxRealtimeEventPayloadBytes = 256 << 10

type RealtimeEvent struct {
	Sequence      int64                        `json:"sequence"`
	ID            string                       `json:"id"`
	UserID        string                       `json:"userId,omitempty"`
	ProjectID     string                       `json:"projectId,omitempty"`
	RootFrameID   string                       `json:"rootFrameId,omitempty"`
	FrameID       string                       `json:"frameId,omitempty"`
	Type          string                       `json:"type"`
	Kind          realtime.DeliveryKind        `json:"kind"`
	Payload       map[string]any               `json:"payload"`
	Invalidations []realtime.QueryInvalidation `json:"invalidations"`
	CreatedAt     time.Time                    `json:"createdAt"`
}

type RealtimeEventInput struct {
	ID          string         `json:"id"`
	UserID      string         `json:"userId,omitempty"`
	ProjectID   string         `json:"projectId,omitempty"`
	RootFrameID string         `json:"rootFrameId,omitempty"`
	FrameID     string         `json:"frameId,omitempty"`
	Type        string         `json:"type"`
	Payload     map[string]any `json:"payload"`
	OccurredAt  time.Time      `json:"occurredAt,omitempty"`
}

type RealtimeEventFilter struct {
	UserID          string
	IncludeGlobal   bool
	ProjectID       string
	RootFrameID     string
	FrameID         string
	Type            string
	AfterSequence   int64
	ThroughSequence int64
	Limit           int
	CursorReset     bool
}

func (s *Store) GetRealtimeEventByID(id string) (RealtimeEvent, bool, error) {
	if s == nil || s.db == nil {
		return RealtimeEvent{}, false, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return RealtimeEvent{}, false, errors.New("realtime event id is required")
	}
	event, _, _, found, err := s.realtimeEventByID(context.Background(), id)
	return event, found, err
}

func (s *Store) AppendRealtimeEvent(input RealtimeEventInput) (RealtimeEvent, error) {
	if s == nil || s.db == nil {
		return RealtimeEvent{}, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.Type = strings.TrimSpace(input.Type)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	spec, found := realtime.LookupEvent(input.Type)
	if !found {
		return RealtimeEvent{}, fmt.Errorf("%w: %s", realtime.ErrUnknownEvent, input.Type)
	}
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	invalidations := realtime.Invalidations(input.Type, payload)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return RealtimeEvent{}, fmt.Errorf("encode realtime event payload: %w", err)
	}
	if len(payloadJSON) > maxRealtimeEventPayloadBytes {
		return RealtimeEvent{}, fmt.Errorf("realtime event payload exceeds %d bytes", maxRealtimeEventPayloadBytes)
	}
	invalidationsJSON, err := json.Marshal(invalidations)
	if err != nil {
		return RealtimeEvent{}, fmt.Errorf("encode realtime event invalidations: %w", err)
	}
	now := input.OccurredAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	ctx := context.Background()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO realtime_events (
			id, user_id, project_id, root_frame_id, frame_id,
			event_type, event_kind, payload, invalidations, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		input.ID, input.UserID, input.ProjectID, input.RootFrameID, input.FrameID,
		input.Type, string(spec.Kind), string(payloadJSON), string(invalidationsJSON), now,
	)
	if err != nil {
		return RealtimeEvent{}, fmt.Errorf("append realtime event: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RealtimeEvent{}, fmt.Errorf("count appended realtime events: %w", err)
	}
	if changed == 0 {
		existing, storedPayload, storedInvalidations, found, err := s.realtimeEventByID(ctx, input.ID)
		if err != nil {
			return RealtimeEvent{}, err
		}
		if !found {
			return RealtimeEvent{}, errors.New("realtime event conflicted but could not be reloaded")
		}
		if existing.UserID != input.UserID || existing.ProjectID != input.ProjectID ||
			existing.RootFrameID != input.RootFrameID || existing.FrameID != input.FrameID ||
			existing.Type != input.Type || existing.Kind != spec.Kind ||
			storedPayload != string(payloadJSON) || storedInvalidations != string(invalidationsJSON) {
			return RealtimeEvent{}, fmt.Errorf("realtime event id %q already identifies a different realtime event", input.ID)
		}
		return existing, nil
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return RealtimeEvent{}, fmt.Errorf("resolve realtime event sequence: %w", err)
	}
	return RealtimeEvent{
		Sequence: sequence, ID: input.ID, UserID: input.UserID, ProjectID: input.ProjectID,
		RootFrameID: input.RootFrameID, FrameID: input.FrameID, Type: input.Type, Kind: spec.Kind,
		Payload: cloneFeedbackPayload(payload), Invalidations: invalidations, CreatedAt: now,
	}, nil
}

func (s *Store) realtimeEventByID(ctx context.Context, id string) (RealtimeEvent, string, string, bool, error) {
	var event RealtimeEvent
	var kind, payloadJSON, invalidationsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT sequence, id, user_id, project_id, root_frame_id, frame_id,
			event_type, event_kind, payload, invalidations, created_at
		FROM realtime_events WHERE id = ?`, id).Scan(
		&event.Sequence, &event.ID, &event.UserID, &event.ProjectID, &event.RootFrameID,
		&event.FrameID, &event.Type, &kind, &payloadJSON, &invalidationsJSON, &event.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RealtimeEvent{}, "", "", false, nil
	}
	if err != nil {
		return RealtimeEvent{}, "", "", false, fmt.Errorf("get realtime event by id: %w", err)
	}
	event.Kind = realtime.DeliveryKind(kind)
	if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
		return RealtimeEvent{}, "", "", false, fmt.Errorf("decode realtime event payload: %w", err)
	}
	if err := json.Unmarshal([]byte(invalidationsJSON), &event.Invalidations); err != nil {
		return RealtimeEvent{}, "", "", false, fmt.Errorf("decode realtime event invalidations: %w", err)
	}
	return event, payloadJSON, invalidationsJSON, true, nil
}

func (s *Store) ListRealtimeEvents(filter RealtimeEventFilter) ([]RealtimeEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	filter, includeGlobal, err := normalizeRealtimeEventFilter(filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT sequence, id, user_id, project_id, root_frame_id, frame_id,
			event_type, event_kind, payload, invalidations, created_at
		FROM realtime_events
		WHERE sequence > ? AND (? = 0 OR sequence <= ?)
			AND (user_id = ? OR (? = 1 AND user_id = ''))
			AND (? = '' OR project_id = ?)
			AND (? = '' OR root_frame_id = ?)
			AND (? = '' OR frame_id = ?)
			AND (? = '' OR event_type = ?)
		ORDER BY sequence LIMIT ?`,
		filter.AfterSequence, filter.ThroughSequence, filter.ThroughSequence,
		filter.UserID, includeGlobal,
		filter.ProjectID, filter.ProjectID, filter.RootFrameID, filter.RootFrameID,
		filter.FrameID, filter.FrameID, filter.Type, filter.Type, filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list realtime events: %w", err)
	}
	defer rows.Close()
	events := make([]RealtimeEvent, 0)
	for rows.Next() {
		var event RealtimeEvent
		var kind, payloadJSON, invalidationsJSON string
		if err := rows.Scan(
			&event.Sequence, &event.ID, &event.UserID, &event.ProjectID, &event.RootFrameID,
			&event.FrameID, &event.Type, &kind, &payloadJSON, &invalidationsJSON, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan realtime event: %w", err)
		}
		event.Kind = realtime.DeliveryKind(kind)
		if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode realtime event payload: %w", err)
		}
		if err := json.Unmarshal([]byte(invalidationsJSON), &event.Invalidations); err != nil {
			return nil, fmt.Errorf("decode realtime event invalidations: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate realtime events: %w", err)
	}
	return events, nil
}

func (s *Store) LatestRealtimeEventSequence(filter RealtimeEventFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	filter, includeGlobal, err := normalizeRealtimeEventFilter(filter)
	if err != nil {
		return 0, err
	}
	var sequence int64
	err = s.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(MAX(sequence), 0) FROM realtime_events
		WHERE (user_id = ? OR (? = 1 AND user_id = ''))
			AND (? = '' OR project_id = ?)
			AND (? = '' OR root_frame_id = ?)
			AND (? = '' OR frame_id = ?)
			AND (? = '' OR event_type = ?)`,
		filter.UserID, includeGlobal,
		filter.ProjectID, filter.ProjectID, filter.RootFrameID, filter.RootFrameID,
		filter.FrameID, filter.FrameID, filter.Type, filter.Type,
	).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("resolve latest realtime event sequence: %w", err)
	}
	return sequence, nil
}

func normalizeRealtimeEventFilter(filter RealtimeEventFilter) (RealtimeEventFilter, int, error) {
	filter.UserID = strings.TrimSpace(filter.UserID)
	filter.ProjectID = strings.TrimSpace(filter.ProjectID)
	filter.RootFrameID = strings.TrimSpace(filter.RootFrameID)
	filter.FrameID = strings.TrimSpace(filter.FrameID)
	filter.Type = strings.TrimSpace(filter.Type)
	if filter.UserID == "" {
		return RealtimeEventFilter{}, 0, errors.New("realtime event user id is required")
	}
	if filter.AfterSequence < 0 {
		return RealtimeEventFilter{}, 0, errors.New("realtime event cursor must be non-negative")
	}
	if filter.ThroughSequence < 0 {
		return RealtimeEventFilter{}, 0, errors.New("realtime event upper cursor must be non-negative")
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}
	includeGlobal := 0
	if filter.IncludeGlobal {
		includeGlobal = 1
	}
	return filter, includeGlobal, nil
}
