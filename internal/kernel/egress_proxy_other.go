//go:build !linux

package kernel

import "os"

type kernelEgressProxy struct {
	port int
}

func startKernelEgressProxy(_ string, _ string, _ []string, _ []string, _ ...string) (*kernelEgressProxy, error) {
	return nil, nil
}

func (p *kernelEgressProxy) Close() {}

func (p *kernelEgressProxy) takeChild() *os.File { return nil }
