//go:build !windows && !linux

package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configureWorkerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type workerProcess struct {
	command *exec.Cmd
}

func startWorkerProcess(command *exec.Cmd, directories ...string) (*workerProcess, error) {
	configureWorkerProcess(command)
	release, err := configureWorkerResourceDomain(command, directories)
	if err != nil {
		return nil, err
	}
	defer release()
	process := &workerProcess{command: command}
	publish := bindWorkerProcessCancellation(command, process.kill)
	defer publish()
	if err := command.Start(); err != nil {
		return nil, err
	}
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

// A process-group signal does not contain descendants that deliberately start
// a new session. Native kernel execution remains disabled until a separate
// child-lifetime boundary and its macOS regression tests are established.
func killWorkerProcess(command *exec.Cmd) error {
	return (&workerProcess{command: command}).kill()
}

func (process *workerProcess) signal(signal os.Signal) error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	value, ok := signal.(syscall.Signal)
	if !ok {
		return process.command.Process.Signal(signal)
	}
	if err := syscall.Kill(-process.command.Process.Pid, value); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func (process *workerProcess) kill() error {
	return process.signal(syscall.SIGKILL)
}

func (process *workerProcess) close() {}

func unexpectedKernelStopReason(command *exec.Cmd, waitErr error) string {
	if command != nil && command.ProcessState != nil {
		if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return "was killed by " + unixKernelSignalName(status.Signal())
			}
			if code := status.ExitStatus(); code == 0 {
				return "exited unexpectedly (exit code 0)"
			} else if code > 0 {
				return fmt.Sprintf("exited unexpectedly with exit code %d", code)
			}
		}
	}
	if waitErr != nil {
		return "exited unexpectedly"
	}
	return "exited unexpectedly (exit code 0)"
}

func unixKernelSignalName(signal syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
		syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
		syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL",
		syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
		syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	}
	if name := names[signal]; name != "" {
		return name
	}
	return fmt.Sprintf("signal %d", signal)
}
