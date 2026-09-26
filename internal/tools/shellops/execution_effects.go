package shellops

import (
	"context"

	"synon-go/internal/executionprep"
)

// shellExecutionCommand retains the existing process/sandbox path. Only a
// host-created obligation selects the native guard; unsupported shells and
// unavailable native runtimes fail closed instead of entering compatibility.
func shellExecutionCommand(ctx context.Context, shellName, command string) (string, []string, error) {
	plan := executionprep.ObservationFromContext(ctx)
	if plan == nil {
		return shellExecutor(shellName, command)
	}
	if shellName != "PowerShell" {
		return "", nil, executionprep.ErrObservationUnproved
	}
	guarded, err := executionprep.GuardedPowerShell(command, plan)
	if err != nil {
		return "", nil, err
	}
	executable, err := executionprep.NativePowerShell()
	if err != nil {
		return "", nil, err
	}
	return executable, []string{"-NoProfile", "-NonInteractive", "-Command", guarded}, nil
}
