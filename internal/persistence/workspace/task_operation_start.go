package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// BeginTaskOperation records side-effect admission separately from transport
// claims. A retry before this point has not started a domain mutation.
func (s *Store) BeginTaskOperation(ctx context.Context, event OutboxEvent) (startedBefore, cancelled bool, err error) {
	operation, err := DecodeTaskOperation(event)
	if err != nil {
		return false, false, err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return false, false, err
	}
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		digest := sha256.Sum256([]byte(event.ClaimToken))
		claimHash := hex.EncodeToString(digest[:])
		var checkErr error
		cancelled, checkErr = checkTaskOperation(ctx, tx, event)
		if checkErr != nil || cancelled {
			return checkErr
		}
		var priorClaim string
		readErr := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.claim_hash') FROM frame_events
			WHERE frame_id=? AND event_type='task_operation_started' AND json_extract(payload,'$.operation_id')=?
			ORDER BY sequence DESC LIMIT 1`, operation.FrameID, operation.ID).Scan(&priorClaim)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if priorClaim == claimHash {
			// The same lease is not permission to invoke the adapter twice.
			return ErrOutboxClaimLost
		}
		startedBefore = readErr == nil
		_, err := appendFrameLifecycleEvent(ctx, tx, operation.FrameID, "task_operation_started",
			map[string]any{"operation_id": operation.ID, "claim_hash": claimHash}, s.now().UTC())
		return err
	})
	return
}
