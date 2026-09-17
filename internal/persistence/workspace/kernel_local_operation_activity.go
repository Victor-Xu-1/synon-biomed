package workspace

import (
	"context"
	"errors"
)

// CountActiveKernelLocalOperations includes policy, handoff and execution
// states only while their owning runner attempt is still active. A paused or
// terminal task can retain an approved operation for an explicit lossless
// continuation without blocking unrelated verified deployments forever.
func (s *Store) CountActiveKernelLocalOperations(ctx context.Context) (int, error) {
	db := s.readDatabase()
	if db == nil {
		return 0, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM kernel_local_operations AS operation
		WHERE operation.state IN ('pending_approval','approved','prepared','started')
		  AND EXISTS (
			SELECT 1
			FROM transcript_runner_attempts AS attempt
			WHERE attempt.stream_uid=operation.stream_uid
			  AND (attempt.attempt=operation.source_runner_attempt OR attempt.attempt=operation.runner_attempt)
			  AND attempt.status='running'
			  AND attempt.finished_event_id IS NULL
			  AND attempt.finished_at IS NULL
			  AND attempt.expires_at>?
		  )`, now,
	).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
