package server

import (
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	runnermachine "synon-go/internal/sessionrunner"
)

func newSessionRunnerPhaseMachine() (*runnermachine.Machine, error) {
	machine := runnermachine.NewMachine()
	if _, _, err := machine.Enter(runnermachine.PhaseClaim); err != nil {
		return nil, err
	}
	return machine, nil
}

func ensureSessionRunnerExecutionPhaseMachine(run *sessionRunnerChatRun) error {
	if run == nil || run.phaseMachine != nil {
		return nil
	}
	machine, err := newSessionRunnerPhaseMachine()
	if err != nil {
		return err
	}
	if _, _, err := machine.Enter(runnermachine.PhaseRecovery); err != nil {
		return err
	}
	if _, _, err := machine.Enter(runnermachine.PhaseContext); err != nil {
		return err
	}
	run.phaseMachine = machine
	return nil
}

func advanceSessionRunnerPhase(run *sessionRunnerChatRun, phase runnermachine.Phase) error {
	if run == nil {
		return nil
	}
	if run.phaseMachine == nil {
		return fmt.Errorf("session runner phase machine is unavailable before %s", phase)
	}
	_, _, err := run.phaseMachine.Enter(phase)
	return err
}

func sessionRunnerLifecyclePhase(run *sessionRunnerChatRun) string {
	if run == nil || run.phaseMachine == nil {
		return ""
	}
	return string(run.phaseMachine.Current())
}

func advanceSessionRunnerPreparationPhase(run *sessionRunnerChatRun, stage string) error {
	stage = strings.TrimSpace(stage)
	var phase runnermachine.Phase
	switch stage {
	case "resume_state", "completion_recovery", "agent_authority", "model_resolution", "kernel_recovery":
		phase = runnermachine.PhaseRecovery
	case "prompt_snapshot":
		phase = runnermachine.PhaseSnapshot
	case "model_execution":
		phase = runnermachine.PhaseProvider
	default:
		phase = runnermachine.PhaseContext
	}
	return advanceSessionRunnerPhase(run, phase)
}

func advanceSessionRunnerEventPhase(run *sessionRunnerChatRun, event agentruntime.Event) error {
	if run == nil || run.phaseMachine == nil {
		return nil
	}
	switch event.Type {
	case agentruntime.EventModelResponse:
		current := run.phaseMachine.Current()
		if current == runnermachine.PhaseTool {
			if err := advanceSessionRunnerPhase(run, runnermachine.PhaseProvider); err != nil {
				return err
			}
		}
		if len(event.ToolCalls) > 0 {
			return advanceSessionRunnerPhase(run, runnermachine.PhaseTool)
		}
	case agentruntime.EventToolStarted, agentruntime.EventToolProgress,
		agentruntime.EventToolCompleted, agentruntime.EventToolFailed, agentruntime.EventToolPaused:
		if err := advanceSessionRunnerPhase(run, runnermachine.PhaseTool); err != nil {
			return err
		}
		if event.Type == agentruntime.EventToolPaused {
			return advanceSessionRunnerPhase(run, runnermachine.PhasePaused)
		}
	case agentruntime.EventFinal:
		return advanceSessionRunnerPhase(run, runnermachine.PhaseVerify)
	}
	return nil
}

func markSessionRunnerComplete(run *sessionRunnerChatRun) error {
	if run == nil || run.phaseMachine == nil {
		return nil
	}
	current := run.phaseMachine.Current()
	if current == runnermachine.PhaseProvider || current == runnermachine.PhaseTool {
		if err := advanceSessionRunnerPhase(run, runnermachine.PhaseVerify); err != nil {
			return err
		}
	}
	return advanceSessionRunnerPhase(run, runnermachine.PhaseComplete)
}

func resumeSessionRunnerProviderAfterCompletionGate(run *sessionRunnerChatRun) error {
	return advanceSessionRunnerPhase(run, runnermachine.PhaseProvider)
}

func markSessionRunnerPaused(run *sessionRunnerChatRun) error {
	return advanceSessionRunnerPhase(run, runnermachine.PhasePaused)
}

func markSessionRunnerTerminal(run *sessionRunnerChatRun) error {
	return advanceSessionRunnerPhase(run, runnermachine.PhaseTerminal)
}

func markActiveSessionRunnerPaused(run *activeSessionRun) error {
	if run == nil || run.phaseMachine == nil {
		return nil
	}
	current := run.phaseMachine.Current()
	if current == runnermachine.PhaseTerminal {
		return nil
	}
	_, _, err := run.phaseMachine.Enter(runnermachine.PhasePaused)
	return err
}
