package workspace

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelExecutionResultCompleted = "completed"
	KernelExecutionResultFailed    = "failed"
	KernelExecutionResultCancelled = "cancelled"
)

type HeartbeatKernelExecutionBackendInput struct {
	BackendID                 string
	BackendGeneration         int64
	ExecutorInstanceID        string
	ExpectedHeartbeatSequence int64
}

type RenewKernelExecutionBackendControlInput struct {
	BackendID         string
	BackendGeneration int64
	ControllerEpoch   int64
	ControllerToken   string
	LeaseExpiresAt    time.Time
}

type KernelDetachedExecutionControlInput struct {
	ExecutionID       string
	BackendGeneration int64
	ControllerEpoch   int64
	ControllerToken   string
	ExpectedVersion   int64
}

type CommitKernelExecutionDispatchInput struct {
	KernelDetachedExecutionControlInput
	DispatchSequence int64
}

type MarkKernelExecutionRequestWrittenInput struct {
	KernelDetachedExecutionControlInput
	DispatchSequence int64
}

type MarkKernelExecutionStartedInput struct {
	ExecutionID         string
	BackendGeneration   int64
	ExecutorInstanceID  string
	ExpectedVersion     int64
	ObservationSequence int64
}

type RequestKernelExecutionCancelInput struct {
	KernelDetachedExecutionControlInput
	CancelRequestID string
	Reason          string
}

type AcknowledgeKernelExecutionCancelInput struct {
	ExecutionID        string
	BackendGeneration  int64
	ExecutorInstanceID string
	ExpectedVersion    int64
	CancelRequestID    string
	AckSequence        int64
	Signal             string
}

type KernelExecutionResultReceipt struct {
	ReceiptID          string
	ExecutionID        string
	OperationID        string
	BackendID          string
	BackendGeneration  int64
	TerminalSequence   int64
	Outcome            string
	ResultJSON         string
	ResultRef          string
	ResultSHA256       string
	ExecutionLogSHA256 string
	ExitCode           *int64
	TerminationSignal  string
	Interrupted        bool
	TimedOut           bool
	StartedAt          time.Time
	FinishedAt         time.Time
	FilesWrittenJSON   string
	DroppedRootsJSON   string
	CreatedAt          time.Time
}

type CommitKernelExecutionResultInput struct {
	ReceiptID          string
	ExecutionID        string
	BackendGeneration  int64
	ExecutorInstanceID string
	TerminalSequence   int64
	Outcome            string
	ResultJSON         string
	ResultRef          string
	ResultSHA256       string
	ExecutionLogSHA256 string
	ExitCode           *int64
	TerminationSignal  string
	Interrupted        bool
	TimedOut           bool
	StartedAt          time.Time
	FinishedAt         time.Time
	FilesWrittenJSON   string
	DroppedRootsJSON   string
}

// MarkKernelExecutionEvidenceLostInput is used only after the durable backend
// has stopped and no terminal receipt can be produced. It fences the detached
// execution so recovery cannot retry an execution whose physical evidence is
// gone.
type MarkKernelExecutionEvidenceLostInput struct {
	ExecutionID       string
	BackendGeneration int64
	ReasonCode        string
}

func (s *Store) MarkKernelExecutionEvidenceLost(
	ctx context.Context,
	input MarkKernelExecutionEvidenceLostInput,
) (DetachedKernelExecution, error) {
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || input.ReasonCode == "" {
		return DetachedKernelExecution{}, errors.New("detached execution evidence-loss authority is required")
	}
	if _, err := normalizeKernelLocalOperationReason(input.ReasonCode); err != nil {
		return DetachedKernelExecution{}, err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	var execution DetachedKernelExecution
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || current.BackendGeneration != input.BackendGeneration {
			return ErrDetachedKernelExecutionConflict
		}
		if current.State == DetachedKernelExecutionStateEvidenceLost {
			execution = current
			return nil
		}
		if current.State == DetachedKernelExecutionStateTerminal || current.TerminalReceiptID != "" ||
			(current.State != DetachedKernelExecutionStateAccepted &&
				current.State != DetachedKernelExecutionStateDispatchCommitted &&
				current.State != DetachedKernelExecutionStateStarted &&
				current.State != DetachedKernelExecutionStateCancelRequested) {
			return ErrDetachedKernelExecutionConflict
		}
		backend, backendFound, queryErr := getKernelExecutionBackendQuery(ctx, tx, current.BackendID)
		if queryErr != nil {
			return queryErr
		}
		if !backendFound || backend.BackendGeneration != current.BackendGeneration ||
			(backend.State != KernelExecutionBackendStateStopped &&
				backend.State != KernelExecutionBackendStateEvidenceLost) {
			return ErrKernelExecutionBackendStale
		}
		updated, updateErr := updateDetachedKernelExecutionTx(ctx, tx, current,
			`state=?,state_version=state_version+1,reason_code=?,updated_at=?`,
			DetachedKernelExecutionStateEvidenceLost, input.ReasonCode,
			s.now().UTC().Format(time.RFC3339Nano))
		if updateErr != nil {
			return updateErr
		}
		execution = updated
		return nil
	})
	return execution, err
}

