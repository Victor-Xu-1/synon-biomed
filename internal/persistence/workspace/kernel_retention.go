package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// The default keeps one logical task warm while its frame or Transcript
	// attempt is protected, then releases the persistent kernel promptly once
	// the task reaches a durable terminal state. Explicit session policy can
	// still opt into 60..21,600 seconds of cross-task retention.
	DefaultKernelIdleTimeoutSeconds = 5
	maxKernelRetentionRoots         = 2048
)

type KernelRetentionState struct {
	RootFrameID        string
	Exists             bool
	Protected          bool
	IdleTimeoutSeconds int
}

func (s *Store) KernelRetentionWake() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.kernelRetentionWakeMu.Lock()
	defer s.kernelRetentionWakeMu.Unlock()
	if s.kernelRetentionWake == nil {
		s.kernelRetentionWake = make(chan struct{})
	}
	return s.kernelRetentionWake
}

func (s *Store) signalKernelRetentionWake() {
	if s == nil {
		return
	}
	s.kernelRetentionWakeMu.Lock()
	if s.kernelRetentionWake == nil {
		s.kernelRetentionWake = make(chan struct{})
	}
	close(s.kernelRetentionWake)
	s.kernelRetentionWake = make(chan struct{})
	s.kernelRetentionWakeMu.Unlock()
}

// NotifyKernelRunnerStateChanged wakes recovery/reaping supervisors after a
// Transcript runner terminal transition. The runner attempt and kernel
// operation share this database but are committed by separate authorities;
// the notification avoids waiting for the old lease deadline before an
// already-terminal pre-start operation can be reclaimed.
func (s *Store) NotifyKernelRunnerStateChanged() {
	s.signalKernelRetentionWake()
}

// NotifyKernelOperationChanged wakes operation and approval recovery after a
// kernel operation is committed inside a Transcript transaction. The caller
// invokes this only after that transaction succeeds.
func (s *Store) NotifyKernelOperationChanged() {
	s.signalKernelRetentionWake()
}

// ListKernelRetentionStates resolves all durable idle-policy inputs in one
// query. The kernel package deliberately knows nothing about Workspace or
// Transcript state; this Store remains the sole persistence authority.
func (s *Store) ListKernelRetentionStates(ctx context.Context, rootFrameIDs []string) ([]KernelRetentionState, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(rootFrameIDs) == 0 {
		return []KernelRetentionState{}, nil
	}
	if len(rootFrameIDs) > maxKernelRetentionRoots {
		return nil, errors.New("too many kernel retention roots")
	}
	args := make([]any, 0, len(rootFrameIDs)+1)
	values := make([]string, 0, len(rootFrameIDs))
	seen := make(map[string]struct{}, len(rootFrameIDs))
	for _, raw := range rootFrameIDs {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n") {
			return nil, errors.New("kernel retention root id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		values = append(values, fmt.Sprintf("(%d, ?)", len(values)))
		args = append(args, id)
	}
	args = append(args, time.Now().UTC())
	contextJSON := `CASE WHEN json_valid(COALESCE(metadata.context_data, '{}'))
		THEN COALESCE(metadata.context_data, '{}') ELSE '{}' END`
	idleValue := `json_extract(` + contextJSON + `, '$._original_input.kernel_idle_timeout')`
	query := `WITH requested(ordinal, root_frame_id) AS (VALUES ` + strings.Join(values, ",") + `)
		SELECT requested.ordinal, requested.root_frame_id, root.id IS NOT NULL,
			CASE WHEN json_type(` + contextJSON + `, '$._original_input.kernel_idle_timeout') IN ('integer','real')
				AND ` + idleValue + ` BETWEEN 60 AND 21600
				THEN CAST(` + idleValue + ` AS INTEGER) ELSE ? END,
			EXISTS (
				SELECT 1 FROM frames active
				WHERE active.root_frame_id=requested.root_frame_id
					AND active.status IN ('processing','running','awaiting_user_response','awaiting_plan_approval')
			) OR EXISTS (
				SELECT 1 FROM transcript_streams stream
				JOIN transcript_runner_attempts attempt ON attempt.stream_uid=stream.stream_uid
				WHERE stream.root_frame_id=requested.root_frame_id
					AND attempt.status='running' AND attempt.expires_at>?
			)
		FROM requested
		LEFT JOIN frames root ON root.id=requested.root_frame_id AND root.parent_frame_id IS NULL
		LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=root.id
		ORDER BY requested.ordinal`
	// The default belongs before the trailing current-time argument.
	args = append(args[:len(args)-1], DefaultKernelIdleTimeoutSeconds, args[len(args)-1])
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query kernel retention states: %w", err)
	}
	defer rows.Close()
	states := make([]KernelRetentionState, 0, len(values))
	for rows.Next() {
		var ordinal int
		var state KernelRetentionState
		if err := rows.Scan(&ordinal, &state.RootFrameID, &state.Exists, &state.IdleTimeoutSeconds, &state.Protected); err != nil {
			return nil, fmt.Errorf("scan kernel retention state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kernel retention states: %w", err)
	}
	return states, nil
}
