//go:build linux

package kernel

import "synon-go/internal/processsupervisor"

const processObservationLimit = 64

func readLinuxProcessTreeAt(root string, pid int, expectedStart uint64) processResourceCounter {
	result := processResourceCounter{pid: pid}
	members, partial, err := readLinuxProcessTreeMembersAt(root, pid, expectedStart, processWalkLimit)
	if err != nil || len(members) == 0 {
		return result
	}
	result.visible, result.startIdentity = true, members[0].start
	result.observationPartial = partial
	for _, member := range members {
		result.rssBytes += member.rss
		result.cpuCounter += member.cpu
		if len(result.observedProcesses) < processObservationLimit {
			result.observedProcesses = append(result.observedProcesses, member.process)
		} else {
			result.observationPartial = true
		}
	}
	if root == "/proc" {
		sample, err := processsupervisor.SampleMemoryPressure(pid)
		start, identityErr := linuxProcessStartTicks(pid)
		if identityErr == nil && uint64(start) == result.startIdentity {
			if err != nil {
				sample.MemoryPressure = processsupervisor.MemoryPressure{Status: "unavailable"}
			}
			result.memoryPressure = &sample.MemoryPressure
		}
	}
	return result
}
