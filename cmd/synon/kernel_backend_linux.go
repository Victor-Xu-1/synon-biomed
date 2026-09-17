//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"synon-go/internal/config"
	kernelruntime "synon-go/internal/kernel"
	kerneldetached "synon-go/internal/kernel/detached"
	workspace "synon-go/internal/persistence/workspace"
)

// Keep the platform-specific supervisor behind a compile-time boundary. A
// runtime.GOOS condition still requires Linux-only symbols to type-check on
// Windows, even when that branch cannot execute there.
func newKernelExecutionBackend(cfg config.Config, store *workspace.Store) (kernelruntime.ExecutionBackend, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve detached kernel executor: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve detached kernel executor path: %w", err)
	}
	socketRoot, err := kerneldetached.DefaultSharedSocketRoot(cfg.HomeDir)
	if err != nil {
		return nil, fmt.Errorf("resolve detached kernel socket root: %w", err)
	}
	launcher, err := kerneldetached.NewSystemdUserExecutorLauncher()
	if err != nil {
		return nil, fmt.Errorf("resolve detached kernel executor supervisor: %w", err)
	}
	return &kerneldetached.Backend{
		Store: store, Executable: executable, HomeDir: cfg.HomeDir,
		CondaHome: cfg.CondaHome, CondaEnvsPath: cfg.CondaEnvsPath,
		SocketRoot: socketRoot,
		LogDir:     filepath.Join(cfg.HomeDir, "runtime", "kernel-executors", "logs"),
		Launcher:   launcher,
	}, nil
}
