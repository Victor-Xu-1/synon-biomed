//go:build linux

package kernel

import (
	"os"
	"testing"
	"time"
)

func TestLinuxResourceSnapshotReportsRealHostAndKernelProcess(t *testing.T) {
	snapshot := readPlatformResourceSnapshot(
		[]resourceTarget{{kernelID: "self", pid: os.Getpid()}},
		t.TempDir(),
	)
	if snapshot.sampledAt.IsZero() || snapshot.hostCores < 1 {
		t.Fatalf("machine sample = %#v", snapshot)
	}
	if snapshot.totalMemoryBytes == nil || *snapshot.totalMemoryBytes == 0 ||
		snapshot.availableMemoryBytes == nil || *snapshot.availableMemoryBytes == 0 {
		t.Fatalf("memory sample = total=%v available=%v", snapshot.totalMemoryBytes, snapshot.availableMemoryBytes)
	}
	if snapshot.hostTotalCPU == 0 || snapshot.hostBusyCPU > snapshot.hostTotalCPU {
		t.Fatalf("cpu counters total=%d busy=%d", snapshot.hostTotalCPU, snapshot.hostBusyCPU)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil && snapshot.cgroupCPUUsec == nil {
		t.Fatal("cgroup v2 CPU usage is available but was not sampled")
	}
	if snapshot.diskTotalBytes == nil || snapshot.diskAvailableBytes == nil ||
		*snapshot.diskTotalBytes < *snapshot.diskAvailableBytes {
		t.Fatalf("disk sample = total=%v available=%v", snapshot.diskTotalBytes, snapshot.diskAvailableBytes)
	}
	process := snapshot.processes["self"]
	if !process.visible || process.rssBytes == 0 {
		t.Fatalf("process sample = %#v", process)
	}
}

func TestResourceSamplerProducesSecondSampleCPUPercentages(t *testing.T) {
	sampler := newResourceSampler()
	targets := []resourceTarget{{kernelID: "self", pid: os.Getpid()}}
	firstMachine, firstProcesses := sampler.sample(targets, t.TempDir())
	if firstMachine.TotalCPUPct != nil || firstProcesses["self"].CPUPct != nil {
		t.Fatalf("first CPU sample must wait for a delta: machine=%v process=%v", firstMachine.TotalCPUPct, firstProcesses["self"].CPUPct)
	}
	deadline := time.Now().Add(75 * time.Millisecond)
	for time.Now().Before(deadline) {
		for spin := 0; spin < 100_000; spin++ {
			_ = spin * spin
		}
	}
	secondMachine, secondProcesses := sampler.sample(targets, t.TempDir())
	if secondMachine.TotalCPUPct == nil || *secondMachine.TotalCPUPct < 0 {
		t.Fatalf("second machine CPU sample = %v", secondMachine.TotalCPUPct)
	}
	if firstMachine.CgroupCPUPct == nil && readLinuxCgroupCPUUsage() != nil &&
		(secondMachine.CgroupCPUPct == nil || *secondMachine.CgroupCPUPct < 0) {
		t.Fatalf("second cgroup CPU sample = %v", secondMachine.CgroupCPUPct)
	}
	if secondProcesses["self"].CPUPct == nil || *secondProcesses["self"].CPUPct < 0 {
		t.Fatalf("second process CPU sample = %v", secondProcesses["self"].CPUPct)
	}
}

func TestParseLinuxCgroupCPUUsage(t *testing.T) {
	usage := parseLinuxCgroupCPUUsage([]byte("user_usec 3\nusage_usec 42\nsystem_usec 7\n"))
	if usage == nil || *usage != 42 {
		t.Fatalf("usage = %v", usage)
	}
	for _, raw := range [][]byte{
		[]byte("user_usec 3\n"),
		[]byte("usage_usec nope\n"),
		[]byte("usage_usec 1 2\n"),
	} {
		if got := parseLinuxCgroupCPUUsage(raw); got != nil {
			t.Fatalf("invalid usage %q = %v", raw, *got)
		}
	}
}

func TestLinuxResourceSnapshotRejectsReusedPIDIdentity(t *testing.T) {
	startTicks, err := CurrentProcessStartTicks()
	if err != nil {
		t.Fatal(err)
	}
	targets := []resourceTarget{{
		kernelID: "self", pid: os.Getpid(), pidStartTicks: uint64(startTicks + 1),
	}}
	snapshot := readPlatformResourceSnapshot(targets, t.TempDir())
	if process := snapshot.processes["self"]; process.visible || process.rssBytes != 0 {
		t.Fatalf("reused PID was attributed to kernel: %#v", process)
	}
}
