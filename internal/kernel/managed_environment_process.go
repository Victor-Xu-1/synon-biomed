package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/processsupervisor"
)

// ManagedEnvironmentInstallerInactivityError is a terminal, recoverable
// process-supervision result. It is distinct from a wall-clock deadline: the
// installer exceeded the window without output, CPU work, process-tree
// changes, or I/O.
type ManagedEnvironmentInstallerInactivityError struct {
	Duration time.Duration
	Cause    error
}

func (e *ManagedEnvironmentInstallerInactivityError) Error() string {
	if e == nil || e.Duration <= 0 {
		return "managed environment installer stopped making observable progress"
	}
	return fmt.Sprintf("managed environment installer stopped making observable progress for %s", e.Duration)
}

func (e *ManagedEnvironmentInstallerInactivityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type managedEnvironmentActivityWriter struct {
	writer   io.Writer
	watchdog *processsupervisor.InactivityWatchdog
}

func (w managedEnvironmentActivityWriter) Write(value []byte) (int, error) {
	written, err := w.writer.Write(value)
	if written > 0 && w.watchdog != nil {
		w.watchdog.MarkActivity()
	}
	return written, err
}

func (m *Manager) runManagedEnvironmentCommand(ctx context.Context, arguments ...string) error {
	return m.runManagedEnvironmentProcessWithEnv(ctx, m.config.Micromamba, m.managedEnvironmentInstallerEnv(), arguments...)
}

func (m *Manager) runManagedEnvironmentProcess(ctx context.Context, executable string, arguments ...string) error {
	return m.runManagedEnvironmentProcessWithEnv(ctx, executable, m.managedEnvironmentInstallerEnv(), arguments...)
}

func (m *Manager) runManagedEnvironmentProcessWithEnv(ctx context.Context, executable string, environment []string, arguments ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	command := newWorkerProcessCommand(ctx, executable, arguments...)
	command.Env = environment
	output := newTailBuffer(maxDiagnosticBytes)
	progress := newManagedEnvironmentProgressObserver(ctx, executable, arguments)
	inactivityTimeout := processsupervisor.DefaultInactivityTimeout
	if m != nil && m.config.ManagedEnvironmentInstallerInactivityTimeout > 0 {
		inactivityTimeout = m.config.ManagedEnvironmentInstallerInactivityTimeout
	}
	watchdog := processsupervisor.NewInactivityWatchdog(inactivityTimeout)
	activityWriter := managedEnvironmentActivityWriter{writer: io.MultiWriter(output, progress), watchdog: watchdog}
	command.Stdout, command.Stderr = activityWriter, activityWriter
	process, err := startWorkerProcess(command)
	if err == nil {
		defer process.close()
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		err = watchdog.Wait(ctx, command.Process.Pid, done, process.kill)
	}
	if err != nil {
		progress.Flush()
		var inactivity *processsupervisor.InactivityError
		if errors.As(err, &inactivity) {
			err = &ManagedEnvironmentInstallerInactivityError{Duration: inactivity.Duration, Cause: err}
		}
		return fmt.Errorf("managed environment operation failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	progress.Complete()
	return nil
}

func (m *Manager) managedEnvironmentInstallerEnv() []string {
	threadLimit := strconv.Itoa(managedEnvironmentInstallerThreadLimit())
	return managedEnvironmentInstallerProxyEnv(kernelEnvironment(map[string]string{
		"HOME": m.config.CondaHome, "MAMBA_ROOT_PREFIX": m.config.CondaHome,
		"CONDA_PKGS_DIRS": filepath.Join(m.config.CondaHome, "pkgs"),
		"PATH":            managedExecutableSearchPath(filepath.Dir(m.config.Micromamba)), "PYTHONNOUSERSITE": "1",
		"MAMBA_DOWNLOAD_THREADS": threadLimit, "MAMBA_EXTRACT_THREADS": threadLimit,
		"CMAKE_BUILD_PARALLEL_LEVEL": threadLimit, "MAX_JOBS": threadLimit,
		"OMP_NUM_THREADS": threadLimit, "OPENBLAS_NUM_THREADS": threadLimit,
		"MKL_NUM_THREADS": threadLimit, "NUMEXPR_NUM_THREADS": threadLimit,
		"RAYON_NUM_THREADS": threadLimit,
		// Managed workers deliberately cannot change host CPU affinity. Older
		// Intel OpenMP/MKL builds otherwise call pthread_setaffinity_np during
		// import and abort before scientific code starts. Disable binding at the
		// runtime boundary instead of making every task guess library-specific
		// repair variables after a crash.
		"KMP_AFFINITY": "disabled", "OMP_PROC_BIND": "false",
	}), m.config.InstallerProxy)
}

func managedEnvironmentInstallerThreadLimit() int {
	// Installer extraction and native build steps can otherwise consume every
	// logical CPU and make the interactive application unresponsive. Reserve
	// most host capacity for the UI, runner, and OS while still allowing bounded
	// parallel package work on larger machines.
	limit := runtime.GOMAXPROCS(0) / 3
	if limit < 1 {
		return 1
	}
	if limit > 4 {
		return 4
	}
	return limit
}

func smokeManagedEnvironment(ctx context.Context, language, prefix string) error {
	var executable string
	var arguments []string
	var err error
	switch language {
	case "python":
		executable, err = managedPythonExecutableAtPrefix(prefix)
		if err != nil {
			return err
		}
		arguments = []string{"-I", "-c", "import json,sys;print(json.dumps({'ok':True,'version':list(sys.version_info[:3])},sort_keys=True))"}
	case "r":
		executable = filepath.Join(prefix, "bin", executableName("Rscript"))
		arguments = []string{"--no-init-file", "--no-environ", "--no-site-file", "-e", `cat('{\"ok\":true}')`}
	default:
		return errors.New("managed environment language is invalid")
	}
	info, err := os.Stat(executable)
	if err != nil || !executableRegularFile(info) {
		return errors.New("managed environment interpreter is unavailable")
	}
	command := newWorkerProcessCommand(ctx, executable, arguments...)
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	stdout := newTailBuffer(maxDiagnosticBytes)
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed environment smoke failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil || !result.OK {
		return errors.New("managed environment smoke returned an invalid result")
	}
	return nil
}

func managedEnvironmentRuntimeEnv(prefix string) []string {
	threadLimit := strconv.Itoa(managedEnvironmentRuntimeThreadLimit())
	return kernelEnvironment(map[string]string{
		"CONDA_PREFIX":           prefix,
		"PATH":                   managedExecutableSearchPath(filepath.Join(prefix, "bin")),
		"PYTHONNOUSERSITE":       "1",
		"PIP_CONFIG_FILE":        os.DevNull,
		"R_LIBS_USER":            os.DevNull,
		"R_LIBS_SITE":            os.DevNull,
		"OMP_NUM_THREADS":        threadLimit,
		"OMP_THREAD_LIMIT":       threadLimit,
		"OPENBLAS_NUM_THREADS":   threadLimit,
		"MKL_NUM_THREADS":        threadLimit,
		"BLIS_NUM_THREADS":       threadLimit,
		"VECLIB_MAXIMUM_THREADS": threadLimit,
		"NUMEXPR_NUM_THREADS":    threadLimit,
		"RAYON_NUM_THREADS":      threadLimit,
		"KMP_AFFINITY":           "disabled",
		"OMP_PROC_BIND":          "false",
	})
}

func managedEnvironmentRuntimeThreadLimit() int {
	// Scientific libraries otherwise size native thread pools from every host
	// core. Reserve capacity for the Web service, persistence, and OS so a long
	// calculation cannot starve its own control plane.
	limit := runtime.GOMAXPROCS(0) / 3
	if limit < 1 {
		return 1
	}
	if limit > 4 {
		return 4
	}
	return limit
}

func managedEnvironmentInstallerRuntimeEnv(prefix string, configured ...string) []string {
	return managedEnvironmentInstallerProxyEnv(managedEnvironmentRuntimeEnv(prefix), configured...)
}

// Package hooks and interpreter launchers need OS utilities as well as the
// selected environment. Preserve the service's executable search contract,
// excluding relative/current-directory entries; never inherit all variables.
func managedExecutableSearchPath(preferred string) string {
	paths := append([]string{preferred}, filepath.SplitList(os.Getenv("PATH"))...)
	result := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, entry := range paths {
		if !filepath.IsAbs(entry) || strings.ContainsAny(entry, "\x00\r\n") {
			continue
		}
		entry = filepath.Clean(entry)
		if !seen[entry] {
			seen[entry] = true
			result = append(result, entry)
		}
	}
	return strings.Join(result, string(os.PathListSeparator))
}

// managedEnvironmentInstallerProxyEnv forwards only the conventional proxy
// variables needed by package managers. Runtime kernels do not inherit them,
// so a dependency installer can work on proxy-required networks without
// turning proxy credentials into ambient task authority.
func managedEnvironmentInstallerProxyEnv(environment []string, configured ...string) []string {
	result := append([]string(nil), environment...)
	set := func(key, value string) {
		for index, item := range result {
			name, _, ok := strings.Cut(item, "=")
			if ok && name == key {
				result[index] = key + "=" + value
				return
			}
		}
		result = append(result, key+"="+value)
	}
	keys := []string{
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	}
	for _, key := range keys {
		value, found := os.LookupEnv(key)
		if !found || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			continue
		}
		set(key, value)
	}
	if len(configured) > 0 {
		value := strings.TrimSpace(configured[0])
		if value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n") {
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
				set(key, value)
			}
		}
	}
	return result
}

