package sessionrunner

import (
	"reflect"
	"testing"
)

func TestMachineAcceptsProviderToolRoundsAndTerminalCompletion(t *testing.T) {
	machine := NewMachine()
	sequence := []Phase{
		PhaseClaim, PhaseRecovery, PhaseContext, PhaseSnapshot,
		PhaseProvider, PhaseTool, PhaseProvider, PhaseTool, PhaseVerify,
		PhaseComplete, PhaseTerminal,
	}
	for _, phase := range sequence {
		if _, _, err := machine.Enter(phase); err != nil {
			t.Fatalf("enter %s: %v", phase, err)
		}
	}
	if current := machine.Current(); current != PhaseTerminal {
		t.Fatalf("current phase = %s, want terminal", current)
	}
	history := machine.History()
	observed := make([]Phase, len(history))
	for index, transition := range history {
		observed[index] = transition.To
	}
	if !reflect.DeepEqual(observed, sequence) {
		t.Fatalf("phase history = %v, want %v", observed, sequence)
	}
}

func TestMachineAllowsIdempotentToolProgressAndRejectsReordering(t *testing.T) {
	machine := NewMachine()
	for _, phase := range []Phase{PhaseClaim, PhaseRecovery, PhaseContext, PhaseSnapshot, PhaseProvider, PhaseTool} {
		if _, _, err := machine.Enter(phase); err != nil {
			t.Fatal(err)
		}
	}
	if _, changed, err := machine.Enter(PhaseTool); err != nil || changed {
		t.Fatalf("idempotent tool phase changed=%t err=%v", changed, err)
	}
	if _, _, err := machine.Enter(PhaseContext); err == nil {
		t.Fatal("expected backward tool-to-context transition to fail")
	}
}

func TestMachineAllowsPauseFromRecoveryProviderAndTool(t *testing.T) {
	for _, prefix := range [][]Phase{
		{PhaseClaim, PhaseRecovery},
		{PhaseClaim, PhaseRecovery, PhaseContext, PhaseSnapshot, PhaseProvider},
		{PhaseClaim, PhaseRecovery, PhaseContext, PhaseSnapshot, PhaseProvider, PhaseTool},
	} {
		machine := NewMachine()
		for _, phase := range prefix {
			if _, _, err := machine.Enter(phase); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := machine.Enter(PhasePaused); err != nil {
			t.Fatalf("pause from %s: %v", prefix[len(prefix)-1], err)
		}
		if _, _, err := machine.Enter(PhaseTerminal); err != nil {
			t.Fatalf("terminal after pause: %v", err)
		}
	}
}

func TestMachineAllowsBoundedCompletionGateToReturnToProvider(t *testing.T) {
	machine := NewMachine()
	for _, phase := range []Phase{
		PhaseClaim, PhaseRecovery, PhaseContext, PhaseSnapshot, PhaseProvider, PhaseVerify,
		PhaseProvider, PhaseTool, PhaseVerify, PhaseComplete, PhaseTerminal,
	} {
		if _, _, err := machine.Enter(phase); err != nil {
			t.Fatalf("enter %s: %v", phase, err)
		}
	}
}
