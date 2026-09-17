//go:build windows

package kernel

import (
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32ResourceDLL      = windows.NewLazySystemDLL("kernel32.dll")
	psapiResourceDLL         = windows.NewLazySystemDLL("psapi.dll")
	globalMemoryStatusExProc = kernel32ResourceDLL.NewProc("GlobalMemoryStatusEx")
	getSystemTimesProc       = kernel32ResourceDLL.NewProc("GetSystemTimes")
	getProcessMemoryInfoProc = psapiResourceDLL.NewProc("GetProcessMemoryInfo")
)

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type windowsProcessMemoryCounters struct {
	Length                     uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func readPlatformResourceSnapshot(targets []resourceTarget, diskPath string) platformResourceSnapshot {
	snapshot := platformResourceSnapshot{
		sampledAt: time.Now().UTC(),
		hostCores: runtime.NumCPU(),
		processes: make(map[string]processResourceCounter, len(targets)),
	}
	snapshot.totalMemoryBytes, snapshot.availableMemoryBytes = readWindowsMemory()
	snapshot.hostTotalCPU, snapshot.hostBusyCPU = readWindowsHostCPU()
	snapshot.diskTotalBytes, snapshot.diskAvailableBytes = readWindowsDisk(diskPath)
	for _, target := range targets {
		snapshot.processes[target.kernelID] = readWindowsProcess(target.pid)
	}
	return snapshot
}

func readWindowsMemory() (*uint64, *uint64) {
	status := windowsMemoryStatusEx{Length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	result, _, _ := globalMemoryStatusExProc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 || status.TotalPhys == 0 {
		return nil, nil
	}
	total, available := status.TotalPhys, status.AvailPhys
	return uint64Pointer(total), uint64Pointer(available)
}

func readWindowsHostCPU() (uint64, uint64) {
	var idle, kernel, user windows.Filetime
	result, _, _ := getSystemTimesProc.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return 0, 0
	}
	idleValue := filetimeCounter(idle)
	total := filetimeCounter(kernel) + filetimeCounter(user)
	if idleValue > total {
		return 0, 0
	}
	return total, total - idleValue
}

func readWindowsDisk(path string) (*uint64, *uint64) {
	if strings.TrimSpace(path) == "" {
		path = os.TempDir()
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, &total, &free); err != nil || total == 0 {
		return nil, nil
	}
	return uint64Pointer(total), uint64Pointer(available)
}

func readWindowsProcess(pid int) processResourceCounter {
	counter := processResourceCounter{pid: pid}
	if pid <= 0 {
		return counter
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return counter
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return counter
	}
	memory := windowsProcessMemoryCounters{Length: uint32(unsafe.Sizeof(windowsProcessMemoryCounters{}))}
	result, _, _ := getProcessMemoryInfoProc.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&memory)),
		uintptr(memory.Length),
	)
	counter.visible = true
	counter.cpuCounter = filetimeCounter(kernel) + filetimeCounter(user)
	if result != 0 {
		counter.rssBytes = uint64(memory.WorkingSetSize)
	}
	return counter
}

func filetimeCounter(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