func validateManagedEnvironmentImports(ctx context.Context, language, prefix string, imports []string) error {
	if len(imports) == 0 {
		return nil
	}
	if _, err := validateManagedImportNames(imports); err != nil {
		return err
	}
	raw, err := json.Marshal(imports)
	if err != nil {
		return errors.New("managed environment import witness is invalid")
	}
	// Import-time logs are bounded in the parent. Only successful process exit
	// plus the verifier's final sentinel satisfies this runtime witness.
	script := "import importlib,json,sys\nfor name in json.loads(sys.argv[1]):\n importlib.import_module(name)\nprint('SYNON_IMPORT_WITNESS_OK')"
	var executable string
	var arguments []string
	switch language {
	case "python":
		executable, err = managedPythonExecutableAtPrefix(prefix)
		if err != nil {
			return err
		}
		arguments = []string{"-I", "-c", script, string(raw)}
	case "r":
		executable = filepath.Join(prefix, "bin", executableName("Rscript"))
		// Conda distribution names are lowercase; resolve the exact runtime
		// namespace from R's installed metadata rather than guessing its case.
		script = `requested <- commandArgs(TRUE)
available <- rownames(installed.packages())
for (name in requested) {
  matches <- available[tolower(available) == tolower(name)]
  if (length(matches) != 1L || !requireNamespace(matches[[1]], quietly=TRUE)) stop(paste("runtime package unavailable:", name))
}
cat("SYNON_IMPORT_WITNESS_OK\n")`
		arguments = append([]string{"--no-init-file", "--no-environ", "--no-site-file", "-e", script}, imports...)
	default:
		return errors.New("managed environment import witness language is unsupported")
	}
	command := newWorkerProcessCommand(ctx, executable, arguments...)
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	stdout := newTailBuffer(maxDiagnosticBytes)
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed environment import witness failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	validSentinel := len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "SYNON_IMPORT_WITNESS_OK"
	if !validSentinel {
		return errors.New("managed environment import witness returned an invalid result")
	}
	return nil
}

func validateManagedEnvironmentPublication(
	ctx context.Context,
	language, prefix string,
	packages, imports []string,
) error {
	if err := smokeManagedEnvironment(ctx, language, prefix); err != nil {
		return err
	}
	if language == "r" {
		if err := validateManagedRPackageInventory(ctx, prefix); err != nil {
			return err
		}
	}
	if err := validateManagedEnvironmentImports(ctx, language, prefix, imports); err != nil {
		return err
	}
	return validateManagedEnvironmentBinaryCompatibility(ctx, language, prefix, packages)
}

func (m *Manager) validateRecoveredManagedEnvironment(ctx context.Context, recovered recoveredManagedEnvironment) error {
	marker, err := readManagedEnvironmentMarker(recovered.path)
	if err != nil {
		return err
	}
	prefix := recovered.path
	if marker.Kind == "path-venv" {
		prefix = marker.RuntimePath
	}
	return m.validateManagedEnvironmentGeneration(
		ctx, marker.Generation, marker.Language, prefix, marker.Packages, marker.ImportNames,
	)
}
