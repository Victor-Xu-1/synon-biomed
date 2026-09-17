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

type ConfigureRoutineHostInput struct {
	ID           string
	RootFrameID  string
	OwnerUserID  string
	Label        *string
	OnTick       string
	EveryMinutes int
	MutationID   string
}

// ConfigureRoutineHost atomically creates or reconfigures the one routine
// owned by a root frame. Ownership is checked again inside the transaction.
func (s *Store) ConfigureRoutineHost(ctx context.Context, input ConfigureRoutineHostInput) (Routine, error) {
	if s == nil || s.db == nil {
		return Routine{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return Routine{}, errors.New("routine host context is required")
	}
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.RootFrameID) == "" || strings.TrimSpace(input.OwnerUserID) == "" || strings.TrimSpace(input.OnTick) == "" {
		return Routine{}, errors.New("routine id, root frame, owner, and on_tick are required")
	}
	if input.EveryMinutes < 5 || input.EveryMinutes > 1440 {
		return Routine{}, errors.New("routine interval must be between 5 and 1440 minutes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Routine{}, fmt.Errorf("begin routine host configure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rootFrameID, ownerUserID string
	if err := tx.QueryRowContext(ctx, `
		SELECT f.root_frame_id, p.user_id
		FROM frames f JOIN projects p ON p.id = f.project_id
		WHERE f.id = ?`, input.RootFrameID).Scan(&rootFrameID, &ownerUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Routine{}, errors.New("routine root frame does not exist")
		}
		return Routine{}, fmt.Errorf("verify routine host owner: %w", err)
	}
	if rootFrameID != input.RootFrameID || ownerUserID != input.OwnerUserID {
		return Routine{}, errors.New("routine root frame is not owned by the execution user")
	}
	now := s.now().UTC()
	nextDue := now.Add(time.Duration(input.EveryMinutes) * time.Minute)
	var label any
	if input.Label != nil {
		label = *input.Label
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO routine_schedules
			(id, root_frame_id, owner_user_id, label, on_tick, every_minutes, enabled, next_due, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(root_frame_id) DO UPDATE SET
			owner_user_id = excluded.owner_user_id,
			label = excluded.label,
			on_tick = excluded.on_tick,
			every_minutes = excluded.every_minutes,
			enabled = 1,
			paused_reason = NULL,
			next_due = excluded.next_due,
			updated_at = excluded.updated_at`,
		input.ID, input.RootFrameID, input.OwnerUserID, label, input.OnTick,
		input.EveryMinutes, nextDue, now, now)
	if err != nil {
		return Routine{}, fmt.Errorf("upsert routine host configuration: %w", err)
	}
	routine, err := scanRoutine(tx.QueryRowContext(ctx, routineSelect+` WHERE root_frame_id = ?`, input.RootFrameID))
	if err != nil {
		return Routine{}, fmt.Errorf("read routine host configuration: %w", err)
	}
	mutationID := strings.TrimSpace(input.MutationID)
	if mutationID == "" {
		mutationID = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%v", input.RootFrameID, input.EveryMinutes, input.OnTick, input.Label))))
	}
	if err := s.enqueueRoutineRealtimeTx(ctx, tx, routine, "configured", "routine-host-configure:"+mutationID); err != nil {
		return Routine{}, fmt.Errorf("enqueue routine host configuration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Routine{}, fmt.Errorf("commit routine host configuration: %w", err)
	}
	return routine, nil
}

func (s *Store) GetRoutineByRoot(ctx context.Context, rootFrameID string) (Routine, bool, error) {
	if s == nil || s.db == nil {
		return Routine{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil || strings.TrimSpace(rootFrameID) == "" {
		return Routine{}, false, errors.New("routine host context and root frame are required")
	}
	routine, err := scanRoutine(s.db.QueryRowContext(ctx, routineSelect+` WHERE root_frame_id = ?`, rootFrameID))
	if errors.Is(err, sql.ErrNoRows) {
		return Routine{}, false, nil
	}
	if err != nil {
		return Routine{}, false, fmt.Errorf("get routine by root: %w", err)
	}
	return routine, true, nil
}

// RecordRoutineHostDone records work/idle state without completing the
// scheduler lease. Scheduler finalization remains responsible for tick_count,
// next_due, and lock release, preventing a host.done call from double-firing.
func (s *Store) RecordRoutineHostDone(ctx context.Context, rootFrameID, ownerUserID string, claim Routine, hadWork bool, summary string) (Routine, error) {
	if s == nil || s.db == nil {
		return Routine{}, errors.New("workspace store is closed")
	}
	if ctx == nil || strings.TrimSpace(rootFrameID) == "" || strings.TrimSpace(ownerUserID) == "" || claim.LockedAt == nil || claim.LockedAt.IsZero() || claim.ClaimGeneration < 1 || strings.TrimSpace(claim.ClaimToken) == "" {
		return Routine{}, errors.New("routine host context, root frame, owner, and active claim are required")
	}
	var routine Routine
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		now := s.now().UTC()
		result, err := tx.ExecContext(ctx, `
			UPDATE routine_schedules
			SET host_done_locked_at = ?, host_done_claim_generation = ?,
				host_done_claim_token = ?, host_had_work = ?, host_done_summary = ?,
				host_done_at = ?, updated_at = ?
			WHERE root_frame_id = ? AND owner_user_id = ? AND locked_at = ?
				AND claim_generation = ? AND claim_token = ?`,
			claim.LockedAt.UTC(), claim.ClaimGeneration, claim.ClaimToken,
			hadWork, summary, now, now, rootFrameID, ownerUserID,
			claim.LockedAt.UTC(), claim.ClaimGeneration, claim.ClaimToken)
		if err != nil {
			return fmt.Errorf("record routine host done: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count routine host done: %w", err)
		}
		if changed != 1 {
			return errors.New("routine is not claimed by this execution generation")
		}
		routine, err = scanRoutine(tx.QueryRowContext(ctx, routineSelect+` WHERE root_frame_id = ?`, rootFrameID))
		if err != nil {
			return fmt.Errorf("read routine after host done: %w", err)
		}
		fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%t\x00%s", rootFrameID, claim.ClaimGeneration, claim.ClaimToken, hadWork, summary)))
		if err := s.enqueueRoutineRealtimeTx(ctx, tx, routine, "host_done", fmt.Sprintf("routine-host-done:%x", fingerprint)); err != nil {
			return fmt.Errorf("enqueue routine host done: %w", err)
		}
		return nil
	})
	return routine, err
}
