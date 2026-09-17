//go:build linux

package detached

import (
	"errors"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestDetachedExecutorSeparatesDurableAndProcessLocalKernelGenerations(t *testing.T) {
	got, err := detachedExecutorWorkerGeneration("kernel-stable", 2, "kernel-stable", 1)
	if err != nil || got != 1 {
		t.Fatalf("process generation=%d err=%v", got, err)
	}
	for _, test := range []struct {
		name       string
		durableID  string
		durableGen int64
		processID  string
		processGen uint64
	}{
		{name: "different kernel", durableID: "kernel-a", durableGen: 2, processID: "kernel-b", processGen: 1},
		{name: "missing durable generation", durableID: "kernel-a", durableGen: 0, processID: "kernel-a", processGen: 1},
		{name: "missing process generation", durableID: "kernel-a", durableGen: 2, processID: "kernel-a", processGen: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := detachedExecutorWorkerGeneration(
				test.durableID, test.durableGen, test.processID, test.processGen,
			); !errors.Is(err, workspace.ErrKernelExecutionBackendConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
