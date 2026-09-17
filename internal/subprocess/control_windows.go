//go:build windows

package subprocess

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Control struct {
	mu     sync.Mutex
	job    windows.Handle
	closed bool
}

func Prepare(command *exec.Cmd) (*Control, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	control := &Control{job: job}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	command.Cancel = func() error {
		return control.Kill(command)
	}
	return control, nil
}

func (control *Control) Attach(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.closed {
		return os.ErrProcessDone
	}
	return windows.AssignProcessToJobObject(control.job, handle)
}

func (control *Control) Kill(command *exec.Cmd) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.closed {
		return os.ErrProcessDone
	}
	if err := windows.TerminateJobObject(control.job, 1); err == nil {
		return nil
	}
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	if err := command.Process.Kill(); err != nil {
		if command.ProcessState != nil && command.ProcessState.Exited() {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (control *Control) Close() error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.closed {
		return nil
	}
	control.closed = true
	return windows.CloseHandle(control.job)
}
