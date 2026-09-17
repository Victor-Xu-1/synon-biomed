//go:build !linux

package main

import (
	"synon-go/internal/config"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func newKernelExecutionBackend(_ config.Config, _ *workspace.Store) (kernelruntime.ExecutionBackend, error) {
	// A nil interface selects the existing in-process kernel manager. Do not
	// return a typed nil: that would incorrectly enable detached dispatch.
	return nil, nil
}
