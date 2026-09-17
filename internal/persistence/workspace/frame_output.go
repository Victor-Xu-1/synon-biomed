package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GetFrameOutputData returns the durable model/runtime result associated with a
// frame. The boolean distinguishes an absent output from an explicitly empty one.
func (s *Store) GetFrameOutputData(frameID string) (map[string]any, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return nil, false, errors.New("frame id is required")
	}
	var raw sql.NullString
	err := s.db.QueryRowContext(context.Background(), `
		SELECT output_data FROM frame_runtime_metadata WHERE frame_id = ?`, frameID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get frame output data: %w", err)
	}
	if !raw.Valid {
		return nil, false, nil
	}
	output := map[string]any{}
	if err := json.Unmarshal([]byte(raw.String), &output); err != nil {
		return nil, false, fmt.Errorf("decode frame output data: %w", err)
	}
	return output, true, nil
}

// SetFrameOutputData persists a frame result without disturbing other runtime
// metadata. Runtime writers and migrations can use this for terminal state.
func (s *Store) SetFrameOutputData(frameID string, output map[string]any) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return errors.New("frame id is required")
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode frame output data: %w", err)
	}
	result, err := s.db.ExecContext(context.Background(), `
		INSERT INTO frame_runtime_metadata (frame_id, output_data, context_data)
		SELECT id, ?, '{}' FROM frames WHERE id = ?
		ON CONFLICT(frame_id) DO UPDATE SET output_data = excluded.output_data`, string(raw), frameID)
	if err != nil {
		return fmt.Errorf("set frame output data: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect frame output data update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("frame %q does not exist", frameID)
	}
	return nil
}
