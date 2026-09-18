package server

import (
	"context"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// Resource sampling takes place outside the durable execution transaction. A
// persistent worker may finish one cell and begin another during that sample.
// The monotonic execution receipt fences attribution without another executor
// or a second source of process facts.
func (s *Server) fenceDetachedExecutionObservation(ctx context.Context, kernel *kernelruntime.SessionKernel, entry workspace.DetachedKernelInventoryEntry) error {
	observation := kernel.ExecutionObservation
	if observation == nil || observation.Status == "unavailable" {
		return nil
	}
	valid := false
	if entry.Execution != nil && entry.Execution.WorkerStartedAt != nil && observation.ExecutionID == entry.Execution.ExecutionID {
		current, found, err := s.workspaceStore.GetDetachedKernelExecution(ctx, entry.Execution.ExecutionID)
		if err != nil {
			return err
		}
		valid = found && detachedObservationStillCurrent(entry, current)
	}
	if !valid {
		observation.Status = "unavailable"
		observation.Processes = []kernelruntime.ObservedProcess{}
		observation.MemoryPressure = nil
	}
	return nil
}

func detachedObservationStillCurrent(entry workspace.DetachedKernelInventoryEntry, current workspace.DetachedKernelExecution) bool {
	return entry.Execution != nil && entry.Execution.WorkerStartedAt != nil && current.ExecutionID == entry.Execution.ExecutionID &&
		current.BackendID == entry.Backend.BackendID && current.BackendGeneration == entry.Backend.BackendGeneration &&
		current.WorkerStartedAt != nil && current.WorkerStartedAt.Equal(*entry.Execution.WorkerStartedAt) &&
		(current.State == workspace.DetachedKernelExecutionStateStarted || current.State == workspace.DetachedKernelExecutionStateCancelRequested)
}
