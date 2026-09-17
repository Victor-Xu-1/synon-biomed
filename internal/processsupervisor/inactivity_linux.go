//go:build linux

package processsupervisor

import (
	"os"
	"strconv"
	"strings"
)

func sampleProcessActivity(rootPID int) processActivitySnapshot {
	snapshot := processActivitySnapshot{Processes: map[processActivityIdentity]processActivityCounters{}}
	if rootPID <= 0 {
		return snapshot
	}
	pids := append([]int{rootPID}, linuxProcessDescendants(rootPID)...)
	for _, pid := range pids {
		identity, counters, ok := readLinuxProcessActivity(pid)
		if !ok {
			continue
		}
		if pid == rootPID {
			snapshot.Available = true
		}
		snapshot.Processes[identity] = counters
	}
	return snapshot
}

func readLinuxProcessActivity(pid int) (processActivityIdentity, processActivityCounters, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	boundary := strings.LastIndex(string(raw), ") ")
	if boundary < 0 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	fields := strings.Fields(string(raw[boundary+2:]))
	if len(fields) <= 19 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	userTicks, userErr := strconv.ParseUint(fields[11], 10, 64)
	systemTicks, systemErr := strconv.ParseUint(fields[12], 10, 64)
	startTicks, startErr := strconv.ParseUint(fields[19], 10, 64)
	if userErr != nil || systemErr != nil || startErr != nil || startTicks == 0 {
		return processActivityIdentity{}, processActivityCounters{}, false
	}
	return processActivityIdentity{PID: pid, StartTicks: startTicks}, processActivityCounters{
		CPU: userTicks + systemTicks,
		IO:  readLinuxProcessIO(pid),
	}, true
}

func readLinuxProcessIO(pid int) uint64 {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0
	}
	var total uint64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "rchar", "wchar", "read_bytes", "write_bytes":
			if value, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
				total += value
			}
		}
	}
	return total
}

func linuxProcessDescendants(root int) []int {
	result := []int{}
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		tasks, err := os.ReadDir("/proc/" + strconv.Itoa(parent) + "/task")
		if err != nil {
			continue
		}
		for _, task := range tasks {
			tid, parseErr := strconv.Atoi(task.Name())
			if parseErr != nil || tid <= 0 {
				continue
			}
			raw, readErr := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + strconv.Itoa(tid) + "/children")
			if readErr != nil {
				continue
			}
			for _, field := range strings.Fields(string(raw)) {
				child, childErr := strconv.Atoi(field)
				if childErr != nil || child <= 0 || seen[child] {
					continue
				}
				seen[child] = true
				result = append(result, child)
				queue = append(queue, child)
			}
		}
	}
	return result
}
