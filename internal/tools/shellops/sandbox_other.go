//go:build !linux

package shellops

import (
	"runtime"
)

func wrapSandboxCommand(request sandboxCommand) (string, []string, []string, SandboxEvidence, error) {
	evidence := platformSandboxStatus()
	return request.Command, request.Args, request.Environment, evidence, nil
}

func platformSandboxStatus() SandboxEvidence {
	return SandboxEvidence{
		Platform: runtime.GOOS, Mode: "process-only", Filesystem: "workspace-path-validation",
		Network: "host", Environment: "minimal", ResourceLimits: false, Available: false,
		Reason: "native filesystem and network sandbox is not implemented on this platform",
	}
}
