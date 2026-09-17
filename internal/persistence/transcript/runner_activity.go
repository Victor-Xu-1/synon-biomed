package transcript

import (
	"context"
	"errors"
	"time"
)

const runnerAttemptLeaseHandoffGrace = 2 * time.Minute

// CountActiveRunnerAttempts reports durable logical-task work with a live
// lease. An execution segment can temporarily have no in-memory owner while it
// is handing off a tool or provider continuation. A short grace after lease
// expiry closes the deployment race between the expiring segment and durable
// recovery; old orphan rows still stop blocking activation after that grace.
func (r *Repository) CountActiveRunnerAttempts(ctx context.Context) (int, error) {
	db := r.readDatabase()
	if db == nil {
		return 0, errors.New("transcript repository is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := r.now().UTC()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_runner_attempts
		WHERE status='running' AND finished_event_id IS NULL AND finished_at IS NULL
		  AND expires_at>?`, now.Add(-runnerAttemptLeaseHandoffGrace),
	).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ExpireRunnerAttemptsClaimedBefore fences leases owned by a previous server
// process. The startup timestamp is captured before the new process starts any
// runner workers, so work claimed by this process is never touched. Durable
// recovery can then resume the same logical attempt from its latest checkpoint
// immediately instead of displaying a live lease with no execution entity for
// the remainder of the old TTL.
func (r *Repository) ExpireRunnerAttemptsClaimedBefore(ctx context.Context, startup time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrSchemaUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startup = startup.UTC()
	if startup.IsZero() {
		return 0, errors.New("runtime startup timestamp is required")
	}
	now := r.now().UTC()
	result, err := r.db.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET expires_at=?
		WHERE status='running' AND finished_event_id IS NULL AND finished_at IS NULL
		  AND claimed_at<? AND expires_at>?`, now, startup, now)
	if err != nil {
		return 0, schemaError(err)
	}
	count, err := result.RowsAffected()
	return count, schemaError(err)
}
