//go:build linux

package shellops

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const maxSandboxPayloadBytes = 1 << 20

func init() {
	if os.Getenv(sandboxHelperEnv) != "1" {
		return
	}
	if err := runLinuxSandboxHelper(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "synon sandbox helper:", err)
		os.Exit(125)
	}
	os.Exit(126)
}

func wrapSandboxCommand(request sandboxCommand) (string, []string, []string, SandboxEvidence, error) {
	evidence := platformSandboxStatus()
	if !evidence.Available {
		return "", nil, nil, evidence, errors.New("Linux shell sandbox is unavailable: " + evidence.Reason)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return "", nil, nil, evidence, fmt.Errorf("encode sandbox request: %w", err)
	}
	if len(raw) > maxSandboxPayloadBytes {
		return "", nil, nil, evidence, errors.New("sandbox request exceeds internal size limit")
	}
	helper, err := os.Executable()
	if err != nil {
		return "", nil, nil, evidence, fmt.Errorf("locate sandbox helper executable: %w", err)
	}
	environment := append([]string(nil), request.Environment...)
	environment = append(environment,
		sandboxHelperEnv+"=1",
		sandboxPayloadEnv+"="+base64.RawStdEncoding.EncodeToString(raw),
	)
	return helper, nil, environment, evidence, nil
}

func runLinuxSandboxHelper() error {
	encoded := os.Getenv(sandboxPayloadEnv)
	if encoded == "" || len(encoded) > base64.RawStdEncoding.EncodedLen(maxSandboxPayloadBytes) {
		return errors.New("sandbox payload is missing or oversized")
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("sandbox payload is invalid")
	}
	var request sandboxCommand
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode sandbox payload: %w", err)
	}
	if request.Command == "" || !filepath.IsAbs(request.Command) || !filepath.IsAbs(request.Root) || !filepath.IsAbs(request.Workdir) {
		return errors.New("sandbox command, root, and workdir must be absolute")
	}
	if err := ensureInside(request.Root, request.Workdir); err != nil {
		return err
	}
	if err := os.Chdir(request.Workdir); err != nil {
		return fmt.Errorf("enter sandbox workdir: %w", err)
	}
	unix.Umask(0o077)
	if err := applyLinuxResourceLimits(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set sandbox no-new-privileges: %w", err)
	}
	if err := applyLinuxLandlock(request); err != nil {
		return err
	}
	if err := applyLinuxNetworkSeccomp(); err != nil {
		return err
	}
	environment := make([]string, 0, len(request.Environment))
	for _, entry := range request.Environment {
		if entry == "" || len(entry) > 128*1024 {
			return errors.New("sandbox environment entry is invalid")
		}
		environment = append(environment, entry)
	}
	arguments := append([]string{request.Command}, request.Args...)
	return syscall.Exec(request.Command, arguments, environment)
}

func applyLinuxResourceLimits() error {
	limits := []struct {
		resource int
		current  uint64
		maximum  uint64
	}{
		{unix.RLIMIT_CORE, 0, 0},
		{unix.RLIMIT_NOFILE, 256, 256},
		// RLIMIT_NPROC is per user, not per sandbox. A low value can prevent the
		// first thread when the host user already runs other supervised jobs.
		{unix.RLIMIT_NPROC, 4096, 4096},
		{unix.RLIMIT_FSIZE, 1 << 30, 1 << 30},
		{unix.RLIMIT_AS, 8 << 30, 8 << 30},
		{unix.RLIMIT_CPU, 600, 600},
	}
	for _, limit := range limits {
		var inherited unix.Rlimit
		if err := unix.Getrlimit(limit.resource, &inherited); err != nil {
			return fmt.Errorf("read sandbox resource limit %d: %w", limit.resource, err)
		}
		maximum := min(limit.maximum, inherited.Max)
		current := min(limit.current, maximum)
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: current, Max: maximum}); err != nil {
			return fmt.Errorf("set sandbox resource limit %d: %w", limit.resource, err)
		}
	}
	return nil
}

func applyLinuxLandlock(request sandboxCommand) error {
	abi, err := linuxLandlockABI()
	if err != nil {
		return err
	}
	readAccess := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	writeAccess := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	attribute := unix.LandlockRulesetAttr{Access_fs: readAccess | writeAccess}
	if abi >= 4 {
		attribute.Access_net = unix.LANDLOCK_ACCESS_NET_CONNECT_TCP | unix.LANDLOCK_ACCESS_NET_BIND_TCP
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attribute)), unsafe.Sizeof(attribute), 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)
	if err := addLandlockPathRule(ruleset, request.Root, readAccess|writeAccess); err != nil {
		return err
	}
	if err := addLandlockPathRule(ruleset, "/dev/null", readAccess|unix.LANDLOCK_ACCESS_FS_WRITE_FILE); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	readOnlyPaths := append([]string{
		"/bin", "/usr", "/lib", "/lib64", "/etc", "/dev/urandom", "/dev/zero", "/proc/self", "/sys/devices/system/cpu",
	}, request.ReadOnlyPaths...)
	for _, path := range uniqueSandboxPaths(readOnlyPaths) {
		if err := addLandlockPathRule(ruleset, path, readAccess); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("restrict sandbox with Landlock: %w", errno)
	}
	return nil
}

func addLandlockPathRule(ruleset int, path string, allowed uint64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		allowed &= unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	parent, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open Landlock path %s: %w", path, err)
	}
	defer unix.Close(parent)
	attribute := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(parent)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attribute)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("add Landlock path %s: %w", path, errno)
	}
	return nil
}

func linuxLandlockABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("query Landlock ABI: %w", errno)
	}
	if version < 3 {
		return int(version), fmt.Errorf("Landlock ABI %d is below required ABI 3", version)
	}
	return int(version), nil
}

func applyLinuxNetworkSeccomp() error {
	blocked := []uintptr{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
		unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_SENDMMSG,
		unix.SYS_RECVFROM, unix.SYS_RECVMSG, unix.SYS_RECVMMSG,
	}
	filter := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
	for _, syscallNumber := range blocked {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: uint32(syscallNumber)},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("install sandbox seccomp filter: %w", err)
	}
	return nil
}

type sandboxProcess struct {
	command *exec.Cmd
}

func startSandboxProcess(command *exec.Cmd) (*sandboxProcess, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error { return killSandboxProcessTree(command) }
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &sandboxProcess{command: command}, nil
}

func (process *sandboxProcess) kill() error {
	if process == nil {
		return nil
	}
	return killSandboxProcessTree(process.command)
}

func (process *sandboxProcess) close() {}

func killSandboxProcessTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func platformSandboxStatus() SandboxEvidence {
	abi, err := linuxLandlockABI()
	if err != nil {
		return SandboxEvidence{Platform: "linux", Mode: "fail-closed", Filesystem: "unavailable", Network: "unavailable", Environment: "minimal", ResourceLimits: true, Available: false, Reason: err.Error()}
	}
	return SandboxEvidence{
		Platform: "linux", Mode: fmt.Sprintf("landlock-v%d+seccomp", abi), Filesystem: "workspace-read-write/system-read-only",
		Network: "denied", Environment: "minimal", ResourceLimits: true, Available: true,
	}
}
