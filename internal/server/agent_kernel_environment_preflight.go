package server

import "strings"

// agentKernelEnvironmentRequiresManagedGeneration reports whether a kernel
// tool must be bound to an immutable managed-environment generation before a
// worker can be started. System runtimes remain available through their
// existing launcher path; custom runtimes must never fall through to a worker
// start with an empty generation.
func agentKernelEnvironmentRequiresManagedGeneration(publicName, environment string) bool {
	name := strings.ToLower(strings.TrimSpace(publicName))
	if name == "r" || name == softwareRuntimeToolName {
		return true
	}
	if name != "python" && name != "bash" {
		return false
	}
	return !agentKernelSystemPythonEnvironment(environment) &&
		strings.TrimSpace(environment) != agentKernelManagedPythonEnvironment
}

// agentKernelEnvironmentReadinessPreflight is a non-executing, durable result
// used when a requested custom environment is unavailable. Returning a
// preflight result (rather than waiting for worker startup) lets the runner
// continue with the same logical task and choose the governed environment
// preparation route without entering a recovery timeout loop.
func agentKernelEnvironmentReadinessPreflight(cause error) map[string]any {
	message := "The selected analysis environment is not ready for execution."
	recovery := "Check available environments and prepare one compatible immutable environment before retrying this step."
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		// Keep infrastructure detail out of the user-facing stream. The durable
		// event records the stable status; detailed causes stay in the caller's
		// diagnostic path.
		recovery = "The environment readiness check failed; inspect the available environments, repair the selected runtime, then retry this step."
	}
	return map[string]any{
		"ok":        false,
		"executed":  false,
		"status":    "environment_preflight_required",
		"message":   message,
		"recovery":  recovery,
		"retryable": true,
	}
}
