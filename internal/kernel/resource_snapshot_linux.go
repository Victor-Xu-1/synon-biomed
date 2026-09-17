//go:build linux

package kernel

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func readPlatformResourceSnapshot(targets []resourceTarget, diskPath string) platformResourceSnapshot {
	snapshot := platformResourceSnapshot{
		sampledAt: time.Now().UTC(),
		hostCores: runtime.NumCPU(),
		processes: make(map[string]processResourceCounter, len(targets)),
	}
	snapshot.totalMemoryBytes, snapshot.availableMemoryBytes = readLinuxMemory()
	snapshot.hostTotalCPU, snapshot.hostBusyCPU = readLinuxHostCPU()
	snapshot.cgroupCPUUsec = readLinuxCgroupCPUUsage()
	snapshot.diskTotalBytes, snapshot.diskAvailableBytes = readLinuxDisk(diskPath)
	for _, target := range targets {
		snapshot.processes[target.kernelID] = readLinuxProcessTree(target.pid, target.pidStartTicks)
	}
	return snapshot
}

func readLinuxCgroupCPUUsage() *uint64 {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	var relative string
	for _, line := range strings.Split(string(membership), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative = strings.TrimSpace(strings.TrimPrefix(line, "0::"))
			break
		}
	}
	if relative == "" {
		return nil
	}
	clean := filepath.Clean("/" + strings.TrimPrefix(relative, "/"))
	root := filepath.Clean("/sys/fs/cgroup")
	statPath := filepath.Join(root, strings.TrimPrefix(clean, "/"), "cpu.stat")
	if statPath != filepath.Join(root, "cpu.stat") && !strings.HasPrefix(statPath, root+string(filepath.Separator)) {
		return nil
	}
	raw, err := os.ReadFile(statPath)
	if err != nil {
		return nil
	}
	return parseLinuxCgroupCPUUsage(raw)
}

func parseLinuxCgroupCPUUsage(raw []byte) *uint64 {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "usage_usec" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil
		}
		return uint64Pointer(value)
	}
	return nil
}

func readLinuxMemory() (*uint64, *uint64) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, nil
	}
	var total, available uint64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > math.MaxUint64/1024 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	var totalPtr, availablePtr *uint64
	if total > 0 {
		totalPtr = uint64Pointer(total)
	}
	if available > 0 {
		availablePtr = uint64Pointer(available)
	}
	return totalPtr, availablePtr
}

func readLinuxHostCPU() (uint64, uint64) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0
		}
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	if idle > total {
		return 0, 0
	}
	return total, total - idle
}

func readLinuxDisk(path string) (*uint64, *uint64) {
	if strings.TrimSpace(path) == "" {
		path = os.TempDir()
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil || stat.Bsize <= 0 {
		return nil, nil
	}
	blockSize := uint64(stat.Bsize)
	if uint64(stat.Blocks) > math.MaxUint64/blockSize || uint64(stat.Bavail) > math.MaxUint64/blockSize {
		return nil, nil
	}
	total := uint64(stat.Blocks) * blockSize
	available := uint64(stat.Bavail) * blockSize
	return uint64Pointer(total), uint64Pointer(available)
}

func readLinuxProcessTree(pid int, expectedStartTicks uint64) processResourceCounter {
	return readLinuxProcessTreeAt("/proc", pid, expectedStartTicks)
}
