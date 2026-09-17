//go:build !linux

package kernel

import (
	"fmt"
	"os"
	"runtime"
)

func newProviderRuntimeSocketPair(string) (*os.File, *os.File, error) {
	return nil, nil, fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}

func providerHostNetworkNamespaceInode() (string, error) {
	return "", fmt.Errorf("%w on %s/%s", ErrConfinementUnavailable, runtime.GOOS, runtime.GOARCH)
}
