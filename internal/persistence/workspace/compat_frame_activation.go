package workspace

import (
	"context"
	"fmt"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type compatibilityFrameActivationMode uint8

const (
	compatibilityFrameActivationIdle compatibilityFrameActivationMode = iota
	compatibilityFrameActivationCompleted
)

// activateCompatibilityFrameRequest commits the frame state and presentation
// reset together. Readers must never observe a newly active frame carrying the
// previous attempt's terminal failure description.
func (s *Store) activateCompatibilityFrameRequest(
	ctx context.Context,
	frameID string,
	mode compatibilityFrameActivationMode,
) (bool, error) {
	repository := transcriptstore.NewRepository(s.db)
	activated := false
	err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		query := `UPDATE frames SET status = 'processing', updated_at = ?
			WHERE id = ? AND status NOT IN ('processing', 'running', 'awaiting_user_response', 'awaiting_plan_approval')`
		if mode == compatibilityFrameActivationCompleted {
			query = `UPDATE frames SET status = 'processing', updated_at = ?
				WHERE id = ? AND status = 'completed'`
		}
		result, err := tx.ExecContext(ctx, query, s.now().UTC(), frameID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return nil
		}
		if changed != 1 {
			return fmt.Errorf("activated %d compatibility frames, want one", changed)
		}
		if err := resetCompatibilityFrameTerminalPresentation(ctx, tx, frameID); err != nil {
			return err
		}
		activated = true
		return nil
	})
	return activated, err
}

func resetCompatibilityFrameTerminalPresentation(
	ctx context.Context,
	tx workspaceTransaction,
	frameID string,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id,status_description,completed_at,context_data)
		VALUES(?,'',NULL,'{}')
		ON CONFLICT(frame_id) DO UPDATE SET status_description='',completed_at=NULL`, frameID); err != nil {
		return fmt.Errorf("reset compatibility frame terminal presentation: %w", err)
	}
	return nil
}
