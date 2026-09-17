package server

import (
	"fmt"

	kernelruntime "synon-go/internal/kernel"

	workspace "synon-go/internal/persistence/workspace"
)

func kernelOutcomeStatus(outcome kernelruntime.ExecutionOutcome) (string, string) {
	stderr := outcome.Response.Stderr
	if outcome.Response.Error != "" {
		if stderr != "" {
			stderr += "\n"
		}
		stderr += outcome.Response.Error
	}
	if outcome.Response.Interrupted || outcome.TimedOut {
		return stderr, "cancelled"
	}
	if outcome.Err != nil {
		if stderr != "" {
			stderr += "\n"
		}
		stderr += outcome.Err.Error()
		return stderr, "error"
	}
	if outcome.Response.Error != "" {
		return stderr, "error"
	}
	return stderr, "ok"
}

func (s *Server) publishAgentKernelEvent(access workspace.KernelFrameAccess, started kernelruntime.ExecutionStarted, phase string, extra map[string]any) {
	payload := map[string]any{"phase": phase, "origin": "agent", "language": started.Language, "environment": started.Environment, "kernel_target": started.KernelKind, "tool_use_id": started.ToolUseID}
	for key, value := range extra {
		payload[key] = value
	}
	_, _ = s.publishCompatEvent(workspace.RealtimeEventInput{ID: fmt.Sprintf("kernel-execution:%s:%s", started.ExecID, phase), UserID: access.UserID, ProjectID: access.Frame.ProjectID, RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID, Type: "execution_cell_update", Payload: payload})
}
