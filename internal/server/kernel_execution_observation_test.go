package server

import (
	"context"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/processsupervisor"
)

func TestDetachedExecutionObservationFencesTerminalAndReplacementReceipts(t *testing.T) {
	now := time.Now().UTC()
	initial := workspace.DetachedKernelExecution{ExecutionID: "cell-one", BackendID: "backend-one", BackendGeneration: 1,
		WorkerStartedAt: &now, State: workspace.DetachedKernelExecutionStateStarted}
	entry := workspace.DetachedKernelInventoryEntry{Backend: workspace.KernelExecutionBackend{BackendID: initial.BackendID, BackendGeneration: 1}, Execution: &initial}
	for _, item := range []struct {
		name   string
		mutate func(*workspace.DetachedKernelExecution)
		valid  bool
	}{
		{"same execution", func(*workspace.DetachedKernelExecution) {}, true},
		{"cancelling still alive", func(e *workspace.DetachedKernelExecution) {
			e.State = workspace.DetachedKernelExecutionStateCancelRequested
		}, true},
		{"terminal receipt", func(e *workspace.DetachedKernelExecution) { e.State = workspace.DetachedKernelExecutionStateTerminal }, false},
		{"queued execution", func(e *workspace.DetachedKernelExecution) { e.State = workspace.DetachedKernelExecutionStateAccepted }, false},
		{"replacement cell", func(e *workspace.DetachedKernelExecution) { e.ExecutionID = "cell-two" }, false},
		{"replacement backend", func(e *workspace.DetachedKernelExecution) { e.BackendID = "backend-two" }, false},
		{"new generation", func(e *workspace.DetachedKernelExecution) { e.BackendGeneration++ }, false},
		{"not started", func(e *workspace.DetachedKernelExecution) { e.WorkerStartedAt = nil }, false},
		{"different start", func(e *workspace.DetachedKernelExecution) { v := now.Add(time.Second); e.WorkerStartedAt = &v }, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			current := initial
			item.mutate(&current)
			if got := detachedObservationStillCurrent(entry, current); got != item.valid {
				t.Fatalf("current=%t want=%t", got, item.valid)
			}
		})
	}
}

func TestDetachedExecutionObservationUsesDurableReadAndPropagatesFailure(t *testing.T) {
	store, _, _ := newKernelAPITestRuntime(t)
	s := &Server{workspaceStore: store}
	now := time.Now().UTC()
	entry := workspace.DetachedKernelInventoryEntry{Execution: &workspace.DetachedKernelExecution{ExecutionID: "cell-not-in-store", WorkerStartedAt: &now}}
	makeKernel := func() kernelruntime.SessionKernel {
		return kernelruntime.SessionKernel{ExecutionObservation: &kernelruntime.ExecutionObservation{
			ExecutionID: entry.Execution.ExecutionID, SampledAt: now, Status: "observed",
			Processes:      []kernelruntime.ObservedProcess{{PID: 1, Name: "must-not-be-attributed"}},
			MemoryPressure: &processsupervisor.MemoryPressure{Status: "pressured", LimitBytes: 100},
		}}
	}
	kernel := makeKernel()
	if err := s.fenceDetachedExecutionObservation(context.Background(), &kernel, entry); err != nil {
		t.Fatal(err)
	}
	if kernel.ExecutionObservation.Status != "unavailable" || len(kernel.ExecutionObservation.Processes) != 0 || kernel.ExecutionObservation.MemoryPressure != nil {
		t.Fatal("missing durable identity was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	kernel = makeKernel()
	if err := s.fenceDetachedExecutionObservation(ctx, &kernel, entry); err == nil {
		t.Fatal("failed durable read was hidden")
	}
}
