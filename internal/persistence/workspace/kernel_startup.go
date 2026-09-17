package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type KernelStartupFailure struct {
	BackendID          string
	BackendGeneration  int64
	ExecutorInstanceID string
	Stage              string
	CreatedAt          time.Time
}

func validKernelStartupStage(stage string) bool {
	switch stage {
	case "launch", "runtime_discovery", "session_spec", "resource_domain", "worker_start", "worker_identity", "control_socket", "activation", "process_exit":
		return true
	default:
		return false
	}
}

// ClaimKernelExecutorStartup publishes physical ownership before runtime
// discovery or worker initialization. Replays from the same process are safe;
// another process cannot initialize a second worker for the same generation.
func (s *Store) ClaimKernelExecutorStartup(ctx context.Context, input ActivateKernelExecutionBackendInput) error {
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		!validDetachedIdentity(input.ExecutorInstanceID) || input.BackendGeneration <= 0 || input.ExecutorPID <= 0 || input.ExecutorPIDStartTicks <= 0 {
		return errors.New("complete kernel startup process authority is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_execution_backends SET executor_pid=?,executor_pid_start_ticks=?,state_version=state_version+1,updated_at=?
		WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND state='starting'
		AND executor_pid IS NULL`,
		input.ExecutorPID, input.ExecutorPIDStartTicks, s.now().UTC().Format(time.RFC3339Nano), input.BackendID,
		input.BackendGeneration, input.ExecutorInstanceID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		current, found, readErr := s.GetKernelExecutionBackend(ctx, input.BackendID)
		if readErr != nil {
			return readErr
		}
		if !found || current.BackendGeneration != input.BackendGeneration || current.ExecutorInstanceID != input.ExecutorInstanceID || current.State != KernelExecutionBackendStateStarting || current.ExecutorPID != input.ExecutorPID || current.ExecutorPIDStartTicks != input.ExecutorPIDStartTicks {
			return ErrKernelExecutionBackendStale
		}
	}
	return nil
}

// FailKernelExecutorStartup commits both the receipt and terminal head in one
// transaction. Only callers that observed failure of this generation may use
// this method; a controller wait timeout alone is not such evidence. No raw
// arguments, environment variables, or process output enter the receipt.
func (s *Store) FailKernelExecutorStartup(ctx context.Context, failure KernelStartupFailure) error {
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(failure.BackendID) ||
		!validDetachedIdentity(failure.ExecutorInstanceID) || failure.BackendGeneration <= 0 || !validKernelStartupStage(failure.Stage) {
		return errors.New("complete kernel startup failure authority is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return err
	}
	return repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var generation int64
		var instance, state string
		if err := tx.QueryRowContext(ctx, `SELECT backend_generation,executor_instance_id,state FROM kernel_execution_backends WHERE backend_id=?`, failure.BackendID).Scan(&generation, &instance, &state); err != nil {
			return err
		}
		if generation != failure.BackendGeneration || instance != failure.ExecutorInstanceID {
			return ErrKernelExecutionBackendStale
		}
		if state == KernelExecutionBackendStateStopped {
			var stage string
			if err := tx.QueryRowContext(ctx, `SELECT stage FROM kernel_execution_startup_receipts WHERE backend_id=? AND backend_generation=? AND executor_instance_id=?`, failure.BackendID, generation, instance).Scan(&stage); err != nil {
				return ErrKernelExecutionBackendStale
			}
			if stage != failure.Stage {
				return ErrKernelExecutionBackendConflict
			}
			return nil
		}
		if state != KernelExecutionBackendStateStarting {
			return ErrKernelExecutionBackendStale
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kernel_execution_startup_receipts(backend_id,backend_generation,executor_instance_id,stage,created_at) VALUES(?,?,?,?,?)`, failure.BackendID, generation, instance, failure.Stage, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE kernel_execution_backends SET state='stopped',state_version=state_version+1,
			controller_token_sha256=NULL,controller_lease_expires_at=NULL,updated_at=? WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND state='starting'`, now, failure.BackendID, generation, instance)
		return err
	})
}

func (s *Store) GetKernelStartupFailure(ctx context.Context, backendID string, generation int64) (KernelStartupFailure, bool, error) {
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(backendID) || generation <= 0 {
		return KernelStartupFailure{}, false, errors.New("kernel startup receipt identity is required")
	}
	var receipt KernelStartupFailure
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT backend_id,backend_generation,executor_instance_id,stage,created_at FROM kernel_execution_startup_receipts WHERE backend_id=? AND backend_generation=?`, backendID, generation).Scan(&receipt.BackendID, &receipt.BackendGeneration, &receipt.ExecutorInstanceID, &receipt.Stage, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelStartupFailure{}, false, nil
	}
	if err != nil {
		return KernelStartupFailure{}, false, err
	}
	receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return receipt, err == nil, err
}

// ListStartingKernelBackends pages the existing authority by immutable identity.
// Callers retain a cursor so live initializations cannot starve exited records.
func (s *Store) ListStartingKernelBackends(ctx context.Context, after string, limit int) ([]KernelExecutionBackend, error) {
	if s == nil || s.db == nil || ctx == nil || limit < 1 || limit > 128 {
		return nil, errors.New("invalid startup recovery page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT backend_id FROM kernel_execution_backends WHERE state='starting' AND backend_id>? ORDER BY backend_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	result := make([]KernelExecutionBackend, 0, len(ids))
	for _, id := range ids {
		backend, found, err := s.GetKernelExecutionBackend(ctx, id)
		if err != nil {
			return nil, err
		}
		if found && backend.State == KernelExecutionBackendStateStarting {
			result = append(result, backend)
		}
	}
	return result, nil
}
