package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRoutineClaimLost = errors.New("routine claim generation is no longer active")
var ErrRoutineCompletionConflict = errors.New("routine completion idempotency key identifies different content")

func (s *Store) RenewClaimedRoutineTick(ctx context.Context, claim Routine, at time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(claim.ID) == "" || claim.ClaimGeneration < 1 || strings.TrimSpace(claim.ClaimToken) == "" {
		return errors.New("routine renewal requires an active claim generation and token")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE routine_schedules SET locked_at = ?, updated_at = ?
		WHERE id = ? AND claim_generation = ? AND claim_token = ? AND locked_at IS NOT NULL`,
		at.UTC(), at.UTC(), claim.ID, claim.ClaimGeneration, claim.ClaimToken)
	if err != nil {
		return fmt.Errorf("renew routine claim: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count renewed routine claim: %w", err)
	}
	if changed != 1 {
		return ErrRoutineClaimLost
	}
	return nil
}

func (s *Store) claimNextDueRoutineFencedTx(ctx context.Context, tx *sql.Tx, now time.Time, lockTTL time.Duration, ownerUserID, claimToken string) (Routine, bool, error) {
	if tx == nil {
		return Routine{}, false, errors.New("routine transaction is required")
	}
	if lockTTL <= 0 {
		return Routine{}, false, errors.New("routine lock ttl must be positive")
	}
	now, ownerUserID, claimToken = now.UTC(), strings.TrimSpace(ownerUserID), strings.TrimSpace(claimToken)
	if claimToken == "" {
		return Routine{}, false, errors.New("routine claim token is required")
	}
	query := routineSelect + ` WHERE claim_token = ?`
	args := []any{claimToken}
	if ownerUserID != "" {
		query += ` AND owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	existing, err := scanRoutine(tx.QueryRowContext(ctx, query, args...))
	if err == nil {
		return existing, existing.LockedAt != nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Routine{}, false, fmt.Errorf("look up idempotent routine claim: %w", err)
	}

	query = routineSelect + ` WHERE enabled = 1 AND next_due <= ? AND (locked_at IS NULL OR locked_at <= ?)`
	args = []any{now, now.Add(-lockTTL)}
	if ownerUserID != "" {
		query += ` AND owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	query += ` ORDER BY next_due, id LIMIT 1`
	routine, err := scanRoutine(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Routine{}, false, nil
	}
	if err != nil {
		return Routine{}, false, fmt.Errorf("select due routine: %w", err)
	}
	nextGeneration := routine.ClaimGeneration + 1
	result, err := tx.ExecContext(ctx, `
		UPDATE routine_schedules SET locked_at = ?, claim_generation = ?, claim_token = ?, updated_at = ?,
			host_done_locked_at = NULL, host_done_claim_generation = NULL,
			host_done_claim_token = NULL, host_had_work = NULL,
			host_done_summary = NULL, host_done_at = NULL
		WHERE id = ? AND claim_generation = ? AND (locked_at IS NULL OR locked_at <= ?)`,
		now, nextGeneration, claimToken, now, routine.ID, routine.ClaimGeneration, now.Add(-lockTTL))
	if err != nil {
		return Routine{}, false, fmt.Errorf("lock due routine: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Routine{}, false, fmt.Errorf("count routine lock: %w", err)
	}
	if changed != 1 {
		return Routine{}, false, nil
	}
	routine.LockedAt, routine.UpdatedAt = &now, now
	routine.ClaimGeneration, routine.ClaimToken = nextGeneration, claimToken
	return routine, true, nil
}

// completeRoutineTickFencedTx is the one completion implementation used by
// scheduler, HTTP compatibility, and host.done marker consumption.
func (s *Store) completeRoutineTickFencedTx(ctx context.Context, tx *sql.Tx, claim Routine, at time.Time, successful bool, result string) (Routine, bool, error) {
	if tx == nil {
		return Routine{}, false, errors.New("routine transaction is required")
	}
	if strings.TrimSpace(claim.ID) == "" || claim.ClaimGeneration < 1 || strings.TrimSpace(claim.ClaimToken) == "" || claim.LockedAt == nil {
		return Routine{}, false, errors.New("routine completion requires an active claim generation and token")
	}
	at = at.UTC()
	current, err := scanRoutine(tx.QueryRowContext(ctx, routineSelect+` WHERE id = ?`, claim.ID))
	if err != nil {
		return Routine{}, false, fmt.Errorf("look up routine completion: %w", err)
	}
	if current.ClaimGeneration != claim.ClaimGeneration || current.ClaimToken != claim.ClaimToken {
		return Routine{}, false, ErrRoutineClaimLost
	}
	if current.LockedAt == nil {
		var storedGeneration *int64
		var storedToken, storedHash *string
		var storedSuccessful *bool
		var storedAt *time.Time
		if err := tx.QueryRowContext(ctx, `
			SELECT completion_claim_generation, completion_claim_token, completion_successful,
				completion_at, completion_result_hash FROM routine_schedules WHERE id = ?`, claim.ID).Scan(
			&storedGeneration, &storedToken, &storedSuccessful, &storedAt, &storedHash); err != nil {
			return Routine{}, false, fmt.Errorf("read routine completion idempotency record: %w", err)
		}
		resultHash := fmt.Sprintf("%x", sha256.Sum256([]byte(result)))
		if storedGeneration == nil || storedToken == nil || storedSuccessful == nil || storedAt == nil || storedHash == nil ||
			*storedGeneration != claim.ClaimGeneration || *storedToken != claim.ClaimToken ||
			*storedSuccessful != successful || !storedAt.Equal(at) || *storedHash != resultHash {
			return Routine{}, false, ErrRoutineCompletionConflict
		}
		return current, false, nil
	}
	var markerLockedAt *time.Time
	var markerGeneration *int64
	var markerToken *string
	var markerHadWork *bool
	if err := tx.QueryRowContext(ctx, `SELECT host_done_locked_at, host_done_claim_generation, host_done_claim_token, host_had_work FROM routine_schedules WHERE id = ?`, claim.ID).Scan(&markerLockedAt, &markerGeneration, &markerToken, &markerHadWork); err != nil {
		return Routine{}, false, fmt.Errorf("read routine host done marker: %w", err)
	}
	hadWork := successful
	if markerLockedAt != nil && markerGeneration != nil && markerToken != nil && markerHadWork != nil &&
		*markerGeneration == current.ClaimGeneration && *markerToken == current.ClaimToken {
		hadWork = *markerHadWork
	}
	nextDue := at.Add(time.Duration(current.EveryMinutes) * time.Minute)
	var lastOK any
	if successful {
		lastOK = at
	}
	update, err := tx.ExecContext(ctx, `
		UPDATE routine_schedules SET locked_at = NULL, next_due = ?, tick_count = tick_count + 1,
			last_fire_at = ?, last_ok_at = COALESCE(?, last_ok_at),
			idle_streak = CASE WHEN ? THEN 0 ELSE idle_streak + 1 END,
			last_results = ?, updated_at = ?, host_done_locked_at = NULL,
			host_done_claim_generation = NULL, host_done_claim_token = NULL,
			host_had_work = NULL, host_done_summary = NULL, host_done_at = NULL,
			completion_claim_generation = ?, completion_claim_token = ?, completion_successful = ?,
			completion_at = ?, completion_result_hash = ?
		WHERE id = ? AND locked_at IS NOT NULL AND claim_generation = ? AND claim_token = ?`,
		nextDue, at, lastOK, hadWork, result, at,
		claim.ClaimGeneration, claim.ClaimToken, successful, at, fmt.Sprintf("%x", sha256.Sum256([]byte(result))),
		claim.ID, claim.ClaimGeneration, claim.ClaimToken)
	if err != nil {
		return Routine{}, false, fmt.Errorf("complete routine tick: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return Routine{}, false, fmt.Errorf("count completed routine tick: %w", err)
	}
	if changed != 1 {
		return Routine{}, false, ErrRoutineClaimLost
	}
	completed, err := scanRoutine(tx.QueryRowContext(ctx, routineSelect+` WHERE id = ?`, claim.ID))
	if err != nil {
		return Routine{}, false, fmt.Errorf("read completed routine: %w", err)
	}
	return completed, true, nil
}
