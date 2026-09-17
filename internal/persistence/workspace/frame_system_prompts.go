package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxFrameSystemPromptPayloadBytes = 4 << 20

type FrameSystemPromptSnapshot struct {
	FrameID   string         `json:"frameId"`
	Hash      string         `json:"hash"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

func (s *Store) PutFrameSystemPromptSnapshot(
	ctx context.Context,
	frameID string,
	payload map[string]any,
) (FrameSystemPromptSnapshot, error) {
	if s == nil || s.db == nil {
		return FrameSystemPromptSnapshot{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" || payload == nil {
		return FrameSystemPromptSnapshot{}, errors.New("frame system prompt requires frame id and payload")
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) == 0 || len(raw) > maxFrameSystemPromptPayloadBytes {
		return FrameSystemPromptSnapshot{}, errors.New("frame system prompt payload is invalid or too large")
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	detached := map[string]any{}
	if err := json.Unmarshal(raw, &detached); err != nil {
		return FrameSystemPromptSnapshot{}, errors.New("frame system prompt payload is invalid")
	}
	updatedAt := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO frame_system_prompts(frame_id,hash,updated_at,payload)
		VALUES(?,?,?,?)
		ON CONFLICT(frame_id) DO UPDATE SET
			hash=excluded.hash, updated_at=excluded.updated_at, payload=excluded.payload`,
		frameID, hash, updatedAt, string(raw),
	); err != nil {
		return FrameSystemPromptSnapshot{}, fmt.Errorf("persist frame system prompt: %w", err)
	}
	return FrameSystemPromptSnapshot{
		FrameID: frameID, Hash: hash, Payload: detached, UpdatedAt: updatedAt,
	}, nil
}

func (s *Store) GetFrameSystemPromptSnapshot(
	ctx context.Context,
	frameID string,
) (FrameSystemPromptSnapshot, bool, error) {
	if s == nil || s.db == nil {
		return FrameSystemPromptSnapshot{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return FrameSystemPromptSnapshot{}, false, errors.New("frame id is required")
	}
	var snapshot FrameSystemPromptSnapshot
	var raw string
	err := s.db.QueryRowContext(ctx, `
		SELECT frame_id,hash,payload,updated_at FROM frame_system_prompts WHERE frame_id=?`, frameID,
	).Scan(&snapshot.FrameID, &snapshot.Hash, &raw, &snapshot.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameSystemPromptSnapshot{}, false, nil
	}
	if err != nil {
		return FrameSystemPromptSnapshot{}, false, fmt.Errorf("load frame system prompt: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxFrameSystemPromptPayloadBytes || json.Unmarshal([]byte(raw), &snapshot.Payload) != nil {
		return FrameSystemPromptSnapshot{}, false, errors.New("stored frame system prompt payload is invalid")
	}
	digest := sha256.Sum256([]byte(raw))
	if snapshot.Hash != hex.EncodeToString(digest[:]) {
		return FrameSystemPromptSnapshot{}, false, errors.New("stored frame system prompt hash does not match payload")
	}
	snapshot.UpdatedAt = snapshot.UpdatedAt.UTC()
	return snapshot, true, nil
}
