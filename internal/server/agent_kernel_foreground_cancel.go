package server

import (
	"context"
	"errors"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// handleAgentKernelForegroundContextDone keeps foreground-wait cancellation
// separate from the main execution setup path. A request timeout detaches an
// already-started operation, while explicit task stop and runtime drain settle
// it through the existing observer/receipt authority.
func (s *Server) handleAgentKernelForegroundContextDone(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	spec kernelruntime.SessionSpec,
	execID string,
	started kernelruntime.ExecutionStarted,
	handle *kernelruntime.ExecutionHandle,
	startObserver func() <-chan struct{},
) (map[string]any, error) {
	if s.kernelManager != nil {
		s.kernelManager.CancelHostCalls(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
	}
	cause := agentKernelContextCause(ctx)
	if agentKernelCallerRequiresExecutionStop(ctx) {
		// A user stop and a runtime drain are lifecycle commands, not short
		// foreground-wait cancellations. Never detach their computation. A
		// user stop settles the exact durable execution as cancelled; a drain
		// leaves the started operation recoverable by the next process.
		if errors.Is(cause, ErrGenerationStopped) {
			if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
				s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
				select {
				case <-handle.Done():
				case <-time.After(6 * time.Second):
				}
				return nil, errors.Join(cause, errors.New("kernel cancellation result channel is unavailable"))
			}
			settled := startObserver()
			s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
			select {
			case <-settled:
			case <-time.After(6 * time.Second):
			}
			return nil, cause
		}

		s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		select {
		case <-handle.Done():
		case <-time.After(6 * time.Second):
		}
		return nil, cause
	}
	if err := s.recordAgentKernelBackgroundStart(access, started); err != nil {
		s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		return nil, errors.New("kernel detached result channel is unavailable")
	}
	startObserver()
	return map[string]any{
		"status": "running", "exec_id": execID,
		"message": "Kernel cell continues in the background after the caller stopped waiting.",
	}, nil
}
