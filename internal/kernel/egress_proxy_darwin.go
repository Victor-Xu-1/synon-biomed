//go:build darwin

package kernel

import (
	"fmt"
	"os"
	"strings"
)

// A requested egress policy requires a host broker and an OS boundary that
// denies direct sockets. Neither is available for the native macOS worker yet.
// Do not silently drop the policy and start an unrestricted interpreter.
type kernelEgressProxy struct {
	port int
}

func startKernelEgressProxy(_ string, _ string, allowed, _ []string, upstreamProxy ...string) (*kernelEgressProxy, error) {
	if len(allowed) != 0 || len(upstreamProxy) != 0 && strings.TrimSpace(upstreamProxy[0]) != "" {
		return nil, fmt.Errorf("%w: macOS kernel egress broker is unavailable", ErrConfinementUnavailable)
	}
	return nil, nil
}

func (*kernelEgressProxy) Close() {}

func (*kernelEgressProxy) takeChild() *os.File { return nil }
