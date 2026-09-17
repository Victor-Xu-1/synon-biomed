//go:build !linux && !windows

package shellops

import "os/exec"

type sandboxProcess struct {
	command *exec.Cmd
}

func startSandboxProcess(command *exec.Cmd) (*sandboxProcess, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &sandboxProcess{command: command}, nil
}

func (process *sandboxProcess) kill() error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	return process.command.Process.Kill()
}

func (*sandboxProcess) close() {}
