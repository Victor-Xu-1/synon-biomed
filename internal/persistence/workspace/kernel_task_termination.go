package workspace

import (
	"context"
	"database/sql"
	"errors"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

// ReconcileKernelTaskTermination joins the task's canonical lifetime to the
// existing executor cancellation protocol. It never signals a PID or changes
// task status. A restarted executor can replay this after a controller crash.
func (s *Store) ReconcileKernelTaskTermination(ctx context.Context, backendID string, generation int64, executorID string) (bool, error) {
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(backendID) || generation <= 0 || !validDetachedIdentity(executorID) {
		return false, errors.New("kernel task reconciliation authority is required")
	}
	readDB := s.readDB
	if readDB == nil {
		readDB = s.db
	}
	backend, found, err := getKernelExecutionBackendQuery(ctx, readDB, backendID)
	if err != nil {
		return false, err
	}
	if !found || backend.BackendGeneration != generation || backend.ExecutorInstanceID != executorID {
		return false, ErrKernelExecutionBackendStale
	}
	ended, err := detachedKernelTaskEnded(ctx, readDB, backend)
	if err != nil || !ended {
		return false, err
	}
	repo, err := s.TranscriptRepository(ctx)
	if err != nil {
		return false, err
	}
	retired := false
	err = repo.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, err := getKernelExecutionBackendQuery(ctx, tx, backendID)
		if err != nil {
			return err
		}
		if !found || current.BackendGeneration != generation || current.ExecutorInstanceID != executorID {
			return ErrKernelExecutionBackendStale
		}
		ended, err := detachedKernelTaskEnded(ctx, tx, current)
		if err != nil || !ended {
			return err
		}
		if current.State != KernelExecutionBackendStateReady && current.State != KernelExecutionBackendStateDraining {
			return ErrKernelExecutionBackendStale
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `UPDATE kernel_detached_executions SET state='cancel_requested',state_version=state_version+1,
    cancel_request_id='task-terminal:'||execution_id,cancel_requested_at=?,reason_code='task_terminal',updated_at=?
    WHERE backend_id=? AND backend_generation=? AND state IN ('accepted','dispatch_committed','started')
      AND COALESCE(cancel_request_id,'')=''`, now, now, backendID, generation); err != nil {
			return err
		}
		if current.State != KernelExecutionBackendStateDraining {
			if _, err = tx.ExecContext(ctx, `UPDATE kernel_execution_backends SET state='draining',state_version=state_version+1,updated_at=?
     WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND state_version=?`, now, backendID, generation, executorID, current.StateVersion); err != nil {
				return err
			}
		}
		retired = true
		return nil
	})
	return retired, err
}

func detachedKernelTaskEnded(ctx context.Context, query detachedKernelExecutionQuery, backend KernelExecutionBackend) (bool, error) {
	var frameIncarnation, rootIncarnation, frameStatus, rootStatus string
	err := query.QueryRowContext(ctx, `SELECT frame.incarnation_id,root.incarnation_id,frame.status,root.status
   FROM frames frame JOIN frames root ON root.id=frame.root_frame_id AND root.project_id=frame.project_id
   WHERE frame.id=? AND frame.project_id=? AND root.id=?`, backend.FrameID, backend.ProjectID, backend.RootFrameID).
		Scan(&frameIncarnation, &rootIncarnation, &frameStatus, &rootStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if frameIncarnation != backend.FrameIncarnationID || rootIncarnation != backend.RootFrameIncarnationID {
		return true, nil
	}
	terminal := func(status string) bool {
		return status == "completed" || status == "failed" || status == "cancelled" || status == "canceled"
	}
	return terminal(frameStatus) || terminal(rootStatus), nil
}
