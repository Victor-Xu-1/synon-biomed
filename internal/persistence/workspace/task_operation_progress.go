package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// AppendTaskOperationProgress only observes the currently owned delivery. The
// lease and observation sequence are checked under one write authority, so a
// previous service cannot overwrite a replacement's progress or terminal state.
func (s *Store) AppendTaskOperationProgress(ctx context.Context, event OutboxEvent, progress map[string]any) error {
	operation, err := DecodeTaskOperation(event)
	if err != nil || operation.Observation == nil {
		return err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return err
	}
	return repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		cancelled, err := checkTaskOperation(ctx, tx, event)
		if err != nil {
			return err
		}
		if cancelled {
			return errors.New("cancelled task cannot publish running progress")
		}
		_, err = tx.AppendNextToolOperationObservation(ctx, *operation.Observation, "running", map[string]any{"progress": progress})
		return err
	})
}
