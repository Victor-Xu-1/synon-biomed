package processsupervisor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultInactivityTimeout bounds local helper processes that are alive but
	// no longer producing output, consuming CPU, or performing I/O. It is not a
	// wall-clock deadline: every observed activity sample extends the window.
	DefaultInactivityTimeout = 5 * time.Minute
	terminationWaitTimeout   = 5 * time.Second
)

var ErrInactivity = errors.New("process stopped making observable progress")

// InactivityError reports that a live process exceeded its activity window.
// The caller still owns domain-specific recovery and user-facing wording.
type InactivityError struct {
	Duration time.Duration
}

func (e *InactivityError) Error() string {
	if e == nil || e.Duration <= 0 {
		return ErrInactivity.Error()
	}
	return fmt.Sprintf("%s for %s", ErrInactivity, e.Duration)
}

func (e *InactivityError) Is(target error) bool {
	return target == ErrInactivity
}

// InactivityWatchdog distinguishes process liveness from useful work. Output
// writers call MarkActivity, while Wait also samples operating-system CPU and
// I/O counters for silent solvers, compilers, and download clients.
type InactivityWatchdog struct {
	timeout  time.Duration
	activity chan struct{}
}

func NewInactivityWatchdog(timeout time.Duration) *InactivityWatchdog {
	if timeout <= 0 {
		timeout = DefaultInactivityTimeout
	}
	return &InactivityWatchdog{timeout: timeout, activity: make(chan struct{}, 1)}
}

func (w *InactivityWatchdog) MarkActivity() {
	if w == nil {
		return
	}
	select {
	case w.activity <- struct{}{}:
	default:
	}
}

// Wait returns the command's terminal result, the caller context error, or an
// InactivityError. terminate must stop the whole owned process tree whenever
// the caller's execution boundary supports descendants.
func (w *InactivityWatchdog) Wait(
	ctx context.Context,
	pid int,
	done <-chan error,
	terminate func() error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if done == nil {
		return errors.New("supervised process completion channel is required")
	}
	if w == nil {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return errors.Join(ctx.Err(), terminateProcessAndWait(done, terminate))
		}
	}

	interval := inactivityPollInterval(w.timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastActivity := time.Now()
	previous := sampleProcessActivity(pid)
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return errors.Join(ctx.Err(), terminateProcessAndWait(done, terminate))
		case <-w.activity:
			lastActivity = time.Now()
		case now := <-ticker.C:
			current := sampleProcessActivity(pid)
			if processActivityAdvanced(previous, current) {
				lastActivity = now
			}
			previous = current
			if now.Sub(lastActivity) < w.timeout {
				continue
			}
			inactivity := &InactivityError{Duration: w.timeout}
			return errors.Join(inactivity, terminateProcessAndWait(done, terminate))
		}
	}
}

func inactivityPollInterval(timeout time.Duration) time.Duration {
	interval := timeout / 6
	if interval < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	if interval > 5*time.Second {
		return 5 * time.Second
	}
	return interval
}

func terminateProcessAndWait(done <-chan error, terminate func() error) error {
	var terminateErr error
	if terminate != nil {
		terminateErr = terminate()
	}
	timer := time.NewTimer(terminationWaitTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return terminateErr
	case <-timer.C:
		return errors.Join(terminateErr, errors.New("supervised process did not exit after termination"))
	}
}

type processActivityIdentity struct {
	PID        int
	StartTicks uint64
}

type processActivityCounters struct {
	CPU uint64
	IO  uint64
}

type processActivitySnapshot struct {
	Available bool
	Processes map[processActivityIdentity]processActivityCounters
}

func processActivityAdvanced(previous, current processActivitySnapshot) bool {
	if !current.Available {
		// Disappearance of the root process is itself an observed lifecycle
		// transition. Give command.Wait a fresh activity window to drain output
		// and reap the process instead of relabeling normal exit cleanup as a
		// stalled command.
		return previous.Available
	}
	if !previous.Available {
		return true
	}
	if len(previous.Processes) != len(current.Processes) {
		return true
	}
	for identity, currentCounters := range current.Processes {
		previousCounters, ok := previous.Processes[identity]
		if !ok || currentCounters.CPU > previousCounters.CPU || currentCounters.IO > previousCounters.IO {
			return true
		}
	}
	return false
}
