//go:build linux

package detached

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ExecutorLaunchRequest is the complete immutable process-launch boundary for
// one detached executor. A Backend owns exactly one configured launcher; it
// never races or falls back to another launch mechanism.
type ExecutorLaunchRequest struct {
	BackendID         string
	BackendGeneration int64
	Executable        string
	Arguments         []string
	WorkingDirectory  string
	LogPath           string
}

type ExecutorLauncher interface {
	Launch(ExecutorLaunchRequest) error
}

// SystemdUserExecutorLauncher places every executor in its own transient user
// service. The service is outside the web controller's cgroup, so a web
// service restart cannot terminate an accepted computation. The executor's
// own idle/close protocol remains the sole normal lifetime authority.
type SystemdUserExecutorLauncher struct {
	systemdRun  string
	meminfoPath string
}

func NewSystemdUserExecutorLauncher() (*SystemdUserExecutorLauncher, error) {
	path, err := exec.LookPath("systemd-run")
	if err != nil {
		return nil, errors.New("systemd-run is required for restart-surviving detached execution")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, errors.New("systemd-run path is invalid")
	}
	return &SystemdUserExecutorLauncher{systemdRun: path, meminfoPath: "/proc/meminfo"}, nil
}

const (
	executorMemoryFloorBytes      = int64(768 << 20)
	executorControlReserveBytes   = int64(3 << 30)
	executorMemoryTotalFraction   = int64(45)
	executorMemoryReserveFraction = int64(40)
	executorMemoryHighFraction    = int64(85)
	executorMemorySwapMaxBytes    = int64(512 << 20)
)

// executorMemoryLimits protects the long-lived control plane from one
// scientific process without imposing a wall-clock deadline on that process.
// Percent-based systemd limits are evaluated against the user manager and can
// consume nearly all of a constrained WSL VM. Resolve an absolute per-executor
// budget from both total and currently available memory instead.
func executorMemoryLimits(path string) (highBytes, maxBytes int64, err error) {
	if strings.TrimSpace(path) == "" {
		path = "/proc/meminfo"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read machine memory information: %w", err)
	}
	values := make(map[string]int64, 2)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		kilobytes, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil || kilobytes <= 0 || kilobytes > (1<<62)/1024 {
			return 0, 0, errors.New("machine memory information is invalid")
		}
		values[fields[0]] = kilobytes * 1024
	}
	total, available := values["MemTotal:"], values["MemAvailable:"]
	if total <= 0 || available <= 0 || available > total {
		return 0, 0, errors.New("machine memory information is incomplete")
	}
	reserve := total * executorMemoryReserveFraction / 100
	if reserve < executorControlReserveBytes {
		reserve = executorControlReserveBytes
	}
	byTotal := total * executorMemoryTotalFraction / 100
	byAvailable := available - reserve
	maxBytes = byTotal
	if byAvailable < maxBytes {
		maxBytes = byAvailable
	}
	if maxBytes < executorMemoryFloorBytes {
		maxBytes = executorMemoryFloorBytes
	}
	if maxBytes >= total {
		return 0, 0, errors.New("machine memory cannot preserve the control-plane reserve")
	}
	highBytes = maxBytes * executorMemoryHighFraction / 100
	if highBytes <= 0 || highBytes >= maxBytes {
		return 0, 0, errors.New("computed executor memory limits are invalid")
	}
	return highBytes, maxBytes, nil
}

func (launcher *SystemdUserExecutorLauncher) Launch(request ExecutorLaunchRequest) error {
	if launcher == nil || !filepath.IsAbs(launcher.systemdRun) ||
		!filepath.IsAbs(request.Executable) || !filepath.IsAbs(request.WorkingDirectory) || !filepath.IsAbs(request.LogPath) ||
		!validExecutorUnitComponent(request.BackendID) || request.BackendGeneration < 1 {
		return errors.New("systemd detached executor launch authority is invalid")
	}
	memoryHigh, memoryMax, err := executorMemoryLimits(launcher.meminfoPath)
	if err != nil {
		return err
	}
	unit := "synon-kernel-executor-" + strings.TrimPrefix(request.BackendID, "kernel-backend-") +
		"-g" + strconv.FormatInt(request.BackendGeneration, 10) + ".service"
	arguments := []string{
		"--user", "--quiet", "--collect", "--service-type=exec", "--unit=" + unit,
		"--working-directory=" + request.WorkingDirectory,
		"--description=Synon detached kernel " + request.BackendID,
		"--property=Restart=no",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=25s",
		"--property=UMask=0077",
		"--property=Nice=5",
		"--property=CPUWeight=80",
		"--property=IOWeight=80",
		// Keep one scientific executor from exhausting the host and making the
		// Web/API control plane look alive while it cannot answer. These are
		// resource-isolation limits, not task deadlines: a bounded-memory job may
		// run indefinitely, and oversized work receives a recoverable cell OOM.
		"--property=MemoryHigh=" + strconv.FormatInt(memoryHigh, 10),
		"--property=MemoryMax=" + strconv.FormatInt(memoryMax, 10),
		// Unbounded swap turns a mathematically oversized dense allocation into
		// hours of host-wide thrashing while the task still appears healthy.
		// Keep a small spill budget for transient peaks; after that the cell gets
		// a recoverable OOM and the long-lived executor remains available.
		"--property=MemorySwapMax=" + strconv.FormatInt(executorMemorySwapMaxBytes, 10),
		// One memory-heavy cell may be killed by the kernel, but the executor
		// must remain alive long enough to persist the exact failure and accept
		// a materially smaller recovery. Stopping the whole unit erased that
		// diagnostic and turned a recoverable resource decision into a task loss.
		"--property=OOMPolicy=continue",
		"--property=StandardOutput=append:" + request.LogPath,
		"--property=StandardError=append:" + request.LogPath,
		"--", request.Executable,
	}
	arguments = append(arguments, request.Arguments...)
	command := exec.Command(launcher.systemdRun, arguments...)
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 1024 {
			detail = detail[len(detail)-1024:]
		}
		return fmt.Errorf("start detached kernel executor service: %w: %s", err, detail)
	}
	return nil
}

func validExecutorUnitComponent(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// DefaultSharedSocketRoot resolves the per-user runtime directory shared by
// independent systemd services. Unlike PrivateTmp=/tmp, this namespace remains
// visible to both the web controller and detached executor after a restart.
func DefaultSharedSocketRoot(homeDir string) (string, error) {
	homeDir = filepath.Clean(strings.TrimSpace(homeDir))
	runtimeDir := filepath.Clean(strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")))
	if !filepath.IsAbs(homeDir) || !filepath.IsAbs(runtimeDir) {
		return "", errors.New("shared detached kernel runtime directory is unavailable")
	}
	info, err := os.Stat(runtimeDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("shared detached kernel runtime directory is not private")
	}
	digest := sha256.Sum256([]byte(homeDir))
	root := filepath.Join(runtimeDir, "synon-kernel-"+hex.EncodeToString(digest[:6]))
	longest := filepath.Join(root, "kernel-backend-"+strings.Repeat("0", 36)+".sock")
	if len([]byte(longest)) > maxUnixSocketPathBytes {
		return "", errors.New("shared detached kernel socket root exceeds the unix path budget")
	}
	return root, nil
}
