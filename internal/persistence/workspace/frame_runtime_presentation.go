package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// FrameRuntimePresentationInput updates runtime-owned presentation fields while
// preserving fields omitted by the caller.
type FrameRuntimePresentationInput struct {
	Model             *string
	StatusDescription *string
}

func (s *Store) UpdateFrameRuntimePresentation(frameID string, input FrameRuntimePresentationInput) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return errors.New("frame id is required")
	}
	if input.Model == nil && input.StatusDescription == nil {
		return errors.New("at least one runtime presentation field is required")
	}

	var model any
	if input.Model != nil {
		model = strings.TrimSpace(*input.Model)
	}
	var statusDescription any
	if input.StatusDescription != nil {
		statusDescription = strings.TrimSpace(*input.StatusDescription)
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin frame runtime presentation update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, model, status_description, context_data)
		SELECT id, ?, ?, '{}' FROM frames WHERE id = ?
		ON CONFLICT(frame_id) DO UPDATE SET
			model = CASE WHEN ? THEN excluded.model ELSE frame_runtime_metadata.model END,
			status_description = CASE WHEN ? THEN excluded.status_description ELSE frame_runtime_metadata.status_description END`,
		model, statusDescription, frameID, input.Model != nil, input.StatusDescription != nil)
	if err != nil {
		return fmt.Errorf("update frame runtime presentation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect frame runtime presentation update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("frame %q does not exist", frameID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), frameID); err != nil {
		return fmt.Errorf("advance frame runtime presentation trace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit frame runtime presentation update: %w", err)
	}
	return nil
}
