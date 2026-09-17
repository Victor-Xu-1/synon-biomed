//go:build !windows

package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func configureWorkerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

type workerProcess struct {
	command *exec.Cmd
}

func startWorkerProcess(command *exec.Cmd) (*workerProcess, error) {
	configureWorkerProcess(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &workerProcess{command: command}
	command.Cancel = process.kill
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

func (process *workerProcess) signal(signal os.Signal) error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	value, ok := signal.(syscall.Signal)
	if !ok {
		return process.command.Process.Signal(signal)
	}
	var firstErr error
	if runtime.GOOS == "linux" {
		descendants := linuxProcessDescendants(process.command.Process.Pid)
		confined := kernelProcessName(process.command.Process.Pid) == "bwrap"
		for index := len(descendants) - 1; index >= 0; index-- {
			if confined && value != syscall.SIGKILL && kernelProcessName(descendants[index]) == "bwrap" {
				continue
			}
			if err := syscall.Kill(descendants[index], value); err != nil && err != syscall.ESRCH && firstErr == nil {
				firstErr = err
			}
		}
		if confined && value != syscall.SIGKILL {
			// The sandbox supervisor (bubblewrap) has no SIGINT handler and
			// would tear the whole sandbox down before the worker can report
			// the interrupt. Deliver directly to the worker processes only.
			return firstErr
		}
	}
	// Descendants can move into their own process groups or PID namespaces,
	// so Linux receives a direct deepest-first signal above. The worker root
	// and every child that stayed in its group must still receive the group
	// signal; returning after the descendant walk left the main kernel alive.
	if err := syscall.Kill(-process.command.Process.Pid, value); err != nil && err != syscall.ESRCH && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func kernelProcessName(pid int) string {
	comm, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(comm))
}

func (process *workerProcess) kill() error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	if runtime.GOOS == "linux" {
		descendants := linuxProcessDescendants(process.command.Process.Pid)
		for index := len(descendants) - 1; index >= 0; index-- {
			_ = syscall.Kill(descendants[index], syscall.SIGKILL)
		}
	}
	if err := syscall.Kill(-process.command.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// killWorkerProcess keeps Linux confinement probes on the same process-group
// termination primitive without constructing a long-lived Worker.
func killWorkerProcess(command *exec.Cmd) error {
	return (&workerProcess{command: command}).kill()
}

func (*workerProcess) close() {}

func unexpectedKernelStopReason(command *exec.Cmd, waitErr error) string {
	if command != nil && command.ProcessState != nil {
		if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				if status.Signal() == syscall.SIGKILL {
					return "was killed — possibly out of memory"
				}
				return "was killed by " + unixKernelSignalName(status.Signal())
			}
			if code := status.ExitStatus(); code == 0 {
				return "exited unexpectedly (exit code 0)"
			} else if code == 128+int(syscall.SIGKILL) {
				// Bubblewrap and other process supervisors may translate the
				// worker's SIGKILL into the conventional shell exit code 137.
				// Preserve the same resource diagnostic as a directly signalled
				// process so settlement can recover with a smaller working set.
				return "was killed — possibly out of memory"
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

func linuxProcessDescendants(root int) []int {
	result := []int{}
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + strconv.Itoa(parent) + "/children")
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			child, err := strconv.Atoi(field)
			if err != nil || child <= 0 || seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result
}