func (s *Store) HeartbeatKernelExecutionBackend(
	ctx context.Context,
	input HeartbeatKernelExecutionBackendInput,
) (KernelExecutionBackend, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) ||
		input.ExpectedHeartbeatSequence <= 0 {
		return KernelExecutionBackend{}, errors.New("complete kernel execution backend heartbeat authority is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	now := s.now().UTC()
	var backend KernelExecutionBackend
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
		if queryErr != nil {
			return queryErr
		}
		if !found || current.BackendGeneration != input.BackendGeneration ||
			current.ExecutorInstanceID != input.ExecutorInstanceID ||
			(current.State != KernelExecutionBackendStateReady && current.State != KernelExecutionBackendStateDraining) {
			return ErrKernelExecutionBackendStale
		}
		if current.HeartbeatSequence == input.ExpectedHeartbeatSequence {
			backend = current
			return nil
		}
		if current.HeartbeatSequence != input.ExpectedHeartbeatSequence-1 {
			return ErrKernelExecutionBackendStale
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE kernel_execution_backends SET
			heartbeat_sequence=?,heartbeat_at=?,state_version=state_version+1,updated_at=?
			WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND
			heartbeat_sequence=? AND state_version=? AND state IN ('ready','draining')`,
			input.ExpectedHeartbeatSequence, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			input.BackendID, input.BackendGeneration, input.ExecutorInstanceID,
			input.ExpectedHeartbeatSequence-1, current.StateVersion)
		if updateErr != nil {
			return updateErr
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrKernelExecutionBackendStale
		}
		backend, _, updateErr = getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
		return updateErr
	})
	return backend, err
}

func (s *Store) RenewKernelExecutionBackendControl(
	ctx context.Context,
	input RenewKernelExecutionBackendControlInput,
) (KernelExecutionBackend, KernelExecutionControlLease, error) {
	normalizeKernelExecutionControl(&input.BackendID, &input.ControllerToken)
	input.LeaseExpiresAt = input.LeaseExpiresAt.UTC()
	now := s.now().UTC()
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		input.BackendGeneration <= 0 || input.ControllerEpoch <= 0 || !validKernelExecutionControlToken(input.ControllerToken) ||
		!input.LeaseExpiresAt.After(now) {
		return KernelExecutionBackend{}, KernelExecutionControlLease{},
			errors.New("complete kernel execution backend control renewal is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionBackend{}, KernelExecutionControlLease{}, err
	}
	var backend KernelExecutionBackend
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
		if queryErr != nil {
			return queryErr
		}
		if !found || !kernelExecutionControllerMatches(current, input.BackendGeneration,
			input.ControllerEpoch, input.ControllerToken, now) {
			return ErrKernelExecutionBackendStale
		}
		if current.ControllerLeaseExpiresAt != nil && current.ControllerLeaseExpiresAt.Equal(input.LeaseExpiresAt) {
			backend = current
			return nil
		}
		if current.ControllerLeaseExpiresAt == nil || !input.LeaseExpiresAt.After(*current.ControllerLeaseExpiresAt) {
			return ErrKernelExecutionBackendStale
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE kernel_execution_backends SET
			controller_lease_expires_at=?,state_version=state_version+1,updated_at=?
			WHERE backend_id=? AND backend_generation=? AND controller_epoch=? AND state_version=?
			AND state IN ('ready','draining')`, input.LeaseExpiresAt.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), input.BackendID, input.BackendGeneration,
			input.ControllerEpoch, current.StateVersion)
		if updateErr != nil {
			return updateErr
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrKernelExecutionBackendStale
		}
		backend, _, updateErr = getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
		return updateErr
	})
	if err != nil {
		return KernelExecutionBackend{}, KernelExecutionControlLease{}, err
	}
	return backend, KernelExecutionControlLease{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Epoch: backend.ControllerEpoch, Token: input.ControllerToken, ExpiresAt: input.LeaseExpiresAt,
	}, nil
}

