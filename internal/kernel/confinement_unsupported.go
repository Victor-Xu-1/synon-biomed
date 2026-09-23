//go:build !linux || !amd64

package kernel

import (
	"fmt"
	"io"
	"os"
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

func replaceKernelDirectory(string, string, bool) error {
	return ErrConfinementUnavailable
}

func CopyProviderOperationFile(string, string, io.Writer, int64) (int64, error) {
	return 0, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}
