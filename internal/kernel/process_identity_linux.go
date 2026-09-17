//go:build linux

package kernel

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	startTicks, err := linuxProcessStartTicks(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 {
		return ProcessIdentity{}, errors.New("kernel worker process group identity is unavailable")
	}
	return ProcessIdentity{PID: pid, StartTicks: startTicks, PGID: pgid}, nil
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
	current, err := linuxProcessStartTicks(int(pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return current == startTicks, nil
}

func linuxProcessStartTicks(pid int) (int64, error) {
	if pid <= 0 {
		return 0, errors.New("positive process id is required")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("read process identity: %w", err)
	}
	// comm is parenthesized and may itself contain spaces or parentheses. The
	// last ') ' delimiter is the only stable boundary before field 3.
	boundary := strings.LastIndex(string(raw), ") ")
	if boundary < 0 {
		return 0, errors.New("process identity is malformed")
	}
	fields := strings.Fields(string(raw[boundary+2:]))
	// starttime is field 22; fields[0] is field 3 after removing pid and comm.
	if len(fields) <= 19 {
		return 0, errors.New("process identity is incomplete")
	}
	value, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("process start identity is invalid")
	}
	return value, nil
}
