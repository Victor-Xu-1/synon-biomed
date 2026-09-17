package kernel

import (
	"bytes"
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
)

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
	command.Stdout, command.Stderr = io.MultiWriter(output, progress), io.MultiWriter(output, progress)
	if err := runWorkerProcess(command); err != nil {
		progress.Flush()
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
		"PATH":            filepath.Dir(m.config.Micromamba), "PYTHONNOUSERSITE": "1",
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
	}))
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
	command.Env = kernelEnvironment(map[string]string{"CONDA_PREFIX": prefix, "PATH": filepath.Join(prefix, "bin"), "PYTHONNOUSERSITE": "1"})
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed environment smoke failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil || !result.OK {
		return errors.New("managed environment smoke returned an invalid result")
	}
	return nil
}

func managedEnvironmentRuntimeEnv(prefix string) []string {
	threadLimit := strconv.Itoa(managedEnvironmentRuntimeThreadLimit())
	return kernelEnvironment(map[string]string{
		"CONDA_PREFIX":           prefix,
		"PATH":                   filepath.Join(prefix, "bin"),
		"PYTHONNOUSERSITE":       "1",
		"PIP_CONFIG_FILE":        os.DevNull,
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

func managedEnvironmentInstallerRuntimeEnv(prefix string) []string {
	environment := managedEnvironmentRuntimeEnv(prefix)
	hostPath := strings.TrimSpace(os.Getenv("PATH"))
	if hostPath != "" {
		buildPath := filepath.Join(prefix, "bin") + string(os.PathListSeparator) + hostPath
		for index, item := range environment {
			if strings.HasPrefix(item, "PATH=") {
				environment[index] = "PATH=" + buildPath
				break
			}
		}
	}
	return managedEnvironmentInstallerProxyEnv(environment)
}

// managedEnvironmentInstallerProxyEnv forwards only the conventional proxy
// variables needed by package managers. Runtime kernels do not inherit them,
// so a dependency installer can work on proxy-required networks without
// turning proxy credentials into ambient task authority.
func managedEnvironmentInstallerProxyEnv(environment []string) []string {
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	} {
		value, found := os.LookupEnv(key)
		if !found || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			continue
		}
		environment = append(environment, key+"="+value)
	}
	return environment
}

func validateManagedEnvironmentImports(ctx context.Context, language, prefix string, imports []string) error {
	if len(imports) == 0 {
		return nil
	}
	if language != "python" {
		return errors.New("import_names require a Python environment")
	}
	raw, err := json.Marshal(imports)
	if err != nil {
		return errors.New("managed environment import witness is invalid")
	}
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	// Imports are executed under a redirected stdout so third-party packages
	// cannot corrupt the witness envelope with warnings or import-time logs.
	// The final sentinel is deliberately plain and unambiguous; this is the
	// Reference import witness, not a best-effort parse of arbitrary output.
	script := "import contextlib,io,json,sys\nimport importlib\nbuf=io.StringIO()\nwith contextlib.redirect_stdout(buf):\n [importlib.import_module(name) for name in json.loads(sys.argv[1])]\nprint('SYNON_IMPORT_WITNESS_OK')"
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", script, string(raw))
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed environment import witness failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	validSentinel := len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "SYNON_IMPORT_WITNESS_OK"
	if !validSentinel {
		// Keep compatibility with older managed generations whose tiny Python
		// witness emitted the legacy JSON envelope; new generations always use
		// the sentinel above.
		for index := len(lines) - 1; index >= 0; index-- {
			var legacy struct {
				OK bool `json:"ok"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(lines[index])), &legacy) == nil && legacy.OK {
				validSentinel = true
				break
			}
		}
	}
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