func (s *Store) CommitKernelExecutionDispatch(
	ctx context.Context,
	input CommitKernelExecutionDispatchInput,
) (DetachedKernelExecution, error) {
	if input.DispatchSequence <= 0 {
		return DetachedKernelExecution{}, errors.New("positive kernel execution dispatch sequence is required")
	}
	return s.transitionDetachedKernelExecutionWithControl(ctx, input.KernelDetachedExecutionControlInput,
		func(tx *transcriptstore.ImmediateTransaction, current DetachedKernelExecution, now time.Time) (DetachedKernelExecution, error) {
			if current.DispatchCommittedAt != nil {
				if current.DispatchSequence == input.DispatchSequence {
					return current, nil
				}
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.State != DetachedKernelExecutionStateAccepted || current.StateVersion != input.ExpectedVersion {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			return updateDetachedKernelExecutionTx(ctx, tx, current, `state=?,state_version=state_version+1,
				dispatch_sequence=?,dispatch_committed_at=?,updated_at=?`,
				DetachedKernelExecutionStateDispatchCommitted, input.DispatchSequence,
				now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		})
}

func (s *Store) MarkKernelExecutionRequestWritten(
	ctx context.Context,
	input MarkKernelExecutionRequestWrittenInput,
) (DetachedKernelExecution, error) {
	if input.DispatchSequence <= 0 {
		return DetachedKernelExecution{}, errors.New("positive kernel execution dispatch sequence is required")
	}
	return s.transitionDetachedKernelExecutionWithControl(ctx, input.KernelDetachedExecutionControlInput,
		func(tx *transcriptstore.ImmediateTransaction, current DetachedKernelExecution, now time.Time) (DetachedKernelExecution, error) {
			if current.DispatchSequence != input.DispatchSequence {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.RequestWrittenAt != nil {
				return current, nil
			}
			if current.State != DetachedKernelExecutionStateDispatchCommitted || current.StateVersion != input.ExpectedVersion {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			return updateDetachedKernelExecutionTx(ctx, tx, current, `state=?,state_version=state_version+1,
				request_written_at=?,updated_at=?`, DetachedKernelExecutionStateDispatchCommitted,
				now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		})
}

func (s *Store) MarkKernelExecutionStarted(
	ctx context.Context,
	input MarkKernelExecutionStartedInput,
) (DetachedKernelExecution, error) {
	normalizeKernelExecutionExecutorInput(&input.ExecutionID, &input.ExecutorInstanceID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) ||
		input.ExpectedVersion <= 0 || input.ObservationSequence <= 0 {
		return DetachedKernelExecution{}, errors.New("complete kernel execution start acknowledgement is required")
	}
	return s.transitionDetachedKernelExecutionWithExecutor(ctx, input.ExecutionID, input.BackendGeneration,
		input.ExecutorInstanceID, func(tx *transcriptstore.ImmediateTransaction, current DetachedKernelExecution,
			now time.Time) (DetachedKernelExecution, error) {
			if current.WorkerStartedAt != nil {
				if current.LastObservationSequence >= input.ObservationSequence {
					return current, nil
				}
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.State != DetachedKernelExecutionStateDispatchCommitted || current.RequestWrittenAt == nil ||
				current.StateVersion != input.ExpectedVersion || current.LastObservationSequence >= input.ObservationSequence {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			return updateDetachedKernelExecutionTx(ctx, tx, current, `state=?,state_version=state_version+1,
				worker_started_at=?,last_observation_sequence=?,updated_at=?`,
				DetachedKernelExecutionStateStarted, now.Format(time.RFC3339Nano), input.ObservationSequence,
				now.Format(time.RFC3339Nano))
		})
}

func (s *Store) RequestKernelExecutionCancel(
	ctx context.Context,
	input RequestKernelExecutionCancelInput,
) (DetachedKernelExecution, error) {
	input.CancelRequestID = strings.TrimSpace(input.CancelRequestID)
	input.Reason = strings.TrimSpace(input.Reason)
	if !validDetachedIdentity(input.CancelRequestID) || utf8.RuneCountInString(input.Reason) > 500 ||
		strings.ContainsRune(input.Reason, '\x00') {
		return DetachedKernelExecution{}, errors.New("kernel execution cancel request id or reason is invalid")
	}
	return s.transitionDetachedKernelExecutionWithControl(ctx, input.KernelDetachedExecutionControlInput,
		func(tx *transcriptstore.ImmediateTransaction, current DetachedKernelExecution, now time.Time) (DetachedKernelExecution, error) {
			if current.CancelRequestID != "" {
				if current.CancelRequestID == input.CancelRequestID && current.ReasonCode == input.Reason {
					return current, nil
				}
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.StateVersion != input.ExpectedVersion || (current.State != DetachedKernelExecutionStateAccepted &&
				current.State != DetachedKernelExecutionStateDispatchCommitted && current.State != DetachedKernelExecutionStateStarted) {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			return updateDetachedKernelExecutionTx(ctx, tx, current, `state=?,state_version=state_version+1,
				cancel_request_id=?,cancel_requested_at=?,reason_code=?,updated_at=?`, DetachedKernelExecutionStateCancelRequested,
				input.CancelRequestID, now.Format(time.RFC3339Nano), input.Reason, now.Format(time.RFC3339Nano))
		})
}

// requestDetachedKernelFrameCancellationTx records canonical frame
// cancellation for every executor-owned cell in the same SQLite transaction
// as the Frame and Transcript terminal facts. The executor consumes this
// durable intent; no Web-process signal is required for correctness.
func requestDetachedKernelFrameCancellationTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	frameID string,
	now time.Time,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT execution_id FROM kernel_detached_executions
		WHERE state IN ('accepted','dispatch_committed','started') AND operation_id IN (
			SELECT operation_id FROM kernel_local_operations WHERE frame_id=? AND state='started'
		) ORDER BY execution_id`, frameID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		current, found, err := getDetachedKernelExecutionQuery(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found || current.CancelRequestID != "" ||
			(current.State != DetachedKernelExecutionStateAccepted &&
				current.State != DetachedKernelExecutionStateDispatchCommitted &&
				current.State != DetachedKernelExecutionStateStarted) {
			return ErrDetachedKernelExecutionConflict
		}
		cancelRequestID := "frame-cancel:" + current.ExecutionID
		if _, err := updateDetachedKernelExecutionTx(ctx, tx, current,
			`state=?,state_version=state_version+1,cancel_request_id=?,cancel_requested_at=?,updated_at=?`,
			DetachedKernelExecutionStateCancelRequested, cancelRequestID,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AcknowledgeKernelExecutionCancel(
	ctx context.Context,
	input AcknowledgeKernelExecutionCancelInput,
) (DetachedKernelExecution, error) {
	normalizeKernelExecutionExecutorInput(&input.ExecutionID, &input.ExecutorInstanceID)
	input.CancelRequestID = strings.TrimSpace(input.CancelRequestID)
	input.Signal = strings.TrimSpace(input.Signal)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) ||
		input.ExpectedVersion <= 0 || !validDetachedIdentity(input.CancelRequestID) || input.AckSequence <= 0 ||
		(input.Signal != "dequeue" && input.Signal != "sigint" && input.Signal != "sigterm" && input.Signal != "sigkill") {
		return DetachedKernelExecution{}, errors.New("complete kernel execution cancel acknowledgement is required")
	}
	return s.transitionDetachedKernelExecutionWithExecutor(ctx, input.ExecutionID, input.BackendGeneration,
		input.ExecutorInstanceID, func(tx *transcriptstore.ImmediateTransaction, current DetachedKernelExecution,
			now time.Time) (DetachedKernelExecution, error) {
			if current.CancelRequestID != input.CancelRequestID {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.CancelAckAt != nil {
				if current.CancelAckSequence == input.AckSequence && current.CancelSignal == input.Signal {
					return current, nil
				}
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			if current.State != DetachedKernelExecutionStateCancelRequested || current.StateVersion != input.ExpectedVersion ||
				current.LastObservationSequence >= input.AckSequence {
				return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
			}
			return updateDetachedKernelExecutionTx(ctx, tx, current, `state=?,state_version=state_version+1,
				cancel_ack_sequence=?,cancel_ack_at=?,cancel_signal=?,last_observation_sequence=?,updated_at=?`,
				DetachedKernelExecutionStateCancelRequested, input.AckSequence, now.Format(time.RFC3339Nano),
				input.Signal, input.AckSequence, now.Format(time.RFC3339Nano))
		})
}

func (s *Store) CommitKernelExecutionResult(
	ctx context.Context,
	input CommitKernelExecutionResultInput,
) (DetachedKernelExecution, KernelExecutionResultReceipt, error) {
	if s == nil || s.db == nil || ctx == nil {
		return DetachedKernelExecution{}, KernelExecutionResultReceipt{},
			errors.New("kernel execution result store is unavailable")
	}
	normalizeCommitKernelExecutionResultInput(&input)
	if err := validateCommitKernelExecutionResultInput(input); err != nil {
		return DetachedKernelExecution{}, KernelExecutionResultReceipt{}, err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return DetachedKernelExecution{}, KernelExecutionResultReceipt{}, err
	}
	now := s.now().UTC()
	var execution DetachedKernelExecution
	var receipt KernelExecutionResultReceipt
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || current.BackendGeneration != input.BackendGeneration {
			return ErrDetachedKernelExecutionConflict
		}
		if current.TerminalReceiptID != "" {
			existing, receiptFound, receiptErr := getKernelExecutionResultReceiptQuery(ctx, tx, current.TerminalReceiptID)
			if receiptErr != nil {
				return receiptErr
			}
			if !receiptFound || !kernelExecutionResultReceiptMatches(existing, input) {
				return ErrDetachedKernelExecutionConflict
			}
			execution, receipt = current, existing
			return nil
		}
		if current.State != DetachedKernelExecutionStateDispatchCommitted &&
			current.State != DetachedKernelExecutionStateStarted && current.State != DetachedKernelExecutionStateCancelRequested {
			return ErrDetachedKernelExecutionConflict
		}
		if input.TerminalSequence <= current.LastObservationSequence {
			return ErrDetachedKernelExecutionConflict
		}
		if executorErr := validateKernelExecutionExecutorTx(ctx, tx, current, input.BackendGeneration,
			input.ExecutorInstanceID); executorErr != nil {
			return executorErr
		}
		var exitCode any
		if input.ExitCode != nil {
			exitCode = *input.ExitCode
		}
		storedResultRef := input.ResultRef
		spoolResultRef := ""
		if strings.HasPrefix(input.ResultRef, "kernel-spool-sha256:") {
			storedResultRef, spoolResultRef = "", input.ResultRef
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO kernel_execution_result_receipts(
			receipt_id,execution_id,operation_id,backend_id,backend_generation,terminal_sequence,
			outcome,result_json,result_ref,result_sha256,execution_log_sha256,exit_code,termination_signal,
			interrupted,timed_out,started_at,finished_at,files_written_json,dropped_roots_json,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.ReceiptID, current.ExecutionID,
			current.OperationID, current.BackendID, current.BackendGeneration, input.TerminalSequence,
			input.Outcome, input.ResultJSON, nullableDetachedText(storedResultRef), input.ResultSHA256,
			nullableDetachedText(input.ExecutionLogSHA256), exitCode, input.TerminationSignal,
			boolToInteger(input.Interrupted), boolToInteger(input.TimedOut), input.StartedAt.Format(time.RFC3339Nano),
			input.FinishedAt.Format(time.RFC3339Nano), input.FilesWrittenJSON, input.DroppedRootsJSON,
			now.Format(time.RFC3339Nano))
		if insertErr != nil {
			return insertErr
		}
		if spoolResultRef != "" {
			if _, insertErr = tx.ExecContext(ctx, `INSERT INTO kernel_execution_result_spool_refs(
				receipt_id,result_ref,created_at) VALUES(?,?,?)`, input.ReceiptID, spoolResultRef,
				now.Format(time.RFC3339Nano)); insertErr != nil {
				return insertErr
			}
		}
		execution, queryErr = updateDetachedKernelExecutionTx(ctx, tx, current,
			`state=?,state_version=state_version+1,terminal_receipt_id=?,last_observation_sequence=?,updated_at=?`,
			DetachedKernelExecutionStateTerminal, input.ReceiptID, input.TerminalSequence, now.Format(time.RFC3339Nano))
		if queryErr != nil {
			return queryErr
		}
		receipt, _, queryErr = getKernelExecutionResultReceiptQuery(ctx, tx, input.ReceiptID)
		return queryErr
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return execution, receipt, err
}

func (s *Store) GetKernelExecutionResultReceipt(
	ctx context.Context,
	receiptID string,
) (KernelExecutionResultReceipt, bool, error) {
	receiptID = strings.TrimSpace(receiptID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(receiptID) {
		return KernelExecutionResultReceipt{}, false, errors.New("kernel execution result receipt id is required")
	}
	return getKernelExecutionResultReceiptQuery(ctx, s.db, receiptID)
}

func (s *Store) transitionDetachedKernelExecutionWithControl(
	ctx context.Context,
	input KernelDetachedExecutionControlInput,
	transition func(*transcriptstore.ImmediateTransaction, DetachedKernelExecution, time.Time) (DetachedKernelExecution, error),
) (DetachedKernelExecution, error) {
	normalizeKernelExecutionControl(&input.ExecutionID, &input.ControllerToken)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || input.ControllerEpoch <= 0 ||
		!validKernelExecutionControlToken(input.ControllerToken) || input.ExpectedVersion <= 0 {
		return DetachedKernelExecution{}, errors.New("complete detached kernel execution control authority is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	var execution DetachedKernelExecution
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || current.BackendGeneration != input.BackendGeneration {
			return ErrDetachedKernelExecutionConflict
		}
		if controlErr := validateKernelExecutionControllerTx(ctx, tx, current, input.BackendGeneration,
			input.ControllerEpoch, input.ControllerToken, s.now().UTC()); controlErr != nil {
			return controlErr
		}
		execution, queryErr = transition(tx, current, s.now().UTC())
		return queryErr
	})
	return execution, err
}

func (s *Store) transitionDetachedKernelExecutionWithExecutor(
	ctx context.Context,
	executionID string,
	backendGeneration int64,
	executorInstanceID string,
	transition func(*transcriptstore.ImmediateTransaction, DetachedKernelExecution, time.Time) (DetachedKernelExecution, error),
) (DetachedKernelExecution, error) {
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	var execution DetachedKernelExecution
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, executionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || current.BackendGeneration != backendGeneration {
			return ErrDetachedKernelExecutionConflict
		}
		if executorErr := validateKernelExecutionExecutorTx(ctx, tx, current, backendGeneration,
			executorInstanceID); executorErr != nil {
			return executorErr
		}
		execution, queryErr = transition(tx, current, s.now().UTC())
		return queryErr
	})
	return execution, err
}

func updateDetachedKernelExecutionTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	current DetachedKernelExecution,
	setClause string,
	args ...any,
) (DetachedKernelExecution, error) {
	query := `UPDATE kernel_detached_executions SET ` + setClause + ` WHERE execution_id=? AND state_version=?`
	args = append(args, current.ExecutionID, current.StateVersion)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		if rowsErr != nil {
			return DetachedKernelExecution{}, rowsErr
		}
		return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
	}
	execution, found, err := getDetachedKernelExecutionQuery(ctx, tx, current.ExecutionID)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	if !found {
		return DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
	}
	return execution, nil
}

func validateKernelExecutionControllerTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	execution DetachedKernelExecution,
	backendGeneration int64,
	epoch int64,
	token string,
	now time.Time,
) error {
	backend, found, err := getKernelExecutionBackendQuery(ctx, tx, execution.BackendID)
	if err != nil {
		return err
	}
	if !found || !kernelExecutionControllerMatches(backend, backendGeneration, epoch, token, now) {
		return ErrKernelExecutionBackendStale
	}
	return nil
}

func validateKernelExecutionExecutorTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	execution DetachedKernelExecution,
	backendGeneration int64,
	executorInstanceID string,
) error {
	backend, found, err := getKernelExecutionBackendQuery(ctx, tx, execution.BackendID)
	if err != nil {
		return err
	}
	if !found || backend.BackendGeneration != backendGeneration ||
		backend.ExecutorInstanceID != executorInstanceID ||
		(backend.State != KernelExecutionBackendStateReady && backend.State != KernelExecutionBackendStateDraining) {
		return ErrKernelExecutionBackendStale
	}
	return nil
}

func kernelExecutionControllerMatches(
	backend KernelExecutionBackend,
	backendGeneration int64,
	epoch int64,
	token string,
	now time.Time,
) bool {
	digest := sha256.Sum256([]byte(token))
	return backend.BackendGeneration == backendGeneration && backend.ControllerEpoch == epoch &&
		backend.ControllerLeaseExpiresAt != nil && backend.ControllerLeaseExpiresAt.After(now) &&
		subtle.ConstantTimeCompare(backend.ControllerTokenSHA256, digest[:]) == 1 &&
		(backend.State == KernelExecutionBackendStateReady || backend.State == KernelExecutionBackendStateDraining)
}

func getKernelExecutionResultReceiptQuery(
	ctx context.Context,
	query detachedKernelExecutionQuery,
	receiptID string,
) (KernelExecutionResultReceipt, bool, error) {
	var receipt KernelExecutionResultReceipt
	var resultRef, logSHA, exitCode sql.NullString
	var interrupted, timedOut int
	var startedAt, finishedAt, createdAt string
	err := query.QueryRowContext(ctx, `SELECT receipt.receipt_id,receipt.execution_id,receipt.operation_id,receipt.backend_id,
		receipt.backend_generation,receipt.terminal_sequence,receipt.outcome,receipt.result_json,
		COALESCE(receipt.result_ref,spool.result_ref),receipt.result_sha256,
		receipt.execution_log_sha256,CAST(receipt.exit_code AS TEXT),receipt.termination_signal,
		receipt.interrupted,receipt.timed_out,receipt.started_at,receipt.finished_at,
		receipt.files_written_json,receipt.dropped_roots_json,receipt.created_at
		FROM kernel_execution_result_receipts receipt
		LEFT JOIN kernel_execution_result_spool_refs spool ON spool.receipt_id=receipt.receipt_id
		WHERE receipt.receipt_id=?`, receiptID).Scan(
		&receipt.ReceiptID, &receipt.ExecutionID, &receipt.OperationID, &receipt.BackendID,
		&receipt.BackendGeneration, &receipt.TerminalSequence, &receipt.Outcome, &receipt.ResultJSON,
		&resultRef, &receipt.ResultSHA256, &logSHA, &exitCode, &receipt.TerminationSignal,
		&interrupted, &timedOut, &startedAt, &finishedAt, &receipt.FilesWrittenJSON,
		&receipt.DroppedRootsJSON, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelExecutionResultReceipt{}, false, nil
	}
	if err != nil {
		return KernelExecutionResultReceipt{}, false, err
	}
	receipt.ResultRef, receipt.ExecutionLogSHA256 = resultRef.String, logSHA.String
	if exitCode.Valid {
		parsed, parseErr := strconv.ParseInt(exitCode.String, 10, 64)
		if parseErr != nil {
			return KernelExecutionResultReceipt{}, false, ErrDetachedKernelExecutionConflict
		}
		receipt.ExitCode = &parsed
	}
	receipt.Interrupted, receipt.TimedOut = interrupted == 1, timedOut == 1
	var parseErr error
	if receipt.StartedAt, parseErr = time.Parse(time.RFC3339Nano, startedAt); parseErr != nil {
		return KernelExecutionResultReceipt{}, false, ErrDetachedKernelExecutionConflict
	}
	if receipt.FinishedAt, parseErr = time.Parse(time.RFC3339Nano, finishedAt); parseErr != nil {
		return KernelExecutionResultReceipt{}, false, ErrDetachedKernelExecutionConflict
	}
	if receipt.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
		return KernelExecutionResultReceipt{}, false, ErrDetachedKernelExecutionConflict
	}
	return receipt, true, nil
}

func normalizeCommitKernelExecutionResultInput(input *CommitKernelExecutionResultInput) {
	input.ReceiptID = strings.TrimSpace(input.ReceiptID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.ResultJSON = strings.TrimSpace(input.ResultJSON)
	input.ResultRef = strings.TrimSpace(input.ResultRef)
	input.ResultSHA256 = strings.TrimSpace(input.ResultSHA256)
	input.ExecutionLogSHA256 = strings.TrimSpace(input.ExecutionLogSHA256)
	input.TerminationSignal = strings.TrimSpace(input.TerminationSignal)
	input.FilesWrittenJSON = strings.TrimSpace(input.FilesWrittenJSON)
	input.DroppedRootsJSON = strings.TrimSpace(input.DroppedRootsJSON)
	input.StartedAt = input.StartedAt.UTC()
	input.FinishedAt = input.FinishedAt.UTC()
}

func validateCommitKernelExecutionResultInput(input CommitKernelExecutionResultInput) error {
	if !validDetachedIdentity(input.ReceiptID) || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) || input.TerminalSequence <= 0 ||
		(input.Outcome != KernelExecutionResultCompleted && input.Outcome != KernelExecutionResultFailed &&
			input.Outcome != KernelExecutionResultCancelled) || !validLowerHexSHA256(input.ResultSHA256) ||
		(input.ExecutionLogSHA256 != "" && !validLowerHexSHA256(input.ExecutionLogSHA256)) ||
		len(input.TerminationSignal) > 64 || strings.ContainsAny(input.TerminationSignal, "\x00\r\n") ||
		input.StartedAt.IsZero() || input.FinishedAt.Before(input.StartedAt) {
		return errors.New("complete kernel execution result receipt is required")
	}
	if input.ResultRef != "" && !validKernelExecutionResultRef(input.ResultRef) {
		return errors.New("kernel execution result reference is invalid")
	}
	if len(input.ResultJSON) == 0 || len(input.ResultJSON) > 1048576 || !json.Valid([]byte(input.ResultJSON)) {
		return errors.New("kernel execution result JSON is invalid")
	}
	var resultObject map[string]any
	if err := json.Unmarshal([]byte(input.ResultJSON), &resultObject); err != nil || resultObject == nil {
		return errors.New("kernel execution result must be a JSON object")
	}
	digest := sha256.Sum256([]byte(input.ResultJSON))
	if hex.EncodeToString(digest[:]) != input.ResultSHA256 {
		return errors.New("kernel execution result digest does not match")
	}
	if !validDetachedJSONArray(input.FilesWrittenJSON, 1048576) ||
		!validDetachedJSONArray(input.DroppedRootsJSON, 262144) {
		return errors.New("kernel execution result file lists are invalid")
	}
	return nil
}

func validKernelExecutionResultRef(value string) bool {
	if strings.HasPrefix(value, "artifact-version:") {
		return len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n")
	}
	const prefix = "kernel-spool-sha256:"
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+64 &&
		validLowerHexSHA256(strings.TrimPrefix(value, prefix))
}

func kernelExecutionResultReceiptMatches(
	receipt KernelExecutionResultReceipt,
	input CommitKernelExecutionResultInput,
) bool {
	return receipt.ReceiptID == input.ReceiptID && receipt.ExecutionID == input.ExecutionID &&
		receipt.BackendGeneration == input.BackendGeneration && receipt.TerminalSequence == input.TerminalSequence &&
		receipt.Outcome == input.Outcome && receipt.ResultJSON == input.ResultJSON && receipt.ResultRef == input.ResultRef &&
		receipt.ResultSHA256 == input.ResultSHA256 && receipt.ExecutionLogSHA256 == input.ExecutionLogSHA256 &&
		nullableInt64Equal(receipt.ExitCode, input.ExitCode) && receipt.TerminationSignal == input.TerminationSignal &&
		receipt.Interrupted == input.Interrupted && receipt.TimedOut == input.TimedOut &&
		receipt.StartedAt.Equal(input.StartedAt) && receipt.FinishedAt.Equal(input.FinishedAt) &&
		receipt.FilesWrittenJSON == input.FilesWrittenJSON && receipt.DroppedRootsJSON == input.DroppedRootsJSON
}

func validDetachedJSONArray(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit || !json.Valid([]byte(value)) {
		return false
	}
	var items []any
	return json.Unmarshal([]byte(value), &items) == nil && items != nil
}

func validKernelExecutionControlToken(token string) bool {
	return len(token) >= 32 && len(token) <= 4096 && token == strings.TrimSpace(token) &&
		!strings.ContainsAny(token, "\x00\r\n")
}

func normalizeKernelExecutionControl(identity *string, token *string) {
	*identity = strings.TrimSpace(*identity)
	*token = strings.TrimSpace(*token)
}

func normalizeKernelExecutionExecutorInput(executionID *string, executorInstanceID *string) {
	*executionID = strings.TrimSpace(*executionID)
	*executorInstanceID = strings.TrimSpace(*executorInstanceID)
}

func nullableDetachedText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64Equal(left *int64, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func boolToInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
