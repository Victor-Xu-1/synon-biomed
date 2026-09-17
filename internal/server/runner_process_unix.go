//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

type runnerProcess struct {
	cmd *exec.Cmd
}

func startRunnerProcess(cmd *exec.Cmd) (*runnerProcess, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &runnerProcess{cmd: cmd}, nil
}

func (process *runnerProcess) kill() error {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-process.cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func (*runnerProcess) close() {}
