package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RunnerTaskMetricsAuthority identifies durable metric scopes for an entire
// frame task and its latest logical input revision. Retries remain part of the
// logical revision that admitted them.
type RunnerTaskMetricsAuthority struct {
	TaskAttempts        []int64
	LatestAttempts      []int64
	TaskToolCallCount   int64
	LatestToolCallCount int64
}

// GetRunnerTaskMetricsAuthority returns task-scoped counters without deriving
// them from the bounded web transcript. The owner check is performed before
// reading either runner attempts or tool-call batches.
func (r *Repository) GetRunnerTaskMetricsAuthority(
	ctx context.Context,
	streamUID, ownerID string,
	inputRevision int64,
) (RunnerTaskMetricsAuthority, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerTaskMetricsAuthority{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" || inputRevision <= 0 {
		return RunnerTaskMetricsAuthority{}, false, errors.New("stream, owner, and a positive input revision are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMetricsAuthority{}, false, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT attempt,claimed_input_revision FROM transcript_runner_attempts
		WHERE stream_uid=? ORDER BY attempt`, streamUID)
	if err != nil {
		return RunnerTaskMetricsAuthority{}, false, schemaError(err)
	}
	defer rows.Close()
	result := RunnerTaskMetricsAuthority{}
	for rows.Next() {
		var attempt, claimedInputRevision int64
		if err := rows.Scan(&attempt, &claimedInputRevision); err != nil {
			return RunnerTaskMetricsAuthority{}, false, schemaError(err)
		}
		if attempt <= 0 || claimedInputRevision <= 0 {
			return RunnerTaskMetricsAuthority{}, false, ErrEventConflict
		}
		result.TaskAttempts = append(result.TaskAttempts, attempt)
		if claimedInputRevision == inputRevision {
			result.LatestAttempts = append(result.LatestAttempts, attempt)
		}
	}
	if err := rows.Err(); err != nil {
		return RunnerTaskMetricsAuthority{}, false, schemaError(err)
	}
	if len(result.TaskAttempts) == 0 {
		return RunnerTaskMetricsAuthority{}, false, nil
	}

	readToolCallCount := func(query string, arguments ...any) (int64, error) {
		var callCount sql.NullInt64
		if err := db.QueryRowContext(ctx, query, arguments...).Scan(&callCount); err != nil {
			return 0, schemaError(err)
		}
		if !callCount.Valid {
			return 0, nil
		}
		if callCount.Int64 < 0 {
			return 0, ErrEventConflict
		}
		return callCount.Int64, nil
	}
	result.TaskToolCallCount, err = readToolCallCount(`
		SELECT SUM(call_count) FROM transcript_tool_call_batches
		WHERE owner_user_id=? AND stream_uid=?`, ownerID, streamUID)
	if err != nil {
		return RunnerTaskMetricsAuthority{}, false, err
	}
	result.LatestToolCallCount, err = readToolCallCount(`
		SELECT SUM(call_count) FROM transcript_tool_call_batches
		WHERE owner_user_id=? AND stream_uid=? AND admitted_input_revision=?`,
		ownerID, streamUID, inputRevision,
	)
	if err != nil {
		return RunnerTaskMetricsAuthority{}, false, err
	}
	return result, true, nil
}

// GetRunnerTaskTimingSummary returns cumulative active runner time for the
// whole frame task. It deliberately excludes idle gaps between user rounds.
func (r *Repository) GetRunnerTaskTimingSummary(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerTaskTimingSummary, bool, error) {
	db := r.readDatabase()
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if db == nil {
		return RunnerTaskTimingSummary{}, false, ErrSchemaUnavailable
	}
	if streamUID == "" || ownerID == "" {
		return RunnerTaskTimingSummary{}, false, errors.New("stream and owner are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskTimingSummary{}, false, err
	}

	var summary RunnerTaskTimingSummary
	var latestFinishedAt sql.NullTime
	var latestExpiresAt time.Time
	err := db.QueryRowContext(ctx, `
		SELECT stream_uid,attempt,claimed_input_revision,status,expires_at,finished_at
		FROM transcript_runner_attempts
		WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, streamUID,
	).Scan(&summary.StreamUID, &summary.Attempt, &summary.InputRevision, &summary.Status, &latestExpiresAt, &latestFinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerTaskTimingSummary{}, false, nil
	}
	if err != nil {
		return RunnerTaskTimingSummary{}, false, schemaError(err)
	}
	summary.ObservedAt = r.now().UTC()
	if latestFinishedAt.Valid {
		finishedAt := latestFinishedAt.Time.UTC()
		summary.FinishedAt = &finishedAt
	}
	summary.Active = summary.Status == "running" && summary.FinishedAt == nil && latestExpiresAt.After(summary.ObservedAt)
	if summary.Status == "running" && summary.FinishedAt != nil || summary.Status != "running" && summary.FinishedAt == nil {
		return RunnerTaskTimingSummary{}, false, ErrEventConflict
	}

	rows, err := db.QueryContext(ctx, `
		SELECT attempt,status,claimed_at,expires_at,finished_at
		FROM transcript_runner_attempts
		WHERE stream_uid=? ORDER BY attempt`, streamUID)
	if err != nil {
		return RunnerTaskTimingSummary{}, false, schemaError(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var attempt int64
		var status string
		var claimedAt time.Time
		var expiresAt time.Time
		var finishedAt sql.NullTime
		if err := rows.Scan(&attempt, &status, &claimedAt, &expiresAt, &finishedAt); err != nil {
			return RunnerTaskTimingSummary{}, false, schemaError(err)
		}
		claimedAt = claimedAt.UTC()
		if attempt <= 0 || claimedAt.IsZero() {
			return RunnerTaskTimingSummary{}, false, ErrEventConflict
		}
		if !found || claimedAt.Before(summary.StartedAt) {
			summary.StartedAt = claimedAt
			found = true
		}
		end := summary.ObservedAt
		if finishedAt.Valid {
			if status == "running" {
				return RunnerTaskTimingSummary{}, false, ErrEventConflict
			}
			end = finishedAt.Time.UTC()
		} else if attempt != summary.Attempt || status != "running" {
			return RunnerTaskTimingSummary{}, false, ErrEventConflict
		} else if expiresAt.Before(end) {
			end = expiresAt.UTC()
		}
		if end.Before(claimedAt) {
			return RunnerTaskTimingSummary{}, false, ErrEventConflict
		}
		segment := end.Sub(claimedAt)
		if segment < 0 || summary.Elapsed > time.Duration(1<<63-1)-segment {
			return RunnerTaskTimingSummary{}, false, ErrEventConflict
		}
		summary.Elapsed += segment
	}
	if err := rows.Err(); err != nil {
		return RunnerTaskTimingSummary{}, false, schemaError(err)
	}
	if !found || summary.StartedAt.IsZero() {
		return RunnerTaskTimingSummary{}, false, ErrEventConflict
	}
	return summary, true, nil
}
