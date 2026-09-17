//go:build linux && amd64

package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	synonKernelPolicySHA256      = "dbbc2b1997258c2ec7208561bab49785456fcc95cbf39a535ef16635d17af7f7"
	kernelConfinementHelperEnv   = "SYNON_INTERNAL_KERNEL_CONFINEMENT_HELPER"
	kernelConfinementPayloadEnv  = "SYNON_INTERNAL_KERNEL_CONFINEMENT_PAYLOAD"
	kernelConfinementOuter       = "outer"
	kernelConfinementTarget      = "target"
	kernelConfinementProbePrefix = "SYNON_KERNEL_CONFINEMENT_PROBE_V1:"
)

type confinedWorkerRequest struct {
	WorkspaceDir    string                `json:"workspace_dir"`
	WorkspaceFD     int                   `json:"workspace_fd"`
	Executable      string                `json:"executable"`
	Arguments       []string              `json:"arguments"`
	Environment     []string              `json:"environment"`
	Mounts          []confinedWorkerMount `json:"mounts,omitempty"`
	AuxiliaryFDs    []int                 `json:"auxiliary_fds,omitempty"`
	Protected       []string              `json:"protected_paths,omitempty"`
	ProbeToken      string                `json:"probe_token,omitempty"`
	ParentMountNS   string                `json:"parent_mount_namespace,omitempty"`
	ParentNetworkNS string                `json:"parent_network_namespace,omitempty"`
}

type confinedWorkerMount struct {
	Path     string `json:"path"`
	FD       int    `json:"fd"`
	Writable bool   `json:"writable,omitempty"`
	Regular  bool   `json:"regular,omitempty"`
	Trusted  bool   `json:"trusted,omitempty"`
}

var synonKernelFilter = buildKernelSyscallFilter()

func init() {
	phase := os.Getenv(kernelConfinementHelperEnv)
	if phase != kernelConfinementOuter && phase != kernelConfinementTarget {
		return
	}
	runtime.LockOSThread()
	exitCode, err := runConfinedWorkerHelper(phase)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "kernel confinement helper failed")
		os.Exit(125)
	}
	os.Exit(exitCode)
}

func newConfinedWorkerCommand(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string) (*exec.Cmd, error) {
	return newConfinedWorkerCommandWithProbeAndAuxiliary(workspaceDir, executable, arguments, environment, mounts, protected, "", "", "", nil)
}

func newConfinedWorkerCommandWithAuxiliary(
	workspaceDir, executable string,
	arguments, environment []string,
	mounts []WorkerMount,
	protected []string,
	auxiliary []*os.File,
) (*exec.Cmd, error) {
	return newConfinedWorkerCommandWithProbeAndAuxiliary(workspaceDir, executable, arguments, environment, mounts, protected, "", "", "", auxiliary)
}

func newConfinedWorkerCommandWithProbe(
	workspaceDir, executable string,
	arguments, environment []string,
	mounts []WorkerMount,
	protected []string,
	probeToken, parentMountNS, parentNetworkNS string,
) (*exec.Cmd, error) {
	return newConfinedWorkerCommandWithProbeAndAuxiliary(
		workspaceDir, executable, arguments, environment, mounts, protected,
		probeToken, parentMountNS, parentNetworkNS, nil,
	)
}

