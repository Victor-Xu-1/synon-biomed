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

func startKernelEgressProxy(_ string, _ string, allowed, denied []string, upstreamProxy ...string) (*kernelEgressProxy, error) {
	policyRequested := len(allowed) != 0 || len(denied) != 0
	for _, upstream := range upstreamProxy {
		policyRequested = policyRequested || strings.TrimSpace(upstream) != ""
	}
	if policyRequested {
		return nil, fmt.Errorf("%w: macOS kernel egress broker is unavailable", ErrConfinementUnavailable)
	}
	return nil, nil
}

func (*kernelEgressProxy) Close() {}

func (*kernelEgressProxy) takeChild() *os.File { return nil }
