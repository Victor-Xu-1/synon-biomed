//go:build !linux || !amd64

package kernel

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

func freezeInternalKernelDirectory(string) (*os.File, error) {
	return nil, ErrConfinementUnavailable
}

func secureROperationLog(string) (string, *os.File, error) {
	return "", nil, ErrConfinementUnavailable
}

func readBoundedKernelMetadataFile(string, string, int) ([]byte, bool, error) {
	return nil, false, ErrConfinementUnavailable
}

func lockKernelFile(context.Context, string) (func(), error) {
	return nil, ErrConfinementUnavailable
}

func replaceKernelDirectory(string, string, bool) error {
	return ErrConfinementUnavailable
}

func newConfinedWorkerCommand(string, string, []string, []string, []WorkerMount, []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func newConfinedWorkerCommandWithAuxiliary(string, string, []string, []string, []WorkerMount, []string, []*os.File) (*exec.Cmd, error) {
	return nil, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func CopyProviderOperationFile(string, string, io.Writer, int64) (int64, error) {
	return 0, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func platformConfinementEvidence() ConfinementEvidence {
	return ConfinementEvidence{
		Available: false, Mode: "unavailable", Reason: "Synon kernel confinement is unavailable on this platform",
	}
}

// This platform has no local managed-kernel namespace. Host file permissions
// retain their existing validation; kernel execution itself stays unavailable.
func platformHostMountPathProtected(string) bool { return false }

func probePlatformConfinement() ConfinementEvidence {
	return platformConfinementEvidence()
}
