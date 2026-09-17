//go:build linux

package detached

import (
	"context"
	"log"
	"time"

	"synon-go/internal/processsupervisor"
)

// Pressure supervision belongs to the existing executor lifetime. Heartbeats
// remain liveness evidence only; SQLite contention must not suspend resource
// relief, and an observation failure must never terminate an active workload.
func (e *Executor) runMemoryPressureSupervisor(ctx context.Context) {
	if e.memoryDomain == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var executionID string
	var policy *processsupervisor.MemoryPressurePolicy
	observationUnavailable := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		e.mu.Lock()
		id := ""
		var active *activeExecution
		for candidate, entry := range e.active {
			if entry.kernel != nil && entry.kernel.IsRunning() {
				id, active = candidate, entry
				break
			}
		}
		draining := e.draining
		e.mu.Unlock()
		if draining || active == nil {
			executionID, policy = "", nil
			continue
		}
		if id != executionID {
			executionID, policy = id, processsupervisor.NewMemoryPressurePolicy(30*time.Second, 2*time.Minute)
		}
		sample, err := e.memoryDomain.sample()
		if err != nil {
			policy.Observe(processsupervisor.MemoryPressureSample{})
			if !observationUnavailable {
				log.Printf("kernel resource observation unavailable backend=%s", e.BackendID)
			}
			observationUnavailable = true
			continue
		}
		observationUnavailable = false
		sample.ProgressSequence = active.kernel.OutputProgressSequence()
		switch policy.Observe(sample) {
		case processsupervisor.MemoryPressureRelieve:
			if err := e.memoryDomain.relieve(sample); err != nil {
				log.Printf("kernel memory pressure relief unavailable backend=%s execution=%s: %v", e.BackendID, id, err)
			} else {
				log.Printf("kernel memory pressure relieved within admitted budget backend=%s execution=%s limit_bytes=%d", e.BackendID, id, sample.LimitBytes)
			}
		case processsupervisor.MemoryPressureRecover:
			sample.Status = "pressured"
			if active.kernel.RequestResourceRecovery(sample.MemoryPressure) {
				log.Printf("kernel memory pressure recovery requested backend=%s execution=%s current_bytes=%d limit_bytes=%d", e.BackendID, id, sample.CurrentBytes, sample.LimitBytes)
			}
		}
	}
}
