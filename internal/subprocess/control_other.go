//go:build !unix && !windows

package subprocess

import (
	"os"
	"os/exec"
)

type Control struct{}

func Prepare(command *exec.Cmd) (*Control, error) {
	control := &Control{}
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
	return command.Process.Kill()
}

func (*Control) Close() error {
	return nil
}
