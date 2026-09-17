package server

import (
	"context"
	"fmt"
)

// RunManagedPythonProvisioner binds the managed scientific runtime lifecycle
// to the service supervisor rather than to constructor or HTTP goroutines.
func (s *Server) RunManagedPythonProvisioner(ctx context.Context) error {
	if s == nil || s.kernelManager == nil || !s.kernelManager.ManagedPythonProvisioningEnabled() {
		if ctx != nil {
			<-ctx.Done()
		}
		return nil
	}
	err := s.kernelManager.ProvisionManagedPythonEnvironment(ctx)
	if context.Cause(ctx) != nil {
		return nil
	}
	if _, publishErr := s.publishGlobalEvent("environment_status", map[string]any{
		"environments": s.environmentStatus(false), "conda_disabled_reason": s.condaDisabledReason(),
	}); publishErr != nil {
		return publishErr
	}
	if err != nil {
		return err
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

func (s *Server) kernelRuntimeWake() <-chan struct{} {
	if s == nil || s.kernelManager == nil {
		return nil
	}
	return s.kernelManager.RuntimeWake()
}
