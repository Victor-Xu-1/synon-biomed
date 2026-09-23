//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsSecurityCapabilitiesAttribute = uintptr(0x00020009)

type windowsSecurityCapabilities struct {
	AppContainerSID *windows.SID
	Capabilities    *windows.SIDAndAttributes
	Count           uint32
	Reserved        uint32
}

var windowsIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func init() {
	if os.Getenv(windowsConfinedHelperEnv) != "1" {
		return
	}
	request, err := decodeWindowsConfinedRequest(os.Getenv(windowsConfinedPayloadEnv))
	if err == nil {
		var exitCode uint32
		exitCode, err = runWindowsConfinedHelper(request)
		if err == nil {
			os.Exit(int(exitCode))
		}
	}
	var win32 syscall.Errno
	if errors.As(err, &win32) {
		_, _ = fmt.Fprintf(os.Stderr, "Windows kernel confinement helper failed (Win32 %d)\n", win32)
	} else {
		_, _ = fmt.Fprintln(os.Stderr, "Windows kernel confinement helper failed")
	}
	os.Exit(125)
}

// The helper is trusted product code. It can execute only after the parent
// supervisor has placed it in a kill-on-close Job. The target is created with
// an AppContainer token while suspended, checked for Job membership, and only
// then resumed. No unconfined target instruction is ever allowed to run.
func runWindowsConfinedHelper(request *windowsConfinedRequest) (uint32, error) {
	if request == nil {
		return 0, ErrConfinementUnavailable
	}
	if inJob, err := windowsProcessInJob(windows.CurrentProcess()); err != nil || !inJob {
		return 0, fmt.Errorf("%w: Windows confinement helper has no process Job", ErrConfinementUnavailable)
	}
	if err := validateWindowsConfinementRequest(
		request.Workspace, request.Executable, request.Arguments, request.Environment, nil, nil, nil,
	); err != nil {
		return 0, err
	}
	if err := verifyWindowsConfinementAuthorityBindings(request); err != nil {
		return 0, err
	}
	sid, err := deriveWindowsAppContainerSID(request.ProfileName)
	if err != nil {
		return 0, err
	}
	defer windows.FreeSid(sid)
	input, err := duplicateWindowsInheritedHandle(windows.Handle(os.Stdin.Fd()))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(input)
	output, err := duplicateWindowsInheritedHandle(windows.Handle(os.Stdout.Fd()))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(output)
	diagnostic, err := duplicateWindowsInheritedHandle(windows.Handle(os.Stderr.Fd()))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(diagnostic)
	handles := []windows.Handle{input, output, diagnostic}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return 0, err
	}
	defer attributes.Delete()
	capabilities := windowsSecurityCapabilities{AppContainerSID: sid}
	if err := attributes.Update(
		windowsSecurityCapabilitiesAttribute, unsafe.Pointer(&capabilities), unsafe.Sizeof(capabilities),
	); err != nil {
		return 0, err
	}
	if err := attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		return 0, err
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.ProcThreadAttributeList = attributes.List()
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = input, output, diagnostic
	environment, err := windowsConfinedEnvironment(request.Environment, request.Workspace)
	if err != nil {
		return 0, err
	}
	executable, err := windows.UTF16PtrFromString(request.Executable)
	if err != nil {
		return 0, err
	}
	commandLine, err := windows.UTF16PtrFromString(
		windows.ComposeCommandLine(append([]string{request.Executable}, request.Arguments...)),
	)
	if err != nil {
		return 0, err
	}
	workingDirectory, err := windows.UTF16PtrFromString(request.Workspace)
	if err != nil {
		return 0, err
	}
	process := windows.ProcessInformation{}
	err = windows.CreateProcess(executable, commandLine, nil, nil, true,
		windows.CREATE_SUSPENDED|windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&environment[0], workingDirectory, &startup.StartupInfo, &process)
	runtime.KeepAlive(capabilities)
	runtime.KeepAlive(handles)
	if err != nil {
		return 0, fmt.Errorf("create AppContainer kernel worker: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	started := false
	defer func() {
		if !started {
			_ = windows.TerminateProcess(process.Process, 1)
		}
	}()
	if inJob, err := windowsProcessInJob(process.Process); err != nil || !inJob {
		return 0, fmt.Errorf("%w: AppContainer worker is outside the process Job", ErrConfinementUnavailable)
	}
	if err := verifyWindowsConfinementAuthorityBindings(request); err != nil {
		return 0, fmt.Errorf("%w: worker authority changed before resume: %v", ErrConfinementUnavailable, err)
	}
	if err := verifyWindowsSuspendedWorkerImage(process.Process, request.Authority[0]); err != nil {
		return 0, err
	}
	previousSuspendCount, err := windows.ResumeThread(process.Thread)
	if err != nil {
		return 0, fmt.Errorf("resume AppContainer kernel worker: %w", err)
	}
	if previousSuspendCount != 1 {
		return 0, fmt.Errorf("AppContainer kernel worker had unexpected suspend count %d", previousSuspendCount)
	}
	started = true
	status, err := windows.WaitForSingleObject(process.Process, windows.INFINITE)
	if err != nil {
		return 0, fmt.Errorf("wait for AppContainer kernel worker: %w", err)
	}
	if status != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("AppContainer kernel worker wait status %#x", status)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
		return 0, err
	}
	return exitCode, nil
}

