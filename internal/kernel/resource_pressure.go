package kernel

import "synon-go/internal/processsupervisor"

type ResourcePressureFailure struct {
	Observation processsupervisor.MemoryPressure
}

func (*ResourcePressureFailure) Error() string {
	return "kernel execution cannot progress under sustained memory pressure within its admitted memory budget"
}

// A handle is already bound to one execution and worker generation. Resource
// intervention goes through its existing lifecycle owner, never a PID-only
// termination path or a user-cancellation receipt.
func (h *ExecutionHandle) RequestResourceRecovery(observation processsupervisor.MemoryPressure) bool {
	if h == nil || h.execution == nil || observation.Status != "pressured" {
		return false
	}
	e := h.execution
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status != "running" {
		return false
	}
	select {
	case e.resourceFailure <- &ResourcePressureFailure{Observation: observation}:
		return true
	default:
		return false
	}
}

func (h *ExecutionHandle) IsRunning() bool {
	if h == nil || h.execution == nil {
		return false
	}
	h.execution.mu.Lock()
	defer h.execution.mu.Unlock()
	return h.execution.status == "running"
}

func (h *ExecutionHandle) OutputProgressSequence() uint64 {
	if h == nil || h.execution == nil {
		return 0
	}
	h.execution.mu.Lock()
	defer h.execution.mu.Unlock()
	return uint64(h.execution.stdoutSeq)
}
