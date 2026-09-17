//go:build unix

package subprocess

import (
	"os"
	"os/exec"
	"syscall"
)

type Control struct{}

func Prepare(command *exec.Cmd) (*Control, error) {
	control := &Control{}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return control.Kill(command)
	}
	return control, nil
}

func (*Control) Attach(*exec.Cmd) error {
	return nil
}

func (*Control) Kill(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (*Control) Close() error {
	return nil
}
