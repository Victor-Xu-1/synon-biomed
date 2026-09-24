//go:build windows

package kernel

import (
	"fmt"
	"os"
	"strings"
)

type kernelEgressProxy struct {
	port int
}

func startKernelEgressProxy(_ string, _ string, allowed, _ []string, upstreamProxy ...string) (*kernelEgressProxy, error) {
	if len(allowed) > 0 || len(upstreamProxy) > 0 && strings.TrimSpace(upstreamProxy[0]) != "" {
		return nil, fmt.Errorf("%w: Windows kernel egress broker is unavailable", ErrConfinementUnavailable)
	}
	return nil, nil
}

func (*kernelEgressProxy) Close() {}

func (*kernelEgressProxy) takeChild() *os.File { return nil }
