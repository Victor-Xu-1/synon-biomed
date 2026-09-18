//go:build windows

package kernel

import (
	"fmt"
	"os"
	"os/exec"

	"synon-go/internal/processsupervisor"
)

func configureWorkerProcess(_ *exec.Cmd) {}

type workerProcess struct {
	command *exec.Cmd
	job     *processsupervisor.Job
}

func startWorkerProcess(command *exec.Cmd, directories ...string) (*workerProcess, error) {
	release, err := configureWorkerResourceDomain(command, directories)
	if err != nil {
		return nil, err
	}
	defer release()
	job, err := processsupervisor.Start(command)
	if err != nil {
		return nil, err
	}
	process := &workerProcess{command: command, job: job}
	// The job supervisor already bound cancellation before starting the child.
	// Keep that authority instead of racing exec.Cmd's context watcher here.
	return process, nil
}

func runWorkerProcess(command *exec.Cmd) error {
	process, err := startWorkerProcess(command)
	if err != nil {
		return err
	}
	defer process.close()
	return command.Wait()
}

func killWorkerProcess(command *exec.Cmd) error {
	// This callback also runs if cancellation precedes Job assignment. The
	// supervisor owns descendant termination; stop the new root in that window.
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}

func (process *workerProcess) signal(signal os.Signal) error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	return process.command.Process.Signal(signal)
}

func (process *workerProcess) kill() error {
	if process == nil || process.job == nil {
		return nil
	}
	return process.job.Terminate()
}

func (process *workerProcess) close() {
	if process != nil && process.job != nil {
		_ = process.job.Close()
	}
}

func unexpectedKernelStopReason(command *exec.Cmd, waitErr error) string {
	if command != nil && command.ProcessState != nil {
		if code := command.ProcessState.ExitCode(); code == 0 {
			return "exited unexpectedly (exit code 0)"
		} else if code > 0 {
			return fmt.Sprintf("exited unexpectedly with exit code %d", code)
		}
	}
	if waitErr != nil {
		return "exited unexpectedly"
	}
	return "exited unexpectedly (exit code 0)"
}
