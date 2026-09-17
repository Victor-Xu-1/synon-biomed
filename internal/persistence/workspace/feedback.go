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

const maxFeedbackPayloadBytes = 64 << 10

type FeedbackRecord struct {
	ID          string         `json:"id"`
	UserID      string         `json:"userId"`
	Kind        string         `json:"kind"`
	RootFrameID string         `json:"rootFrameId,omitempty"`
	Payload     map[string]any `json:"payload"`
	CreatedAt   time.Time      `json:"createdAt"`
}

type FeedbackInput struct {
	ID          string
	UserID      string
	Kind        string
	RootFrameID string
	Payload     map[string]any
}

func (s *Store) SaveSafetyFeedback(input FeedbackInput) (FeedbackRecord, bool, error) {
	if s == nil || s.db == nil {
		return FeedbackRecord{}, false, errors.New("workspace store is closed")
	}
	input.Kind = "safety"
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.UserID == "" || input.RootFrameID == "" {
		return FeedbackRecord{}, false, errors.New("safety feedback user id and root frame id are required")
	}
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FeedbackRecord{}, false, fmt.Errorf("encode feedback payload: %w", err)
	}
	if len(raw) > maxFeedbackPayloadBytes {
		return FeedbackRecord{}, false, fmt.Errorf("feedback payload exceeds %d bytes", maxFeedbackPayloadBytes)
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(context.Background(), `
		INSERT INTO runtime_feedback (id, user_id, kind, root_frame_id, payload, created_at)
		VALUES (?, ?, 'safety', ?, ?, ?)
		ON CONFLICT DO NOTHING`, input.ID, input.UserID, input.RootFrameID, string(raw), now)
	if err != nil {
		return FeedbackRecord{}, false, fmt.Errorf("save safety feedback: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return FeedbackRecord{}, false, fmt.Errorf("inspect safety feedback write: %w", err)
	}
	if inserted == 1 {
		return FeedbackRecord{ID: input.ID, UserID: input.UserID, Kind: "safety", RootFrameID: input.RootFrameID, Payload: cloneFeedbackPayload(payload), CreatedAt: now}, false, nil
	}
	record, found, err := s.getSafetyFeedback(input.UserID, input.RootFrameID)
	if err != nil {
		return FeedbackRecord{}, false, err
	}
	if !found {
		return FeedbackRecord{}, false, errors.New("safety feedback conflict did not resolve to an existing record")
	}
	return record, true, nil
}

func (s *Store) getSafetyFeedback(userID, rootFrameID string) (FeedbackRecord, bool, error) {
	var record FeedbackRecord
	var raw string
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, kind, root_frame_id, payload, created_at
		FROM runtime_feedback
		WHERE user_id = ? AND root_frame_id = ? AND kind = 'safety'
		LIMIT 1`, userID, rootFrameID).Scan(
		&record.ID, &record.UserID, &record.Kind, &record.RootFrameID, &raw, &record.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FeedbackRecord{}, false, nil
	}
	if err != nil {
		return FeedbackRecord{}, false, fmt.Errorf("get safety feedback: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &record.Payload); err != nil {
		return FeedbackRecord{}, false, fmt.Errorf("decode safety feedback payload: %w", err)
	}
	return record, true, nil
}

func (s *Store) SaveFeedback(input FeedbackInput) (FeedbackRecord, error) {
	if s == nil || s.db == nil {
		return FeedbackRecord{}, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.UserID == "" {
		return FeedbackRecord{}, errors.New("feedback user id is required")
	}
	if input.Kind != "general" && input.Kind != "safety" {
		return FeedbackRecord{}, errors.New("feedback kind must be general or safety")
	}
	if input.Kind == "safety" && input.RootFrameID == "" {
		return FeedbackRecord{}, errors.New("safety feedback root frame id is required")
	}
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FeedbackRecord{}, fmt.Errorf("encode feedback payload: %w", err)
	}
	if len(raw) > maxFeedbackPayloadBytes {
		return FeedbackRecord{}, fmt.Errorf("feedback payload exceeds %d bytes", maxFeedbackPayloadBytes)
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO runtime_feedback (id, user_id, kind, root_frame_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		input.ID, input.UserID, input.Kind, nullableString(input.RootFrameID), string(raw), now,
	); err != nil {
		return FeedbackRecord{}, fmt.Errorf("save feedback: %w", err)
	}
	return FeedbackRecord{
		ID: input.ID, UserID: input.UserID, Kind: input.Kind,
		RootFrameID: input.RootFrameID, Payload: cloneFeedbackPayload(payload), CreatedAt: now,
	}, nil
}

func (s *Store) ListFeedbackForUser(userID string, limit int) ([]FeedbackRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("feedback user id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, user_id, kind, root_frame_id, payload, created_at
		FROM runtime_feedback WHERE user_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	defer rows.Close()
	records := make([]FeedbackRecord, 0)
	for rows.Next() {
		var record FeedbackRecord
		var rootFrameID sql.NullString
		var raw string
		if err := rows.Scan(&record.ID, &record.UserID, &record.Kind, &rootFrameID, &raw, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan feedback: %w", err)
		}
		record.RootFrameID = rootFrameID.String
		if err := json.Unmarshal([]byte(raw), &record.Payload); err != nil {
			return nil, fmt.Errorf("decode feedback payload: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate feedback: %w", err)
	}
	return records, nil
}

func cloneFeedbackPayload(payload map[string]any) map[string]any {
	raw, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	clone := map[string]any{}
	if err := json.Unmarshal(raw, &clone); err != nil {
		return map[string]any{}
	}
	return clone
}
