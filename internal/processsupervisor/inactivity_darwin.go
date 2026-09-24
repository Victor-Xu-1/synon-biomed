//go:build darwin

package processsupervisor

import "golang.org/x/sys/unix"

// macOS exposes process start time and accumulated CPU ticks through sysctl.
// The watchdog also observes installer output; process-tree transitions keep
// silent child work alive without mistaking mere process liveness for progress.
func sampleProcessActivity(rootPID int) processActivitySnapshot {
	snapshot := processActivitySnapshot{Processes: map[processActivityIdentity]processActivityCounters{}}
	if rootPID <= 0 {
		return snapshot
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		if root, rootErr := unix.SysctlKinfoProc("kern.proc.pid", rootPID); rootErr == nil && root != nil {
			if identity, counters, ok := darwinProcessActivity(*root); ok {
				snapshot.Available = true
				snapshot.Processes[identity] = counters
			}
		}
		return snapshot
	}
	indices := make(map[int]int, len(processes))
	children := make(map[int][]int)
	for index := range processes {
		pid := int(processes[index].Proc.P_pid)
		parent := int(processes[index].Eproc.Ppid)
		if pid <= 0 {
			continue
		}
		indices[pid] = index
		if parent > 0 && parent != pid {
			children[parent] = append(children[parent], pid)
		}
	}
	rootIndex, ok := indices[rootPID]
	if !ok {
		return snapshot
	}
	rootIdentity, rootCounters, ok := darwinProcessActivity(processes[rootIndex])
	if !ok {
		return snapshot
	}
	snapshot.Available = true
	snapshot.Processes[rootIdentity] = rootCounters
	const maxDescendants = 4096
	queue := []int{rootPID}
	seen := map[int]bool{rootPID: true}
	count := 0
	for len(queue) > 0 && count < maxDescendants {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			count++
			queue = append(queue, child)
			if index, exists := indices[child]; exists {
				if identity, counters, valid := darwinProcessActivity(processes[index]); valid {
					snapshot.Processes[identity] = counters
				}
			}
			if count == maxDescendants {
				break
			}
		}
	}
	return snapshot
}

func darwinProcessActivity(process unix.KinfoProc) (processActivityIdentity, processActivityCounters, bool) {
	pid := int(process.Proc.P_pid)
	started := process.Proc.P_starttime
	if pid <= 0 || started.Sec <= 0 || started.Usec < 0 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	return processActivityIdentity{
			PID: pid, StartTicks: uint64(started.Sec)*1_000_000 + uint64(started.Usec),
		}, processActivityCounters{
			CPU: process.Proc.P_uticks + process.Proc.P_sticks + process.Proc.P_iticks,
		}, true
}
