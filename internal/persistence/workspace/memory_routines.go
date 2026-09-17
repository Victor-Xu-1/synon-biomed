package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Memory struct {
	ID                string     `json:"id"`
	UserID            string     `json:"userId"`
	Body              string     `json:"body"`
	SubjectProjectID  string     `json:"subjectProjectId,omitempty"`
	SubjectArtifactID string     `json:"subjectArtifactId,omitempty"`
	SubjectVersionID  string     `json:"subjectVersionId,omitempty"`
	SubjectFrameID    string     `json:"subjectFrameId,omitempty"`
	SourceFrameID     string     `json:"sourceFrameId,omitempty"`
	Origin            string     `json:"origin"`
	Evidence          string     `json:"evidence"`
	SupersededBy      string     `json:"supersededBy,omitempty"`
	CategoryID        string     `json:"categoryId,omitempty"`
	CategoryName      string     `json:"categoryName,omitempty"`
	CategoryGuidance  string     `json:"categoryGuidance,omitempty"`
	LastSurfacedAt    *time.Time `json:"lastSurfacedAt,omitempty"`
	RecallScore       float64    `json:"recallScore,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type CreateMemoryInput struct {
	ID                string
	UserID            string
	Body              string
	Origin            string
	Evidence          string
	SubjectProjectID  string
	SubjectArtifactID string
	SubjectVersionID  string
	SubjectFrameID    string
	SourceFrameID     string
	CategoryID        string
}

type Routine struct {
	ID              string     `json:"id"`
	RootFrameID     string     `json:"rootFrameId"`
	OwnerUserID     string     `json:"ownerUserId"`
	Label           string     `json:"label,omitempty"`
	OnTick          string     `json:"onTick"`
	EveryMinutes    int        `json:"everyMinutes"`
	Enabled         bool       `json:"enabled"`
	LockedAt        *time.Time `json:"lockedAt,omitempty"`
	ClaimGeneration int64      `json:"-"`
	ClaimToken      string     `json:"-"`
	PausedReason    string     `json:"pausedReason,omitempty"`
	NextDue         time.Time  `json:"nextDue"`
	TickCount       int        `json:"tickCount"`
	MissedTicks     int        `json:"missedTicks"`
	LastFireAt      *time.Time `json:"lastFireAt,omitempty"`
	LastOKAt        *time.Time `json:"lastOkAt,omitempty"`
	IdleStreak      int        `json:"idleStreak"`
	LastResults     string     `json:"lastResults,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type CreateRoutineInput struct {
	ID           string
	RootFrameID  string
	OwnerUserID  string
	Label        string
	OnTick       string
	EveryMinutes int
	Enabled      bool
	NextDue      time.Time
}

func (s *Store) CreateMemory(input CreateMemoryInput) (Memory, error) {
	if s == nil || s.db == nil {
		return Memory{}, errors.New("workspace store is closed")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	if input.UserID == "" {
		return Memory{}, errors.New("memory user id is required")
	}
	var memory Memory
	err := s.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		var err error
		memory, err = s.createMemoryTx(context.Background(), tx, input, input.UserID)
		return err
	})
	return memory, err
}

