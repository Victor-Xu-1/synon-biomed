//go:build !linux && !windows

package kernel

import (
	"runtime"
	"time"
)

func readPlatformResourceSnapshot(targets []resourceTarget, _ string) platformResourceSnapshot {
	processes := make(map[string]processResourceCounter, len(targets))
	for _, target := range targets {
		processes[target.kernelID] = processResourceCounter{pid: target.pid}
	}
	return platformResourceSnapshot{
		sampledAt: time.Now().UTC(),
		hostCores: runtime.NumCPU(),
		processes: processes,
	}
}