func newConfinedWorkerCommandWithProbeAndAuxiliary(
	workspaceDir, executable string,
	arguments, environment []string,
	mounts []WorkerMount,
	protected []string,
	probeToken, parentMountNS, parentNetworkNS string,
	auxiliary []*os.File,
) (*exec.Cmd, error) {
	workspaceDir = filepath.Clean(strings.TrimSpace(workspaceDir))
	if workspaceDir == "." || !filepath.IsAbs(workspaceDir) {
		return nil, errors.New("kernel confinement workspace is invalid")
	}
	resolvedExecutable, err := exec.LookPath(strings.TrimSpace(executable))
	if err != nil {
		return nil, fmt.Errorf("locate kernel worker executable: %w", err)
	}
	if !filepath.IsAbs(resolvedExecutable) {
		resolvedExecutable, err = filepath.Abs(resolvedExecutable)
		if err != nil {
			return nil, fmt.Errorf("resolve kernel worker executable: %w", err)
		}
	}
	resolvedExecutable, err = filepath.EvalSymlinks(resolvedExecutable)
	if err != nil {
		return nil, fmt.Errorf("resolve kernel worker executable: %w", err)
	}
	cleanEnvironment, err := confinementTargetEnvironment(environment)
	if err != nil {
		return nil, err
	}
	extraFiles := []*os.File{}
	closeExtraFiles := func() {
		for _, file := range extraFiles {
			_ = file.Close()
		}
	}
	workspaceFile, err := openFrozenKernelDirectory(workspaceDir)
	if err != nil {
		return nil, errors.New("kernel confinement workspace is invalid")
	}
	extraFiles = append(extraFiles, workspaceFile)
	request := confinedWorkerRequest{
		WorkspaceDir: workspaceDir, Executable: resolvedExecutable, Arguments: append([]string(nil), arguments...),
		WorkspaceFD: 3, Environment: cleanEnvironment, Protected: append([]string(nil), protected...),
		ProbeToken: probeToken, ParentMountNS: parentMountNS, ParentNetworkNS: parentNetworkNS,
	}
	for _, mount := range mounts {
		resolved := mount.Path
		file := mount.frozen
		if file == nil {
			var err error
			resolved, err = filepath.EvalSymlinks(mount.Path)
			if err != nil || resolved != mount.Path || !filepath.IsAbs(resolved) {
				closeExtraFiles()
				return nil, errors.New("kernel confinement mount is invalid")
			}
			file, err = openFrozenKernelMount(resolved, mount.regular)
			if err != nil {
				closeExtraFiles()
				return nil, errors.New("kernel confinement mount is invalid")
			}
		} else if !filepath.IsAbs(resolved) || !kernelFileMount(file, mount.regular) {
			closeExtraFiles()
			return nil, errors.New("kernel confinement mount is invalid")
		}
		request.Mounts = append(request.Mounts, confinedWorkerMount{
			Path: resolved, FD: 3 + len(extraFiles), Writable: mount.Writable,
			Regular: mount.regular, Trusted: mount.trusted,
		})
		extraFiles = append(extraFiles, file)
	}
	for _, file := range auxiliary {
		if !kernelSocketFile(file) {
			closeExtraFiles()
			return nil, errors.New("kernel confinement auxiliary descriptor is invalid")
		}
		request.AuxiliaryFDs = append(request.AuxiliaryFDs, 3+len(extraFiles))
		extraFiles = append(extraFiles, file)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		closeExtraFiles()
		return nil, fmt.Errorf("encode kernel confinement request: %w", err)
	}
	helper, err := os.Executable()
	if err != nil {
		closeExtraFiles()
		return nil, fmt.Errorf("locate kernel confinement helper: %w", err)
	}
	command := exec.Command(helper)
	command.ExtraFiles = extraFiles
	command.Env = append(append([]string(nil), cleanEnvironment...),
		kernelConfinementHelperEnv+"="+kernelConfinementOuter,
		kernelConfinementPayloadEnv+"="+base64.RawStdEncoding.EncodeToString(raw),
	)
	return command, nil
}

func kernelFileMount(file *os.File, regular bool) bool {
	if file == nil {
		return false
	}
	var stat unix.Stat_t
	want := uint32(unix.S_IFDIR)
	if regular {
		want = unix.S_IFREG
	}
	return unix.Fstat(int(file.Fd()), &stat) == nil && stat.Mode&unix.S_IFMT == want
}

func openFrozenKernelDirectory(path string) (*os.File, error) {
	return openFrozenKernelMount(path, false)
}