func verifyWindowsSuspendedWorkerImage(process windows.Handle, expected windowsConfinementFrozenPath) error {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return fmt.Errorf("%w: suspended worker image is unavailable: %v", ErrConfinementUnavailable, err)
	}
	path := strings.TrimPrefix(windows.UTF16ToString(buffer[:size]), `\\?\`)
	if !strings.EqualFold(filepath.Clean(path), expected.Path) {
		return fmt.Errorf("%w: suspended worker image path changed", ErrConfinementUnavailable)
	}
	actual, err := windowsConfinementPathIdentity(path)
	if err != nil || !sameWindowsConfinementIdentity(expected, actual) {
		return fmt.Errorf("%w: suspended worker image identity changed", ErrConfinementUnavailable)
	}
	return nil
}

func duplicateWindowsInheritedHandle(original windows.Handle) (windows.Handle, error) {
	if original == 0 || original == windows.InvalidHandle {
		return 0, errors.New("Windows kernel confinement standard handle is unavailable")
	}
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), original, windows.CurrentProcess(),
		&duplicate, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, err
	}
	return duplicate, nil
}

func windowsProcessInJob(process windows.Handle) (bool, error) {
	var inJob uint32
	result, _, err := windowsIsProcessInJob.Call(uintptr(process), 0, uintptr(unsafe.Pointer(&inJob)))
	if result == 0 {
		return false, err
	}
	return inJob != 0, nil
}

func windowsConfinedEnvironment(requested []string, workspace string) ([]uint16, error) {
	values := make(map[string]string, len(requested)+3)
	for _, key := range windowsConfinementOSKeys {
		if value := os.Getenv(key); value != "" {
			values[strings.ToUpper(key)] = key + "=" + value
		}
	}
	for _, entry := range requested {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, errors.New("Windows kernel worker environment is invalid")
		}
		if strings.EqualFold(key, windowsConfinedHelperEnv) || strings.EqualFold(key, windowsConfinedPayloadEnv) {
			return nil, errors.New("Windows kernel worker environment contains a reserved confinement key")
		}
		values[strings.ToUpper(key)] = entry
	}
	if root := strings.TrimSpace(os.Getenv("SystemRoot")); root != "" {
		values["SYSTEMROOT"] = "SystemRoot=" + root
	}
	values["TEMP"] = "TEMP=" + workspace
	values["TMP"] = "TMP=" + workspace
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	block := make([]uint16, 0, 256)
	for _, key := range keys {
		item, err := windows.UTF16FromString(values[key])
		if err != nil {
			return nil, err
		}
		block = append(block, item...)
	}
	block = append(block, 0)
	return block, nil
}

// The trusted helper exits when its target exits. Bounding pipe settlement
// prevents a detached descendant from keeping the parent command in Wait
// forever after the target has gone.
const windowsConfinedWaitDelay = 2 * time.Second
