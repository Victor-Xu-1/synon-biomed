package server

import (
	"context"
	"fmt"

	kernelruntime "synon-go/internal/kernel"
)

// RunManagedScientificRuntimeProvisioner binds both required scientific core
// runtimes to one service-owned lifecycle. The shared supervisor makes Python
// and R reuse the same immutable Conda root and prevents HTTP/task callers
// from creating competing bootstrap paths.
func (s *Server) RunManagedScientificRuntimeProvisioner(ctx context.Context) error {
	return s.runManagedScientificRuntimeProvisioner(ctx, nil)
}

// RunManagedScientificRuntimeProvisionerWithReady is the startup-supervisor
// entrypoint. It does not report readiness until both required runtimes (and
// the optional bundled MCP runtime when enabled) have completed successfully.
// Keeping this callback at the service boundary prevents a generic supervisor
// from advertising a healthy component while first-run installation is still
// downloading packages.
func (s *Server) RunManagedScientificRuntimeProvisionerWithReady(ctx context.Context, ready func()) error {
	return s.runManagedScientificRuntimeProvisioner(ctx, ready)
}

func (s *Server) runManagedScientificRuntimeProvisioner(ctx context.Context, ready func()) error {
	if s == nil || s.kernelManager == nil || !s.ManagedScientificRuntimeProvisioningEnabled() {
		if ready != nil {
			ready()
		}
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
	if ready != nil {
		ready()
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

// scientificCoreRuntimeHealth is deliberately separate from gateway health:
// the gateway may serve the UI while a first-run scientific installation is
// still pending, but callers must never mistake that for Python/R readiness.
// It exposes only stable status fields and no installer paths or diagnostics.
func (s *Server) scientificCoreRuntimeHealth() map[string]any {
	platform := kernelruntime.ManagedScientificRuntimePlatform()
	platformSupported := kernelruntime.ManagedScientificRuntimePlatformSupported()
	result := map[string]any{
		"required":           true,
		"ready":              false,
		"platform":           platform,
		"platform_supported": platformSupported,
	}
	if s == nil || s.kernelManager == nil {
		result["status"] = "unavailable"
		result["code"] = "bundled_runtime_unavailable"
		return result
	}
	python := s.kernelManager.ManagedPythonProvisioningDetails()
	r := s.kernelManager.ManagedRProvisioningDetails()
	result["python"] = managedCoreRuntimeHealthValue(python)
	result["r"] = managedCoreRuntimeHealthValue(r)
	if !s.ManagedScientificRuntimeProvisioningEnabled() {
		if !platformSupported {
			result["status"] = "unsupported"
			result["code"] = "bundled_runtime_platform_unsupported"
		} else {
			result["status"] = "unavailable"
			result["code"] = "bundled_runtime_unavailable"
		}
		return result
	}
	if python.Status == "ready" && r.Status == "ready" {
		result["status"] = "ready"
		result["ready"] = true
		return result
	}
	if python.Status == "failed" || r.Status == "failed" || python.Status == "unavailable" || r.Status == "unavailable" {
		result["status"] = "failed"
		result["code"] = "bundled_runtime_provisioning_failed"
		return result
	}
	result["status"] = "installing"
	return result
}

func managedCoreRuntimeHealthValue(details kernelruntime.ManagedPythonProvisioningDetails) map[string]any {
	result := map[string]any{"status": details.Status}
	if details.Phase != "" {
		result["phase"] = details.Phase
	}
	return result
}

func (s *Server) kernelRuntimeWake() <-chan struct{} {
	if s == nil || s.kernelManager == nil {
		return nil
	}
	return s.kernelManager.RuntimeWake()
}
