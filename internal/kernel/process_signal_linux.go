//go:build linux

package kernel

import (
	"errors"
	"os"
	"syscall"
)

func (process *workerProcess) signalLinuxTree(signal syscall.Signal) error {
	root := process.command.Process
	if err := root.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	// Observation budgets must not truncate cancellation: all currently owned
	// descendants remain targets, including trees larger than the display limit.
	members, _, err := readLinuxProcessTreeMembersAt("/proc", root.Pid, process.startTicks, int(^uint(0)>>1))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	confined := members[0].process.Name == "bwrap"
	supervisors := linuxSandboxSupervisors(members)
	var failure error
	for index := len(members) - 1; index > 0; index-- {
		member := members[index]
		// Interrupt the interpreter, not its sandbox supervisors. The existing
		// grace/kill lifecycle owns escalation if the interpreter cannot settle.
		if confined && signal != syscall.SIGKILL && supervisors[member.process.PID] {
			continue
		}
		if err := signalLinuxProcessMember(member, signal); err != nil {
			failure = errors.Join(failure, err)
		}
	}
	if confined && signal != syscall.SIGKILL {
		return failure
	}
	// The root may have exited while descendants were settling. Revalidate the
	// captured identity before addressing its process group, including late calls
	// after Wait. Never recapture ownership from a recycled numeric PID.
	alive, err := ProcessIdentityAlive(int64(root.Pid), int64(members[0].start))
	if err != nil {
		return errors.Join(failure, err)
	}
	if !alive {
		return failure
	}
	if err := syscall.Kill(-root.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		failure = errors.Join(failure, err)
	}
	return failure
}

// Only the contiguous sandbox-launcher ancestry is privileged. A computation
// with the same executable name below the interpreter is still cancellable.
func linuxSandboxSupervisors(members []linuxObservedCounter) map[int]bool {
	result := map[int]bool{}
	for index, member := range members {
		if member.process.Name == "bwrap" && (index == 0 || result[member.process.ParentPID]) {
			result[member.process.PID] = true
		}
	}
	return result
}

func signalLinuxProcessMember(member linuxObservedCounter, signal syscall.Signal) error {
	// Go's Linux process handle pins the process with a pidfd on supported
	// kernels. Check the sampled identity after opening that handle, so a PID
	// reused between enumeration and opening is never signalled as our child.
	handle, err := os.FindProcess(member.process.PID)
	if err != nil {
		return err
	}
	defer handle.Release()
	current, err := readLinuxObservedCounter("/proc", member.process.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.start != member.start || !linuxProcessStateLive(current.process.State) {
		return nil
	}
	err = handle.Signal(signal)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
