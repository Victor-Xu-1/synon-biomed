package server

import (
	"strings"
	"time"
)

const maxRunnerIdlePollInterval = 30 * time.Second

// nextRunnerIdlePollInterval exponentially reduces durable recovery polling
// after an empty claim while keeping a bounded restart/recovery backstop. Normal
// work remains event-driven and resets to the configured base interval.
func nextRunnerIdlePollInterval(current, base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if current < base {
		return base
	}
	if current >= maxRunnerIdlePollInterval {
		return maxRunnerIdlePollInterval
	}
	next := current * 2
	if next < current || next > maxRunnerIdlePollInterval {
		return maxRunnerIdlePollInterval
	}
	return next
}

func stopRunnerIdleTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (s *Server) frameResumeDispatchWakeChannel() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.frameResumeDispatchWakeMu.Lock()
	defer s.frameResumeDispatchWakeMu.Unlock()
	if s.frameResumeDispatchWake == nil {
		s.frameResumeDispatchWake = make(chan struct{})
	}
	return s.frameResumeDispatchWake
}

func (s *Server) signalFrameResumeDispatchForEvent(eventType string) {
	switch strings.TrimSpace(eventType) {
	case "frame_resumed", "frame_resume_dispatch_woken", "frame_resume_dispatch_interrupted", "runner_finished":
	default:
		return
	}
	if s == nil {
		return
	}
	s.frameResumeDispatchWakeMu.Lock()
	if s.frameResumeDispatchWake == nil {
		s.frameResumeDispatchWake = make(chan struct{})
	}
	close(s.frameResumeDispatchWake)
	s.frameResumeDispatchWake = make(chan struct{})
	s.frameResumeDispatchWakeMu.Unlock()
}
