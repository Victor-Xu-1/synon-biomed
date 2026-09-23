//go:build windows

package processsupervisor

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessIOCounters struct {
	readOperations   uint64
	writeOperations  uint64
	otherOperations  uint64
	readTransferred  uint64
	writeTransferred uint64
	otherTransferred uint64
}

var getProcessIOCounters = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters")

func sampleProcessActivity(rootPID int) processActivitySnapshot {
	snapshot := processActivitySnapshot{Processes: map[processActivityIdentity]processActivityCounters{}}
	if rootPID <= 0 {
		return snapshot
	}
	rootIdentity, rootCounters, ok := readWindowsProcessActivity(rootPID)
	if !ok {
		return snapshot
	}
	snapshot.Available = true
	snapshot.Processes[rootIdentity] = rootCounters
	for _, pid := range windowsProcessDescendants(rootPID) {
		identity, counters, ok := readWindowsProcessActivity(pid)
		if ok {
			snapshot.Processes[identity] = counters
		}
	}
	return snapshot
}

func readWindowsProcessActivity(pid int) (processActivityIdentity, processActivityCounters, bool) {
	if pid <= 0 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	createdTicks := windowsFiletimeTicks(created)
	if createdTicks == 0 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	var ioCounters windowsProcessIOCounters
	result, _, _ := getProcessIOCounters.Call(uintptr(handle), uintptr(unsafe.Pointer(&ioCounters)))
	var ioTotal uint64
	if result != 0 {
		ioTotal = ioCounters.readOperations + ioCounters.writeOperations + ioCounters.otherOperations +
			ioCounters.readTransferred + ioCounters.writeTransferred + ioCounters.otherTransferred
	}
	return processActivityIdentity{PID: pid, StartTicks: createdTicks}, processActivityCounters{
		CPU: windowsFiletimeTicks(kernel) + windowsFiletimeTicks(user),
		IO:  ioTotal,
	}, true
}

func windowsFiletimeTicks(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func windowsProcessDescendants(rootPID int) []int {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	children := make(map[int][]int)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil
	}
	for {
		pid, parent := int(entry.ProcessID), int(entry.ParentProcessID)
		if pid > 0 && parent > 0 && pid != parent {
			children[parent] = append(children[parent], pid)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	const maxDescendants = 4096
	queue := []int{rootPID}
	seen := map[int]bool{rootPID: true}
	result := make([]int, 0, 8)
	for len(queue) > 0 && len(result) < maxDescendants {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
			if len(result) == maxDescendants {
				break
			}
		}
	}
	return result
}
