package kernel

import (
	"synon-go/internal/processsupervisor"
	"time"
)

// ExecutionObservation is a point-in-time process observation, not a claim
// that an executable has completed its scientific work. It shares the existing
// resource sampler and never dispatches, resumes, or polls an execution itself.
type ExecutionObservation struct {
	ExecutionID    string                            `json:"execution_id"`
	SampledAt      time.Time                         `json:"sampled_at"`
	Status         string                            `json:"status"`
	Processes      []ObservedProcess                 `json:"processes"`
	MemoryPressure *processsupervisor.MemoryPressure `json:"memory_pressure,omitempty"`
}

// ObservedProcess deliberately excludes argv, environment values and full
// executable paths. A process-name fallback is explicitly weaker evidence
// than a readable executable identity and must remain labelled as such.
type ObservedProcess struct {
	PID           int    `json:"pid"`
	ParentPID     int    `json:"parent_pid"`
	StartIdentity string `json:"start_identity"`
	Name          string `json:"name"`
	NameSource    string `json:"name_source"`
	State         string `json:"state"`
}

func executionObservation(counter processResourceCounter, sampledAt time.Time) *ExecutionObservation {
	status := "unavailable"
	processes := counter.observedProcesses
	if counter.visible && len(processes) > 0 {
		status = "observed"
		if counter.observationPartial {
			status = "partial"
		}
	}
	if processes == nil {
		processes = []ObservedProcess{}
	}
	return &ExecutionObservation{SampledAt: sampledAt, Status: status, Processes: processes, MemoryPressure: counter.memoryPressure}
}
