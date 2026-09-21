//go:build linux

package detached

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/sqliteutil"
)

func detachedCancellationOutcome(execution workspace.DetachedKernelExecution) (bool, string) {
	if execution.State != workspace.DetachedKernelExecutionStateCancelRequested ||
		strings.TrimSpace(execution.CancelRequestID) == "" || execution.CancelAckAt == nil {
		return false, ""
	}
	switch strings.TrimSpace(execution.CancelSignal) {
	case "sigint":
		return true, "SIGINT"
	case "sigterm":
		return true, "SIGTERM"
	case "sigkill":
		return true, "SIGKILL"
	case "dequeue", workspace.KernelExecutionCancelProvider:
		return true, ""
	default:
		return false, ""
	}
}

func (e *Executor) cancelExecution(ctx context.Context, request CommandRequest) CommandResponse {
	execution, found, err := e.Store.GetDetachedKernelExecution(ctx, request.ExecutionID)
	if err != nil || !found || execution.CancelRequestID != request.CancelRequestID ||
		execution.StateVersion != request.ExpectedVersion {
		return commandResponseError(request.RequestID, "cancel_conflict", "detached kernel cancellation conflicts with durable state")
	}
	if err := e.reconcileCancellation(ctx); err != nil {
		return commandResponseError(request.RequestID, "cancel_conflict", "detached kernel cancellation could not be reconciled")
	}
	updated, found, err := e.Store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil || !found || updated.CancelAckAt == nil {
		return commandResponseError(request.RequestID, "cancel_conflict", "detached kernel cancellation conflicts with durable state")
	}
	response := commandResponseOK(request.RequestID)
	response.Cancel = &kernelruntime.BackendCancelReceipt{
		ExecutionID: updated.ExecutionID, CancelRequestID: updated.CancelRequestID,
		StateVersion: updated.StateVersion, Acknowledged: true,
		AckSequence: updated.CancelAckSequence, Signal: updated.CancelSignal,
	}
	return response
}

func (e *Executor) runCancellationReconciler(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := e.reconcileCancellation(ctx); err != nil {
			if sqliteutil.IsTransientContention(err) {
				log.Printf("kernel cancellation reconciliation deferred by store contention backend=%s", e.BackendID)
				continue
			}
			return err
		}
	}
}

func (e *Executor) reconcileCancellation(ctx context.Context) error {
	e.cancellationMu.Lock()
	defer e.cancellationMu.Unlock()
	ended, err := e.Store.ReconcileKernelTaskTermination(ctx, e.BackendID, e.BackendGeneration, e.ExecutorInstanceID)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if ended {
		e.draining = true
	}
	e.mu.Unlock()
	ids, err := e.Store.ListPendingKernelCancellationIDs(ctx, e.BackendID, e.BackendGeneration)
	if err != nil {
		return err
	}
	readyToDrain := len(ids) < 64
	for _, id := range ids {
		execution, found, err := e.Store.GetDetachedKernelExecution(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("active detached kernel execution disappeared")
		}
		if execution.State != workspace.DetachedKernelExecutionStateCancelRequested ||
			execution.CancelRequestID == "" {
			continue
		}
		_, err = e.applyDurableCancellation(ctx, execution)
		if err != nil {
			if errors.Is(err, workspace.ErrDetachedKernelExecutionConflict) {
				readyToDrain = false
				continue
			}
			return err
		}
	}
	e.mu.Lock()
	drain := e.draining
	containerActive := false
	for _, entry := range e.active {
		containerActive = containerActive || (entry != nil && entry.cancelContainer != nil)
	}
	e.mu.Unlock()
	// Do not close the provider observer's lifetime before its stop/inspect and
	// terminal receipt finish. Native workers still retire to close descendants.
	if drain && !containerActive && readyToDrain {
		e.beginDraining()
		e.requestShutdownIfDrained(ctx)
	}
	return nil
}

func (e *Executor) applyDurableCancellation(
	ctx context.Context,
	execution workspace.DetachedKernelExecution,
) (workspace.DetachedKernelExecution, error) {
	durableRequest, err := workspace.DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
	if err != nil {
		return workspace.DetachedKernelExecution{}, err
	}
	e.mu.Lock()
	activeEntry, active := e.active[execution.ExecutionID]
	containerExecution := active && activeEntry != nil && activeEntry.cancelContainer != nil
	if !containerExecution {
		// Fence dispatch before a queued cancellation is acknowledged; otherwise
		// a request already read from SQLite could start after being dequeued.
		e.draining = true
	}
	e.mu.Unlock()
	if !active && execution.WorkerStartedAt != nil {
		// Absence from memory is not proof that a previously started external
		// workload stopped. Preserve its ledger for physical-evidence recovery.
		return workspace.DetachedKernelExecution{}, errors.New("started detached execution has no owned outcome observer")
	}
	updated := execution
	if execution.CancelAckAt == nil {
		signal := workspace.KernelExecutionCancelProvider
		if containerExecution {
			activeEntry.cancelContainer()
		} else {
			interrupted := e.Manager.InterruptSessionWithReason(durableRequest.FrameID, durableRequest.FrameIncarnationID,
				durableRequest.RootFrameIncarnationID, durableRequest.ExecutionID, execution.ReasonCode)
			if interrupted.Dequeued || !active {
				signal = "dequeue"
			} else if interrupted.Interrupted && interrupted.Via == "sigint" {
				signal = "sigint"
			}
		}
		updated, err = e.Store.AcknowledgeKernelExecutionCancel(ctx, workspace.AcknowledgeKernelExecutionCancelInput{
			ExecutionID: execution.ExecutionID, BackendGeneration: e.BackendGeneration,
			ExecutorInstanceID: e.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
			CancelRequestID: execution.CancelRequestID, AckSequence: execution.LastObservationSequence + 1,
			Signal: signal,
		})
		if err != nil {
			return workspace.DetachedKernelExecution{}, err
		}
	}
	// An accepted-but-undispatched request has no observer to settle it. Publish
	// its result here through the same receipt writer; active observers serialize
	// with this acknowledgement and retain their actual provider outcome.
	if !active {
		now := time.Now().UTC()
		if err := e.commitOutcome(durableRequest, kernelruntime.ExecutionOutcome{
			Response: kernelruntime.Response{Interrupted: true}, StartedAt: now, FinishedAt: now,
		}, now); err != nil {
			return workspace.DetachedKernelExecution{}, err
		}
		var found bool
		updated, found, err = e.Store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
		if err != nil {
			return workspace.DetachedKernelExecution{}, err
		}
		if !found {
			return workspace.DetachedKernelExecution{}, errors.New("cancelled detached kernel execution disappeared")
		}
	}
	return updated, nil
}
