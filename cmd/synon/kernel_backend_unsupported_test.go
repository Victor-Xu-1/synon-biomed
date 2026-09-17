//go:build !linux

package main

import (
	"testing"

	"synon-go/internal/config"
)

func TestKernelBackendUsesNativeInProcessManagerOutsideLinux(t *testing.T) {
	backend, err := newKernelExecutionBackend(config.Config{HomeDir: t.TempDir()}, nil)
	if err != nil || backend != nil {
		t.Fatalf("native platform must select its existing in-process manager: backend=%#v err=%v", backend, err)
	}
}