func freezeInternalKernelDirectory(path string) (*os.File, error) {
	return openFrozenKernelDirectory(path)
}

func secureROperationLog(prefix string) (string, *os.File, error) {
	directoryFD, err := unix.Open(prefix, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, errors.New("R operation log environment is unavailable")
	}
	defer unix.Close(directoryFD)
	fd, err := unix.Openat(directoryFD, ".operon_metadata.r.ndjson", unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("create R operation log: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		_ = unix.Close(fd)
		return "", nil, errors.New("R operation log is not a private regular file")
	}
	path := filepath.Join(prefix, ".operon_metadata.r.ndjson")
	frozenFD, err := unix.Open("/proc/self/fd/"+strconv.Itoa(fd), unix.O_PATH|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return "", nil, errors.New("freeze R operation log authority")
	}
	var frozen unix.Stat_t
	if err := unix.Fstat(frozenFD, &frozen); err != nil || frozen.Dev != stat.Dev || frozen.Ino != stat.Ino {
		_ = unix.Close(frozenFD)
		return "", nil, errors.New("freeze R operation log authority")
	}
	return path, os.NewFile(uintptr(frozenFD), path), nil
}

func readBoundedKernelMetadataFile(prefix, name string, limit int) ([]byte, bool, error) {
	directoryFD, err := unix.Open(prefix, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, errors.New("R environment metadata directory is unavailable")
	}
	defer unix.Close(directoryFD)
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, metadataReadError(name, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || stat.Size < 0 || stat.Size > int64(limit) {
		_ = unix.Close(fd)
		return nil, false, errors.New("R environment metadata is not a bounded private regular file")
	}
	file := os.NewFile(uintptr(fd), name)
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	_ = file.Close()
	if err != nil || len(raw) > limit {
		return nil, false, errors.New("R environment metadata exceeds the size limit")
	}
	return raw, true, nil
}

func CopyProviderOperationFile(prefix, name string, target io.Writer, limit int64) (int64, error) {
	if target == nil || limit <= 0 || filepath.Base(name) != name || strings.ContainsAny(name, "\x00\r\n") {
		return 0, errors.New("provider operation output request is invalid")
	}
	directoryFD, err := unix.Open(prefix, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, errors.New("provider operation output directory is unavailable")
	}
	defer unix.Close(directoryFD)
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0, errors.New("provider operation output file is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || stat.Size < 0 || stat.Size > limit {
		_ = unix.Close(fd)
		return 0, errors.New("provider operation output is not a bounded private regular file")
	}
	file := os.NewFile(uintptr(fd), name)
	written, copyErr := io.CopyN(target, file, stat.Size)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != stat.Size {
		return written, errors.New("provider operation output could not be copied safely")
	}
	return written, nil
}

func lockKernelFile(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
			}, nil
		} else if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func replaceKernelDirectory(staging, target string, targetExists bool) error {
	if !targetExists {
		return os.Rename(staging, target)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	return os.RemoveAll(staging)
}

func openFrozenKernelMount(path string, regular bool) (*os.File, error) {
	flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if !regular {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	want := uint32(unix.S_IFDIR)
	if regular {
		want = unix.S_IFREG
	}
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != want {
		_ = unix.Close(fd)
		return nil, errors.New("kernel confinement mount type is invalid")
	}
	return os.NewFile(uintptr(fd), path), nil
}

func confinementTargetEnvironment(environment []string) ([]string, error) {
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" || strings.ContainsAny(key, "\x00\r\n") || strings.ContainsRune(item, '\x00') {
			return nil, errors.New("kernel worker environment is invalid")
		}
		if key == kernelConfinementHelperEnv || key == kernelConfinementPayloadEnv {
			continue
		}
		clean = append(clean, item)
	}
	return clean, nil
}

func runConfinedWorkerHelper(phase string) (int, error) {
	request, encoded, err := decodeConfinedWorkerRequest()
	if err != nil {
		return 125, err
	}
	if phase == kernelConfinementOuter {
		return 126, enterKernelMountNamespace(request, encoded)
	}
	if request.ProbeToken != "" {
		return 0, probeConfinedWorkerTarget(request)
	}
	return 126, execConfinedWorkerTarget(request)
}

func decodeConfinedWorkerRequest() (confinedWorkerRequest, string, error) {
	encoded := os.Getenv(kernelConfinementPayloadEnv)
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request confinedWorkerRequest
	if err := decoder.Decode(&request); err != nil {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	}
	if request.WorkspaceDir == "" || !filepath.IsAbs(request.WorkspaceDir) || request.WorkspaceFD < 3 ||
		request.Executable == "" || !filepath.IsAbs(request.Executable) {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	}
	if request.ProbeToken == "" {
		if request.ParentMountNS != "" || request.ParentNetworkNS != "" {
			return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
		}
	} else if len(request.ProbeToken) != 64 || request.ParentMountNS == "" || request.ParentNetworkNS == "" ||
		len(request.ParentMountNS) > 128 || len(request.ParentNetworkNS) > 128 {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	} else if _, err := hex.DecodeString(request.ProbeToken); err != nil {
		return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
	}
	seenFDs := map[int]bool{request.WorkspaceFD: true}
	for _, mount := range request.Mounts {
		if mount.FD < 3 || seenFDs[mount.FD] || !filepath.IsAbs(mount.Path) {
			return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
		}
		seenFDs[mount.FD] = true
	}
	for _, fd := range request.AuxiliaryFDs {
		if fd < 3 || seenFDs[fd] || !kernelSocketFD(fd) {
			return confinedWorkerRequest{}, "", errors.New("kernel confinement request is invalid")
		}
		seenFDs[fd] = true
	}
	return request, encoded, nil
}

func enterKernelMountNamespace(request confinedWorkerRequest, encoded string) error {
	bwrap, err := absoluteExecutable("bwrap")
	if err != nil {
		return ErrConfinementUnavailable
	}
	helper, err := os.Executable()
	if err != nil {
		return ErrConfinementUnavailable
	}
	helper, err = filepath.EvalSymlinks(helper)
	if err != nil {
		return ErrConfinementUnavailable
	}
	hostsFD, err := syntheticKernelEtcFile("hosts", "127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n")
	if err != nil {
		return ErrConfinementUnavailable
	}
	defer unix.Close(hostsFD)
	resolvFD, err := syntheticKernelEtcFile("resolv", "nameserver 127.0.0.1\noptions attempts:1 timeout:1\n")
	if err != nil {
		return ErrConfinementUnavailable
	}
	defer unix.Close(resolvFD)
	arguments, err := kernelBubblewrapArguments(request, helper, hostsFD, resolvFD)
	if err != nil {
		return err
	}
	environment := append(append([]string(nil), request.Environment...),
		kernelConfinementHelperEnv+"="+kernelConfinementTarget,
		kernelConfinementPayloadEnv+"="+encoded,
	)
	return syscall.Exec(bwrap, append([]string{bwrap}, arguments...), environment)
}

func syntheticKernelEtcFile(name, content string) (int, error) {
	fd, err := unix.MemfdCreate("synon-kernel-"+name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, err
	}
	if _, err := unix.Write(fd, []byte(content)); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func kernelBubblewrapArguments(request confinedWorkerRequest, helper string, hostsFD, resolvFD int) ([]string, error) {
	arguments := []string{
		"--unshare-user", "--disable-userns", "--unshare-ipc", "--unshare-uts", "--hostname", "localhost",
		"--new-session", "--die-with-parent", "--unshare-net", "--unshare-pid",
	}
	created := map[string]bool{"/": true}
	for _, path := range []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/opt", "/nix"} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			arguments = append(arguments, "--ro-bind", path, path)
			created[path] = true
		}
	}
	arguments = appendSandboxDirectory(arguments, created, "/etc")
	// Bubblewrap creates bind targets while processing the argument stream. Keep
	// the synthetic /etc writable until every target exists, then seal the
	// directory below. Setuid bubblewrap cannot populate a 0555 tmpfs on hosted
	// runners even though rootless user-namespace bubblewrap can.
	arguments = append(arguments, "--perms", "0755", "--tmpfs", "/etc")
	for _, path := range []string{
		"/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/ld.so.cache", "/etc/ld.so.conf",
		"/etc/ld.so.conf.d", "/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/localtime",
		"/etc/mime.types", "/etc/alternatives", "/etc/default", "/etc/bash.bashrc", "/etc/fonts",
	} {
		if _, err := os.Stat(path); err == nil {
			arguments = appendSandboxParents(arguments, created, path)
			arguments = append(arguments, "--ro-bind", path, path)
		}
	}
	arguments = append(arguments,
		"--perms", "0444", "--file", strconv.Itoa(hostsFD), "/etc/hosts",
		"--perms", "0444", "--file", strconv.Itoa(resolvFD), "/etc/resolv.conf",
		"--ro-bind", "/dev/null", "/etc/ld.so.preload",
		"--chmod", "0555", "/etc",
	)
	for _, path := range []string{"/tmp", "/run"} {
		arguments = appendSandboxDirectory(arguments, created, path)
		arguments = append(arguments, "--tmpfs", path)
	}
	arguments = appendSandboxDirectory(arguments, created, "/tmp/operon-sandbox")
	home := filepath.Clean(strings.TrimSpace(environmentValue(request.Environment, "HOME")))
	if filepath.IsAbs(home) && home != "/" {
		arguments = appendSandboxDirectory(arguments, created, home)
		arguments = append(arguments, "--tmpfs", home)
	}
	arguments = append(arguments, "--dev", "/dev")
	created["/dev"] = true
	acceleratorDevices, err := kernelAcceleratorDevicePaths()
	if err != nil {
		return nil, err
	}
	for _, device := range acceleratorDevices {
		arguments = appendSandboxParents(arguments, created, device)
		arguments = append(arguments, "--dev-bind", device, device)
	}
	arguments = append(arguments, "--chmod", "0555", "/dev", "--proc", "/proc")
	for _, path := range []string{
		"/proc/1/environ", "/proc/1/mem", "/proc/1/cmdline", "/proc/1/maps",
		"/proc/1/task/1/environ", "/proc/1/task/1/mem", "/proc/1/task/1/cmdline", "/proc/1/task/1/maps",
		"/proc/self/mountinfo", "/proc/self/mounts", "/proc/mounts", "/proc/sys/kernel/random/boot_id",
	} {
		arguments = append(arguments, "--ro-bind-try", "/dev/null", path)
	}

	readOnlyDirectories := []string{}
	if prefix := filepath.Clean(strings.TrimSpace(environmentValue(request.Environment, "CONDA_PREFIX"))); filepath.IsAbs(prefix) {
		readOnlyDirectories = append(readOnlyDirectories, prefix)
	}
	if prefix := kernelPythonRuntimePrefix(request.Executable); prefix != "" && !pathCoveredBy(prefix, readOnlyDirectories) {
		// A Python selected from a user or application-managed distribution
		// needs its standard library, not only the executable inode. Treat the
		// verified runtime prefix as read-only execution data so encodings and
		// extension modules remain available without exposing a writable host
		// directory.
		readOnlyDirectories = append(readOnlyDirectories, prefix)
	}
	for _, path := range readOnlyDirectories {
		arguments = appendSandboxDirectory(arguments, created, path)
		arguments = append(arguments, "--ro-bind", path, path)
	}
	workspaceOverlays := make([]confinedWorkerMount, 0)
	for _, mount := range request.Mounts {
		if !kernelMountFD(mount.FD, mount.Regular) {
			return nil, errors.New("kernel confinement mount is invalid")
		}
		if !mount.Trusted && kernelMountOverlapsProtectedPath(mount.Path, request, helper) {
			return nil, errors.New("kernel confinement mount overlaps a protected path")
		}
		if pathContains(request.WorkspaceDir, mount.Path) {
			if mount.Path == request.WorkspaceDir || mount.Writable {
				return nil, errors.New("kernel confinement workspace overlay must be a read-only child")
			}
			workspaceOverlays = append(workspaceOverlays, mount)
			continue
		}
		if mount.Regular {
			arguments = appendSandboxParents(arguments, created, mount.Path)
		} else {
			arguments = appendSandboxDirectory(arguments, created, mount.Path)
		}
		mode := "--ro-bind-fd"
		if mount.Writable {
			mode = "--bind-fd"
		}
		arguments = append(arguments, mode, strconv.Itoa(mount.FD), mount.Path)
	}
	for _, fd := range request.AuxiliaryFDs {
		if !kernelSocketFD(fd) {
			return nil, errors.New("kernel confinement auxiliary descriptor is invalid")
		}
	}
	for _, path := range append([]string{helper, request.Executable}, request.Arguments...) {
		if !filepath.IsAbs(path) || pathCoveredBy(path, readOnlyDirectories) || pathCoveredBy(path, []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/opt", "/nix"}) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		arguments = appendSandboxParents(arguments, created, path)
		arguments = append(arguments, "--ro-bind", path, path)
	}
	if !kernelDirectoryFD(request.WorkspaceFD) {
		return nil, errors.New("kernel confinement workspace is invalid")
	}
	arguments = appendSandboxDirectory(arguments, created, request.WorkspaceDir)
	arguments = append(arguments, "--bind-fd", strconv.Itoa(request.WorkspaceFD), request.WorkspaceDir)
	for _, mount := range workspaceOverlays {
		// Host-verified immutable artifacts and promoted execution-pack outputs
		// are overlaid after the writable workspace bind. This makes the
		// filesystem itself authoritative for Python, R, Bash, and REPL instead
		// of relying on language-specific source inspection.
		arguments = append(arguments, "--ro-bind-fd", strconv.Itoa(mount.FD), mount.Path)
	}
	skillRuntime, err := kernelWorkspaceSkillRuntimeReadOnlyPath(request.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	if skillRuntime != "" {
		// Reviewed Skill assets are executable inputs, not task-owned source.
		// Overlay the cache after the writable workspace bind so shell and
		// Python executions cannot chmod, replace, or patch validation logic.
		arguments = append(arguments, "--ro-bind", skillRuntime, skillRuntime)
	}
	arguments = append(arguments, "--chdir", request.WorkspaceDir)
	arguments = append(arguments, "--", helper)
	return arguments, nil
}

func kernelWorkspaceSkillRuntimeReadOnlyPath(workspace string) (string, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if !filepath.IsAbs(workspace) {
		return "", errors.New("kernel confinement workspace is invalid")
	}
	target := filepath.Join(workspace, ".synon", "runtime", "skills")
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("kernel Skill runtime path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(target) || !pathContains(workspace, resolved) {
		return "", errors.New("kernel Skill runtime path is invalid")
	}
	return resolved, nil
}

func kernelAcceleratorDevicePaths() ([]string, error) {
	candidates := []string{"/dev/dxg", "/dev/kfd"}
	for _, pattern := range []string{
		"/dev/nvidia*", "/dev/nvidia-caps/nvidia-cap*", "/dev/dri/card*", "/dev/dri/renderD*",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, errors.New("kernel accelerator device discovery failed")
		}
		candidates = append(candidates, matches...)
	}
	seen := map[string]struct{}{}
	devices := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if !filepath.IsAbs(candidate) || candidate == "/dev" {
			continue
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("kernel accelerator device is invalid")
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		devices = append(devices, candidate)
	}
	sort.Strings(devices)
	return devices, nil
}

func kernelPythonRuntimePrefix(executable string) string {
	executable = filepath.Clean(strings.TrimSpace(executable))
	if !filepath.IsAbs(executable) || !strings.HasPrefix(strings.ToLower(filepath.Base(executable)), "python") {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(executable))
	if !filepath.IsAbs(prefix) || prefix == "/" || !pathContains(prefix, executable) {
		return ""
	}
	for _, systemRoot := range []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/opt", "/nix"} {
		if prefix == systemRoot || pathContains(systemRoot, prefix) {
			return ""
		}
	}
	matches, err := filepath.Glob(filepath.Join(prefix, "lib", "python*", "encodings", "__init__.py"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	for _, marker := range matches {
		info, statErr := os.Lstat(marker)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			resolved, resolveErr := filepath.EvalSymlinks(prefix)
			if resolveErr == nil && filepath.IsAbs(resolved) {
				return resolved
			}
		}
	}
	return ""
}

func kernelDirectoryFD(fd int) bool {
	return kernelMountFD(fd, false)
}

func kernelMountFD(fd int, regular bool) bool {
	var stat unix.Stat_t
	want := uint32(unix.S_IFDIR)
	if regular {
		want = unix.S_IFREG
	}
	return fd >= 3 && unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == want
}

func kernelSocketFile(file *os.File) bool {
	return file != nil && kernelSocketFD(int(file.Fd()))
}

func kernelSocketFD(fd int) bool {
	var stat unix.Stat_t
	return fd >= 3 && unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFSOCK
}

func kernelMountOverlapsProtectedPath(mount string, request confinedWorkerRequest, helper string) bool {
	for _, protected := range []string{
		"/proc", "/dev", "/sys", "/etc", "/usr", "/lib", "/lib64", "/bin", "/sbin", "/opt", "/nix", "/run",
		"/tmp/operon-sandbox", request.WorkspaceDir,
		canonicalProtectedPath(environmentValue(request.Environment, "CONDA_PREFIX")),
		canonicalProtectedPath(environmentValue(request.Environment, "MAMBA_ROOT_PREFIX")),
		canonicalProtectedPath(environmentValue(request.Environment, "SYNON_DATA_DIR")),
		canonicalProtectedPath(environmentValue(request.Environment, "SYNON_AUTH_DIR")),
		canonicalProtectedPath(helper), canonicalProtectedPath(request.Executable),
	} {
		if filepath.IsAbs(protected) && pathsOverlap(mount, protected) {
			return true
		}
	}
	for _, protected := range request.Protected {
		if filepath.IsAbs(protected) && pathsOverlap(mount, canonicalProtectedPath(protected)) {
			return true
		}
	}
	for _, protected := range []string{
		"/tmp", canonicalProtectedPath(environmentValue(request.Environment, "HOME")),
	} {
		if filepath.IsAbs(protected) && pathContains(mount, protected) {
			return true
		}
	}
	for _, argument := range request.Arguments {
		if filepath.IsAbs(argument) && pathsOverlap(mount, canonicalProtectedPath(argument)) {
			return true
		}
	}
	return false
}

func canonicalProtectedPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func appendSandboxDirectory(arguments []string, created map[string]bool, path string) []string {
	arguments = appendSandboxParents(arguments, created, path)
	if !created[path] {
		arguments = append(arguments, "--dir", path)
		created[path] = true
	}
	return arguments
}

func appendSandboxParents(arguments []string, created map[string]bool, path string) []string {
	parents := []string{}
	for parent := filepath.Dir(path); parent != "/" && parent != "."; parent = filepath.Dir(parent) {
		if created[parent] {
			break
		}
		parents = append(parents, parent)
	}
	for index := len(parents) - 1; index >= 0; index-- {
		parent := parents[index]
		if !created[parent] {
			arguments = append(arguments, "--dir", parent)
			created[parent] = true
		}
	}
	return arguments
}

func environmentValue(environment []string, key string) string {
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

func pathCoveredBy(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func execConfinedWorkerTarget(request confinedWorkerRequest) error {
	if err := applyKernelConfinementPolicy(); err != nil {
		return err
	}
	arguments := append([]string{request.Executable}, request.Arguments...)
	return syscall.Exec(request.Executable, arguments, request.Environment)
}

func applyKernelConfinementPolicy() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return errors.New("kernel confinement is unavailable")
	}
	program := unix.SockFprog{Len: uint16(len(synonKernelFilter)), Filter: &synonKernelFilter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return errors.New("kernel confinement is unavailable")
	}
	return nil
}

func probeConfinedWorkerTarget(request confinedWorkerRequest) error {
	if request.ProbeToken == "" {
		return errors.New("kernel confinement probe is invalid")
	}
	if err := applyKernelConfinementPolicy(); err != nil {
		return err
	}
	mountNamespace, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil || mountNamespace == request.ParentMountNS {
		return errors.New("kernel mount namespace is unavailable")
	}
	networkNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil || networkNamespace == request.ParentNetworkNS {
		return errors.New("kernel network namespace is unavailable")
	}
	noNewPrivileges, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0)
	if errno != 0 || noNewPrivileges != 1 {
		return errors.New("kernel no-new-privileges boundary is unavailable")
	}
	if descriptor, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); err != unix.EPERM {
		if descriptor >= 0 {
			_ = unix.Close(descriptor)
		}
		return errors.New("kernel seccomp boundary is unavailable")
	}
	_, err = fmt.Fprintln(os.Stdout, kernelConfinementProbeSentinel(request.ProbeToken))
	return err
}

