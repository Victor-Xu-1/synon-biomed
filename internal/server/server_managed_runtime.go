package server

import (
	"context"
	"fmt"
)

// RunManagedScientificRuntimeProvisioner binds both required scientific core
// runtimes to one service-owned lifecycle. The shared supervisor makes Python
// and R reuse the same immutable Conda root and prevents HTTP/task callers
// from creating competing bootstrap paths.
func (s *Server) RunManagedScientificRuntimeProvisioner(ctx context.Context) error {
	if s == nil || s.kernelManager == nil || !s.ManagedScientificRuntimeProvisioningEnabled() {
		if ctx != nil {
			<-ctx.Done()
		}
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pythonErr := s.kernelManager.ProvisionManagedPythonEnvironment(ctx)
	if context.Cause(ctx) != nil {
		return nil
	}
	rErr := s.kernelManager.ProvisionManagedREnvironment(ctx)
	if context.Cause(ctx) != nil {
		return nil
	}
	if _, publishErr := s.publishGlobalEvent("environment_status", map[string]any{
		"environments": s.environmentStatus(false), "conda_disabled_reason": s.condaDisabledReason(),
	}); publishErr != nil {
		return publishErr
	}
	if pythonErr != nil && rErr != nil {
		return fmt.Errorf("provision required Python and R runtimes: Python: %w; R: %v", pythonErr, rErr)
	}
	if pythonErr != nil {
		return fmt.Errorf("provision required Python runtime: %w", pythonErr)
	}
	if rErr != nil {
		return fmt.Errorf("provision required R runtime: %w", rErr)
	}
	if s.mcpDirectory != nil && s.kernelManager.ManagedEnvironmentSupervisorEnabled() {
		if _, mcpErr := s.kernelManager.ProvisionBundledMCPPythonEnvironment(ctx); mcpErr != nil {
			return fmt.Errorf("provision bundled MCP Python runtime: %w", mcpErr)
		}
		s.mcpDirectory.ScheduleMissingBundledProbe("local")
	}
	<-ctx.Done()
	return nil
}

// RunManagedPythonProvisioner is retained as a source-compatible wrapper for
// older internal callers; the service supervisor uses the combined method
// above so there is only one bootstrap authority.
func (s *Server) RunManagedPythonProvisioner(ctx context.Context) error {
	return s.RunManagedScientificRuntimeProvisioner(ctx)
}

// RunManagedEnvironmentSupervisor owns task-requested environment mutations
// for the service lifetime. Individual task cancellation only releases that
// waiter; service shutdown remains the sole process-cancellation authority.
func (s *Server) RunManagedEnvironmentSupervisor(ctx context.Context) error {
	if s == nil || s.kernelManager == nil || !s.kernelManager.ManagedEnvironmentSupervisorEnabled() {
		if ctx != nil {
			<-ctx.Done()
		}
		return nil
	}
	return s.kernelManager.RunManagedEnvironmentSupervisor(ctx)
}

func (s *Server) ManagedEnvironmentSupervisorEnabled() bool {
	return s != nil && s.kernelManager != nil && s.kernelManager.ManagedEnvironmentSupervisorEnabled()
}

func (s *Server) ManagedPythonProvisioningEnabled() bool {
	return s != nil && s.kernelManager != nil && s.kernelManager.ManagedPythonProvisioningEnabled()
}

func (s *Server) ManagedScientificRuntimeProvisioningEnabled() bool {
	return s != nil && s.kernelManager != nil &&
		s.kernelManager.ManagedPythonProvisioningEnabled() && s.kernelManager.ManagedRProvisioningEnabled()
}

func (s *Server) kernelRuntimeWake() <-chan struct{} {
	if s == nil || s.kernelManager == nil {
		return nil
	}
	return s.kernelManager.RuntimeWake()
}
