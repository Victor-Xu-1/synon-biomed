//go:build !windows && (!linux || !amd64)

package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func newConfinedWorkerCommand(string, string, []string, []string, []WorkerMount, []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func newConfinedWorkerCommandWithAuxiliary(string, string, []string, []string, []WorkerMount, []string, []*os.File) (*exec.Cmd, error) {
	return nil, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func platformConfinementEvidence() ConfinementEvidence {
	if runtime.GOOS == "darwin" {
		return ConfinementEvidence{
			Available: false, Mode: "unavailable", Reason: darwinConfinementUnverifiedReason,
		}
	}
	return ConfinementEvidence{
		Available: false, Mode: "unavailable", Reason: "Synon kernel confinement is unavailable on this platform",
	}
}

func probePlatformConfinement() ConfinementEvidence {
	return platformConfinementEvidence()
}
