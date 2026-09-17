//go:build windows

package server

import (
	"os/exec"

	"synon-go/internal/processsupervisor"
)

type runnerProcess struct {
	job *processsupervisor.Job
}

func startRunnerProcess(cmd *exec.Cmd) (*runnerProcess, error) {
	job, err := processsupervisor.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &runnerProcess{job: job}, nil
}

func (process *runnerProcess) kill() error {
	if process == nil || process.job == nil {
		return nil
	}
	return process.job.Terminate()
}

func (process *runnerProcess) close() {
	if process != nil && process.job != nil {
		_ = process.job.Close()
	}
}
