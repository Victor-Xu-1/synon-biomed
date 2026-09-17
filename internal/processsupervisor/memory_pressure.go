package processsupervisor

import "time"

// MemoryPressure is observed resource evidence, never a progress estimate.
// Zero limits mean unbounded. Paths, arguments and environment values are not
// part of the public projection or the terminal receipt.
type MemoryPressure struct {
	Status           string  `json:"status"`
	CurrentBytes     uint64  `json:"current_bytes"`
	HighBytes        uint64  `json:"high_bytes"`
	LimitBytes       uint64  `json:"limit_bytes"`
	SwapBytes        uint64  `json:"swap_bytes"`
	FullStallPercent float64 `json:"full_stall_percent"`
}

type MemoryPressureSample struct {
	MemoryPressure
	At               time.Time
	Cgroup           string
	FullStallUsec    uint64
	UserCPUUsec      uint64
	ProgressSequence uint64
}

type MemoryPressureAction int

const (
	MemoryPressureObserve MemoryPressureAction = iota
	MemoryPressureRelieve
	MemoryPressureRecover
)

// The window measures sustained resource stall, not total execution duration.
// Silent productive work, a temporary peak, missing telemetry, and a reset of
// monotonic counters cannot consume the recovery window.
type MemoryPressurePolicy struct {
	reliefAfter, recoveryAfter         time.Duration
	previous                           MemoryPressureSample
	stalledSince                       time.Time
	reliefAttempted, recoveryRequested bool
}

func NewMemoryPressurePolicy(reliefAfter, recoveryAfter time.Duration) *MemoryPressurePolicy {
	return &MemoryPressurePolicy{reliefAfter: reliefAfter, recoveryAfter: recoveryAfter}
}

func (p *MemoryPressurePolicy) Observe(s MemoryPressureSample) MemoryPressureAction {
	before := p.previous
	p.previous = s
	elapsed := s.At.Sub(before.At)
	if s.Status == "unavailable" || s.LimitBytes == 0 || s.ProgressSequence != before.ProgressSequence || s.At.IsZero() || before.At.IsZero() || s.Cgroup != before.Cgroup ||
		elapsed <= 0 || elapsed > 30*time.Second || s.FullStallUsec < before.FullStallUsec || s.UserCPUUsec < before.UserCPUUsec {
		p.stalledSince = time.Time{}
		return MemoryPressureObserve
	}
	interval := float64(elapsed.Microseconds())
	full := float64(s.FullStallUsec-before.FullStallUsec) / interval
	userCPU := float64(s.UserCPUUsec-before.UserCPUUsec) / interval
	nearLimit := s.LimitBytes > 0 && s.CurrentBytes >= s.LimitBytes-s.LimitBytes/20
	aboveHigh := s.HighBytes > 0 && s.CurrentBytes >= s.HighBytes
	// Kernel PSI accounting and the observer's monotonic clock can advance at
	// different rates, especially across VM clock adjustments. Do not discard
	// a severe stall solely because its delta exceeds the observer interval;
	// require the kernel's independent bounded average to corroborate it.
	uncorroboratedSkew := full > 1.05 && s.FullStallPercent < 80
	if full < 0.8 || uncorroboratedSkew || userCPU >= 0.1 || (!aboveHigh && !nearLimit) {
		p.stalledSince = time.Time{}
		return MemoryPressureObserve
	}
	if p.stalledSince.IsZero() {
		p.stalledSince = before.At
	}
	stalledFor := s.At.Sub(p.stalledSince)
	if !p.reliefAttempted && s.LimitBytes > 0 && s.HighBytes > 0 && s.HighBytes < s.LimitBytes && stalledFor >= p.reliefAfter {
		p.reliefAttempted = true
		p.stalledSince = s.At
		return MemoryPressureRelieve
	}
	if !p.recoveryRequested && stalledFor >= p.recoveryAfter {
		p.recoveryRequested = true
		return MemoryPressureRecover
	}
	return MemoryPressureObserve
}