func (s *Store) SupersedeMemory(memoryID, replacementID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if memoryID == "" || replacementID == "" || memoryID == replacementID {
		return errors.New("distinct memory and replacement ids are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory supersession: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldUserID, newUserID string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memories WHERE id = ?`, memoryID).Scan(&oldUserID); err != nil {
		return fmt.Errorf("look up superseded memory: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memories WHERE id = ?`, replacementID).Scan(&newUserID); err != nil {
		return fmt.Errorf("look up replacement memory: %w", err)
	}
	if oldUserID != newUserID {
		return errors.New("memory replacement must belong to the same user")
	}
	result, err := tx.ExecContext(ctx, `UPDATE memories SET superseded_by = ?, updated_at = ? WHERE id = ?`, replacementID, s.now().UTC(), memoryID)
	if err != nil {
		return fmt.Errorf("supersede memory: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read memory supersession result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("supersede memory affected %d rows", changed)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory supersession: %w", err)
	}
	return nil
}

func (s *Store) ListActiveMemories(userID, projectID string) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("memory user id is required")
	}
	query := memorySelect + ` WHERE m.user_id = ? AND m.superseded_by IS NULL`
	args := []any{userID}
	if projectID != "" {
		query += ` AND m.subject_project_id = ?`
		args = append(args, projectID)
	}
	query += ` ORDER BY m.updated_at DESC, m.id DESC`
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("query active memories: %w", err)
	}
	defer rows.Close()
	memories := make([]Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan active memory: %w", err)
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func (s *Store) CreateRoutine(input CreateRoutineInput) (Routine, error) {
	if s == nil || s.db == nil {
		return Routine{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{"routine id": input.ID, "root frame id": input.RootFrameID, "owner user id": input.OwnerUserID, "on-tick instruction": input.OnTick} {
		if strings.TrimSpace(value) == "" {
			return Routine{}, fmt.Errorf("%s is required", field)
		}
	}
	if input.EveryMinutes < 1 {
		return Routine{}, errors.New("routine interval must be at least one minute")
	}
	var rootFrameID string
	if err := s.db.QueryRowContext(context.Background(), `SELECT root_frame_id FROM frames WHERE id = ?`, input.RootFrameID).Scan(&rootFrameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Routine{}, fmt.Errorf("routine root frame %q does not exist", input.RootFrameID)
		}
		return Routine{}, fmt.Errorf("look up routine root frame: %w", err)
	}
	if rootFrameID != input.RootFrameID {
		return Routine{}, fmt.Errorf("routine frame %q is not a root frame", input.RootFrameID)
	}
	now := s.now().UTC()
	nextDue := input.NextDue.UTC()
	if nextDue.IsZero() {
		nextDue = now.Add(time.Duration(input.EveryMinutes) * time.Minute)
	}
	routine := Routine{ID: input.ID, RootFrameID: input.RootFrameID, OwnerUserID: input.OwnerUserID, Label: input.Label, OnTick: input.OnTick, EveryMinutes: input.EveryMinutes, Enabled: input.Enabled, NextDue: nextDue, CreatedAt: now, UpdatedAt: now}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO routine_schedules (id, root_frame_id, owner_user_id, label, on_tick, every_minutes, enabled, next_due, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, routine.ID, routine.RootFrameID, routine.OwnerUserID, routine.Label, routine.OnTick, routine.EveryMinutes, routine.Enabled, routine.NextDue, routine.CreatedAt, routine.UpdatedAt); err != nil {
		return Routine{}, fmt.Errorf("insert routine: %w", err)
	}
	return routine, nil
}

// ClaimNextDueRoutine acquires one due routine. An expired lock is reclaimable,
// which prevents a crashed scheduler from permanently stopping a workflow.
func (s *Store) ClaimNextDueRoutine(now time.Time, lockTTL time.Duration) (Routine, bool, error) {
	if s == nil || s.db == nil {
		return Routine{}, false, errors.New("workspace store is closed")
	}
	if lockTTL <= 0 {
		return Routine{}, false, errors.New("routine lock ttl must be positive")
	}
	var routine Routine
	var claimed bool
	err := s.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		var err error
		routine, claimed, err = s.claimNextDueRoutineFencedTx(context.Background(), tx, now, lockTTL, "", uuid.NewString())
		return err
	})
	return routine, claimed, err
}

func (s *Store) CompleteRoutineTick(id string, at time.Time, successful bool, result string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if id == "" {
		return errors.New("routine id is required")
	}
	return s.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		claim, err := scanRoutine(tx.QueryRowContext(context.Background(), routineSelect+` WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("look up routine completion: %w", err)
		}
		_, _, err = s.completeRoutineTickFencedTx(context.Background(), tx, claim, at, successful, result)
		return err
	})
}

func (s *Store) GetRoutine(id string) (Routine, error) {
	if s == nil || s.db == nil {
		return Routine{}, errors.New("workspace store is closed")
	}
	routine, err := scanRoutine(s.db.QueryRowContext(context.Background(), routineSelect+` WHERE id = ?`, id))
	if err != nil {
		return Routine{}, fmt.Errorf("get routine: %w", err)
	}
	return routine, nil
}

const routineSelect = `SELECT id, root_frame_id, owner_user_id, COALESCE(label, ''), on_tick, every_minutes, enabled, locked_at, claim_generation, COALESCE(claim_token, ''), COALESCE(paused_reason, ''), next_due, tick_count, missed_ticks, last_fire_at, last_ok_at, idle_streak, COALESCE(last_results, ''), created_at, updated_at FROM routine_schedules`

type rowScanner interface{ Scan(...any) error }

func scanRoutine(row rowScanner) (Routine, error) {
	var routine Routine
	if err := row.Scan(&routine.ID, &routine.RootFrameID, &routine.OwnerUserID, &routine.Label, &routine.OnTick, &routine.EveryMinutes, &routine.Enabled, &routine.LockedAt, &routine.ClaimGeneration, &routine.ClaimToken, &routine.PausedReason, &routine.NextDue, &routine.TickCount, &routine.MissedTicks, &routine.LastFireAt, &routine.LastOKAt, &routine.IdleStreak, &routine.LastResults, &routine.CreatedAt, &routine.UpdatedAt); err != nil {
		return Routine{}, err
	}
	return routine, nil
}