func kernelConfinementProbeSentinel(token string) string {
	digest := sha256.Sum256([]byte(token))
	return kernelConfinementProbePrefix + hex.EncodeToString(digest[:])
}

func platformConfinementEvidence() ConfinementEvidence {
	if _, err := absoluteExecutable("bwrap"); err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "bubblewrap is unavailable"}
	}
	return ConfinementEvidence{
		Available: true, Mode: "synon-bwrap-unix-block-v1", PolicySHA256: synonKernelPolicySHA256,
	}
}

func probePlatformConfinement() ConfinementEvidence {
	evidence := platformConfinementEvidence()
	if !evidence.Available {
		return evidence
	}
	bubblewrap, err := absoluteExecutable("bwrap")
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	helper, err := os.Executable()
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	parentMountNS, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	parentNetworkNS, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	probeToken := hex.EncodeToString(random)
	workspace, err := os.MkdirTemp("", "synon-kernel-confinement-probe-")
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	defer os.RemoveAll(workspace)
	command, err := newConfinedWorkerCommandWithProbe(
		workspace,
		helper,
		nil,
		[]string{"HOME=" + workspace, "PATH=" + filepath.Dir(bubblewrap) + ":/usr/bin:/bin", "LANG=C.UTF-8"},
		nil,
		nil,
		probeToken,
		parentMountNS,
		parentNetworkNS,
	)
	if err != nil {
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	var stdout, stderr bytes.Buffer
	command.Dir = "/"
	command.Stdout = &stdout
	command.Stderr = &stderr
	configureWorkerProcess(command)
	if err := command.Start(); err != nil {
		closeKernelCommandExtraFiles(command)
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
	}
	closeKernelCommandExtraFiles(command)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil || stdout.String() != kernelConfinementProbeSentinel(probeToken)+"\n" || stderr.Len() != 0 {
			return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe failed"}
		}
		return evidence
	case <-timer.C:
		_ = killWorkerProcess(command)
		reapTimer := time.NewTimer(2 * time.Second)
		select {
		case <-done:
		case <-reapTimer.C:
		}
		if !reapTimer.Stop() {
			select {
			case <-reapTimer.C:
			default:
			}
		}
		return ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel confinement probe timed out"}
	}
}
