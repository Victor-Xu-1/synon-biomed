// Package sessionrunner owns the canonical Harness task-phase state machine.
// Server adapters persist and project transitions, but cannot invent another
// phase order.
package sessionrunner

import (
	"fmt"
	"sync"
	"time"
)

// Phase is one durable logical stage of a runner cycle.
type Phase string

const (
	PhaseClaim    Phase = "claim"
	PhaseRecovery Phase = "recovery"
	PhaseContext  Phase = "context"
	PhaseSnapshot Phase = "snapshot"
	PhaseProvider Phase = "provider"
	PhaseTool     Phase = "tool"
	PhaseVerify   Phase = "verify"
	PhaseComplete Phase = "complete"
	PhasePaused   Phase = "paused"
	PhaseTerminal Phase = "terminal"
)

// CanonicalPhases is the architecture inventory order. Provider and tool may
// alternate within this order; paused is a recoverable terminal projection for
// one cycle, while terminal closes the cycle.
var CanonicalPhases = []Phase{
	PhaseClaim,
	PhaseRecovery,
	PhaseContext,
	PhaseSnapshot,
	PhaseProvider,
	PhaseTool,
	PhaseVerify,
	PhaseComplete,
	PhasePaused,
	PhaseTerminal,
}

// Transition is an immutable observed phase change.
type Transition struct {
	Sequence int
	From     Phase
	To       Phase
	At       time.Time
}

// Machine validates phase transitions and keeps a concurrency-safe trace.
type Machine struct {
	mu      sync.Mutex
	current Phase
	history []Transition
}

// NewMachine creates an unstarted runner phase machine.
func NewMachine() *Machine {
	return &Machine{}
}

// Enter advances to a legal phase. Re-entering the current phase is
// idempotent so concurrent tool progress events do not create fake phases.
func (machine *Machine) Enter(next Phase) (Transition, bool, error) {
	if machine == nil {
		return Transition{}, false, fmt.Errorf("session runner phase machine is nil")
	}
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if next == "" {
		return Transition{}, false, fmt.Errorf("session runner phase is empty")
	}
	if machine.current == next {
		return Transition{}, false, nil
	}
	if !transitionAllowed(machine.current, next) {
		return Transition{}, false, fmt.Errorf("session runner phase transition %q -> %q is invalid", machine.current, next)
	}
	transition := Transition{
		Sequence: len(machine.history) + 1,
		From:     machine.current,
		To:       next,
		At:       time.Now().UTC(),
	}
	machine.current = next
	machine.history = append(machine.history, transition)
	return transition, true, nil
}

// Current returns the current phase.
func (machine *Machine) Current() Phase {
	if machine == nil {
		return ""
	}
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return machine.current
}

// History returns a detached transition trace.
func (machine *Machine) History() []Transition {
	if machine == nil {
		return nil
	}
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return append([]Transition(nil), machine.history...)
}

func transitionAllowed(current, next Phase) bool {
	if current == "" {
		return next == PhaseClaim
	}
	if next == PhaseTerminal {
		return current != PhaseTerminal
	}
	switch current {
	case PhaseClaim:
		return next == PhaseRecovery || next == PhasePaused
	case PhaseRecovery:
		return next == PhaseContext || next == PhaseVerify || next == PhasePaused
	case PhaseContext:
		return next == PhaseSnapshot || next == PhasePaused
	case PhaseSnapshot:
		return next == PhaseProvider || next == PhasePaused
	case PhaseProvider:
		return next == PhaseTool || next == PhaseVerify || next == PhasePaused
	case PhaseTool:
		return next == PhaseProvider || next == PhaseVerify || next == PhasePaused
	case PhaseVerify:
		// A bounded completion gate may reject the current candidate and ask
		// the provider for a replacement before completion. This is not a new
		// task or a second runner; it remains one verified execution unit.
		return next == PhaseProvider || next == PhaseComplete || next == PhasePaused
	case PhaseComplete:
		return next == PhasePaused
	case PhasePaused, PhaseTerminal:
		return false
	default:
		return false
	}
}
