//go:build windows

package processsupervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job owns a Windows Job Object containing one supervised command and every
// descendant process it creates. Closing or terminating the job cannot leave
// descendants running after their parent exits.
type Job struct {
	mu     sync.Mutex
	handle windows.Handle
}

type windowsProcessAPI struct {
	createJobObject          func(*windows.SecurityAttributes, *uint16) (windows.Handle, error)
	setInformationJobObject  func(windows.Handle, uint32, uintptr, uint32) (int, error)
	openProcess              func(uint32, bool, uint32) (windows.Handle, error)
	assignProcessToJobObject func(windows.Handle, windows.Handle) error
	terminateProcess         func(windows.Handle, uint32) error
	createThreadSnapshot     func(uint32, uint32) (windows.Handle, error)
	threadFirst              func(windows.Handle, *windows.ThreadEntry32) error
	threadNext               func(windows.Handle, *windows.ThreadEntry32) error
	openThread               func(uint32, bool, uint32) (windows.Handle, error)
	resumeThread             func(windows.Handle) (uint32, error)
	waitForSingleObject      func(windows.Handle, uint32) (uint32, error)
}

var systemWindowsProcessAPI = windowsProcessAPI{
	createJobObject:          windows.CreateJobObject,
	setInformationJobObject:  windows.SetInformationJobObject,
	openProcess:              windows.OpenProcess,
	assignProcessToJobObject: windows.AssignProcessToJobObject,
	terminateProcess:         windows.TerminateProcess,
	createThreadSnapshot:     windows.CreateToolhelp32Snapshot,
	threadFirst:              windows.Thread32First,
	threadNext:               windows.Thread32Next,
	openThread:               windows.OpenThread,
	resumeThread:             windows.ResumeThread,
	waitForSingleObject:      windows.WaitForSingleObject,
}

// Start creates the command suspended, assigns it to a preconfigured
// kill-on-close job, and only then permits its primary thread to execute.
func Start(command *exec.Cmd) (*Job, error) {
	return start(command, systemWindowsProcessAPI)
}

func start(command *exec.Cmd, api windowsProcessAPI) (*Job, error) {
	if command == nil {
		return nil, errors.New("supervised command is required")
	}
	jobHandle, err := api.createJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := api.setInformationJobObject(
		jobHandle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(jobHandle)
		return nil, err
	}
	job := &Job{handle: jobHandle}
	if originalCancel := command.Cancel; originalCancel != nil {
		command.Cancel = func() error {
			return errors.Join(job.Terminate(), normalizeProcessDone(originalCancel()))
		}
	}
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.CreationFlags |= windows.CREATE_SUSPENDED
	command.SysProcAttr = &attributes
	if err := command.Start(); err != nil {
		_ = job.Close()
		return nil, err
	}
	processHandle, err := api.openProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		return nil, errors.Join(err, cleanupStartedCommand(command, job, 0, false, api))
	}
	defer windows.CloseHandle(processHandle)
	threadHandle, err := openSuspendedThread(uint32(command.Process.Pid), api)
	if err != nil {
		return nil, errors.Join(err, cleanupStartedCommand(command, job, processHandle, false, api))
	}
	defer windows.CloseHandle(threadHandle)
	if err := api.assignProcessToJobObject(jobHandle, processHandle); err != nil {
		return nil, errors.Join(err, cleanupStartedCommand(command, job, processHandle, false, api))
	}
	previousSuspendCount, err := api.resumeThread(threadHandle)
	if err != nil {
		return nil, errors.Join(err, cleanupStartedCommand(command, job, processHandle, true, api))
	}
	if previousSuspendCount != 1 {
		resumeErr := fmt.Errorf("supervised process primary thread had unexpected suspend count %d", previousSuspendCount)
		return nil, errors.Join(resumeErr, cleanupStartedCommand(command, job, processHandle, true, api))
	}
	return job, nil
}

func openSuspendedThread(processID uint32, api windowsProcessAPI) (windows.Handle, error) {
	snapshot, err := api.createThreadSnapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := api.threadFirst(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.OwnerProcessID == processID {
			return api.openThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := api.threadNext(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return 0, fmt.Errorf("primary thread for supervised process %d was not found", processID)
			}
			return 0, err
		}
	}
}

func cleanupStartedCommand(command *exec.Cmd, job *Job, processHandle windows.Handle, assigned bool, api windowsProcessAPI) error {
	var terminationErr error
	terminationRequested := false
	if assigned {
		if err := normalizeProcessDone(job.Terminate()); err != nil {
			terminationErr = errors.Join(terminationErr, err)
		} else {
			terminationRequested = true
		}
	}
	if processHandle != 0 {
		if err := normalizeProcessDone(api.terminateProcess(processHandle, 1)); err != nil {
			terminationErr = errors.Join(terminationErr, err)
		} else {
			terminationRequested = true
		}
	}
	if command != nil && command.Process != nil {
		if err := normalizeProcessDone(command.Process.Kill()); err != nil {
			terminationErr = errors.Join(terminationErr, err)
		} else {
			terminationRequested = true
		}
	}
	cleanupErr := job.Close()
	exited := false
	if processHandle != 0 {
		status, err := api.waitForSingleObject(processHandle, 5_000)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else if status == windows.WAIT_OBJECT_0 {
			exited = true
		} else {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("supervised process did not exit during failed start: wait status %#x", status))
		}
	} else {
		exited = terminationRequested
	}
	if command != nil && command.Process != nil {
		if exited {
			waitErr := command.Wait()
			if command.ProcessState == nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("reap supervised process: %w", waitErr))
			}
		} else {
			cleanupErr = errors.Join(cleanupErr, command.Process.Release())
		}
	}
	if !exited {
		cleanupErr = errors.Join(cleanupErr, terminationErr)
	}
	return cleanupErr
}

func normalizeProcessDone(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// Terminate stops every process still assigned to the job.
func (job *Job) Terminate() error {
	if job == nil {
		return nil
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 {
		return nil
	}
	return terminateJob(job.handle)
}

func terminateJob(handle windows.Handle) error {
	if handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(handle, 1)
}

// Close releases the Job Object. KILL_ON_JOB_CLOSE guarantees that any
// descendant which outlived the command is terminated here.
func (job *Job) Close() error {
	if job == nil {
		return nil
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(job.handle)
	job.handle = 0
	return err
}
