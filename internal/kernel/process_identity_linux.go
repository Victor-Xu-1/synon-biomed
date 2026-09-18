//go:build linux

package kernel

import (
	"errors"
	"os"
	"syscall"
)

// ProcessIdentity is the immutable operating-system identity of a kernel
// worker. PID alone is not a safe restart fence because Linux may reuse it.
type ProcessIdentity struct {
	PID        int
	StartTicks int64
	PGID       int
}

func (w *Worker) ProcessIdentity() (ProcessIdentity, error) {
	if w == nil || w.process == nil || w.process.command == nil || w.process.command.Process == nil {
		return ProcessIdentity{}, errors.New("kernel worker process identity is unavailable")
	}
	pid := w.process.command.Process.Pid
	current, err := readLinuxObservedCounter("/proc", pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	if !linuxProcessStateLive(current.process.State) || current.start > 1<<63-1 {
		return ProcessIdentity{}, errors.New("kernel worker process is no longer live")
	}
	if w.process.startTicks != 0 && current.start != w.process.startTicks {
		return ProcessIdentity{}, errors.New("kernel worker process identity no longer matches its launch")
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 {
		return ProcessIdentity{}, errors.New("kernel worker process group identity is unavailable")
	}
	return ProcessIdentity{PID: pid, StartTicks: int64(current.start), PGID: pgid}, nil
}

func CurrentProcessStartTicks() (int64, error) {
	return linuxProcessStartTicks(os.Getpid())
}

// ProcessIdentityAlive reports whether pid still names the exact Linux
// process recorded by startTicks. PID alone is not sufficient because the
// kernel may reuse it after an executor exits.
func ProcessIdentityAlive(pid int64, startTicks int64) (bool, error) {
	if pid <= 0 || startTicks <= 0 || pid > int64(^uint(0)>>1) {
		return false, errors.New("complete process identity is required")
	}
	current, err := readLinuxObservedCounter("/proc", int(pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return current.start == uint64(startTicks) && linuxProcessStateLive(current.process.State), nil
}

func linuxProcessStartTicks(pid int) (int64, error) {
	if pid <= 0 {
		return 0, errors.New("positive process id is required")
	}
	current, err := readLinuxObservedCounter("/proc", pid)
	if err != nil {
		return 0, err
	}
	if current.start > 1<<63-1 {
		return 0, errors.New("process start identity is invalid")
	}
	return int64(current.start), nil
}
