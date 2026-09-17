//go:build windows

package shellops

import (
	"os/exec"

	"synon-go/internal/processsupervisor"
)

type sandboxProcess struct {
	job *processsupervisor.Job
}

func startSandboxProcess(command *exec.Cmd) (*sandboxProcess, error) {
	job, err := processsupervisor.Start(command)
	if err != nil {
		return nil, err
	}
	return &sandboxProcess{job: job}, nil
}

func (process *sandboxProcess) kill() error {
	if process == nil || process.job == nil {
		return nil
	}
	return process.job.Terminate()
}

func (process *sandboxProcess) close() {
	if process != nil && process.job != nil {
		_ = process.job.Close()
	}
}
