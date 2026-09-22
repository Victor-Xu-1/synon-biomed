package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

var (
	managedEnvironmentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	rVersionContract       = regexp.MustCompile(`(?m)^(Rscript \(R\)|R) version ([0-9]+)\.([0-9]+)\.[0-9]+( (Patched|alpha|beta|RC|Under development \(unstable\)))? \([0-9]{4}-[0-9]{2}-[0-9]{2}\)\r?$`)
)

// ValidEnvironmentName implements the public managed-environment identifier
// contract shared by the HTTP boundary and the runtime path resolver.
func ValidEnvironmentName(value string) bool {
	return value != "" && len([]byte(value)) <= 100 && managedEnvironmentName.MatchString(value)
}

type RuntimeEnvironmentStatus struct {
	EnvironmentName string
	Language        string
	PackageCount    int
	Status          string
	Error           string
	Phase           string
	StartedAt       time.Time
	LastProgressAt  time.Time
}

func (m *Manager) startSessionWorker(spec SessionSpec) (*Worker, error) {
	if err := m.Verify(); err != nil {
		return nil, err
	}
	if spec.Language == "r" {
		// Keep the per-session library identity aligned with the same canonical
		// R environment used by executable resolution and runtime variables.
		spec.Environment = m.canonicalManagedREnvironment(spec.Environment)
	}
	if spec.Language == "r" {
		library, err := rSessionLibrary(spec)
		if err != nil {
			return nil, err
		}
		if err := os.RemoveAll(library); err != nil {
			return nil, fmt.Errorf("reset R session library: %w", err)
		}
		if err := os.MkdirAll(library, 0o700); err != nil {
			return nil, fmt.Errorf("create R session library: %w", err)
		}
	}
	executable, arguments, environment, internalMounts, err := m.sessionRuntimeWithMounts(spec)
	if err != nil {
		return nil, err
	}
	defer closeFrozenWorkerMounts(internalMounts)
	mounts := append(append([]WorkerMount(nil), spec.Mounts...), internalMounts...)
	proxy, err := startKernelEgressProxy(
		spec.WorkspaceDir, spec.KernelID, spec.EgressAllowedDomains, spec.EgressDeniedDomains,
		spec.UpstreamProxy,
	)
	if err != nil {
		return nil, err
	}
	if proxy != nil {
		forwarderDirectory := filepath.Join(m.config.AssetRoot, "kernels")
		if !kernelMountsCoverPath(mounts, forwarderDirectory) {
			mounts = append(mounts, TrustedReadOnlyDirectoryMount(forwarderDirectory))
		}
		if spec.CABundle != "" && !kernelMountsCoverPath(mounts, spec.CABundle) {
			info, statErr := os.Stat(spec.CABundle)
			if statErr != nil || !info.Mode().IsRegular() {
				proxy.Close()
				return nil, errors.New("kernel CA bundle is unavailable")
			}
			mounts = append(mounts, WorkerMount{Path: spec.CABundle, regular: true, trusted: true})
		}
		executable, arguments, environment, err = m.wrapKernelEgressRuntime(
			executable, arguments, environment, proxy.port, spec.CABundle,
		)
		if err != nil {
			proxy.Close()
			return nil, err
		}
	}
	worker, err := m.startWorkerWithRuntime(
		spec.KernelID, spec.WorkspaceDir, executable, arguments, environment,
		mounts, spec.ProtectedPaths, sessionRestartKey(spec), spec.Language, nil, proxy,
	)
	if err != nil {
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	if proxy != nil && worker.egressProxy != proxy {
		proxy.Close()
	}
	return worker, nil
}

func kernelMountsCoverPath(mounts []WorkerMount, target string) bool {
	target = filepath.Clean(target)
	for _, mount := range mounts {
		relative, err := filepath.Rel(filepath.Clean(mount.Path), target)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (m *Manager) wrapKernelEgressRuntime(
	executable string,
	arguments, environment []string,
	port int,
	caBundle string,
) (string, []string, []string, error) {
	forwarder := filepath.Join(m.config.AssetRoot, "kernels", "kernel_egress_forwarder.py")
	if info, err := os.Stat(forwarder); err != nil || !info.Mode().IsRegular() {
		return "", nil, nil, errors.New("kernel egress forwarder is unavailable")
	}
	const script = `
set -eu
/usr/bin/python3 "$SYNON_KERNEL_EGRESS_FORWARDER" </dev/null >/dev/null 2>&1 &
proxy_pid=$!
for attempt in $(seq 1 100); do
  if /usr/bin/python3 -c 'import os,socket; s=socket.create_connection(("127.0.0.1",int(os.environ["SYNON_KERNEL_EGRESS_PORT"])),0.1); s.close()' 2>/dev/null; then
    exec "$@"
  fi
  sleep 0.01
done
kill "$proxy_pid" 2>/dev/null || true
exit 70
`
	wrapperArguments := []string{"-c", script, "synon-kernel-egress", executable}
	wrapperArguments = append(wrapperArguments, arguments...)
	if port < 1024 || port > 65535 {
		return "", nil, nil, errors.New("kernel egress loopback port is invalid")
	}
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	overrides := map[string]string{
		"SYNON_KERNEL_EGRESS_PORT":      fmt.Sprintf("%d", port),
		"SYNON_KERNEL_EGRESS_FORWARDER": forwarder,
		"HTTP_PROXY":                    proxyURL, "HTTPS_PROXY": proxyURL,
		"http_proxy": proxyURL, "https_proxy": proxyURL,
		"NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1",
		"GIT_SSH_COMMAND": "/bin/false",
	}
	if caBundle != "" {
		overrides["SSL_CERT_FILE"] = caBundle
		overrides["REQUESTS_CA_BUNDLE"] = caBundle
		overrides["CURL_CA_BUNDLE"] = caBundle
	}
	return "/bin/sh", wrapperArguments, mergeKernelEnvironment(environment, overrides), nil
}

func mergeKernelEnvironment(environment []string, overrides map[string]string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for key := range overrides {
		keys[key] = struct{}{}
	}
	merged := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if _, replaced := keys[key]; !replaced {
			merged = append(merged, entry)
		}
	}
	for key, value := range overrides {
		merged = append(merged, key+"="+value)
	}
	sort.Strings(merged)
	return merged
}

func (m *Manager) sessionRuntime(spec SessionSpec) (string, []string, []string, error) {
	executable, arguments, environment, mounts, err := m.sessionRuntimeWithMounts(spec)
	closeFrozenWorkerMounts(mounts)
	return executable, arguments, environment, err
}

func (m *Manager) sessionRuntimeWithMounts(spec SessionSpec) (string, []string, []string, []WorkerMount, error) {
	switch spec.Language {
	case "python":
		python := m.config.Python
		prefix := ""
		if !isSystemPythonEnvironment(spec.Environment) {
			var err error
			if spec.KernelKind == "bash" {
				// Bash uses Python only for the trusted protocol supervisor. The
				// selected scientific environment supplies PATH and native tools,
				// not the supervisor's interpreter or dependencies.
				prefix, err = m.managedEnvironmentPrefix(spec.Environment)
			} else {
				prefix, python, err = m.managedEnvironmentRuntime(spec.Environment, "python")
			}
			if err != nil {
				return "", nil, nil, nil, fmt.Errorf("resolve Python environment %q: %w", spec.Environment, err)
			}
			if err := m.validateSessionRuntimeGeneration(spec); err != nil {
				return "", nil, nil, nil, err
			}
		}
		return python, pythonWorkerArguments(m.config.WorkerPath), m.runtimeEnvironmentAtPrefix(spec.Environment, "python", spec.WorkspaceDir, spec.KernelID, "", "", "", prefix), nil, nil
	case "r":
		worker := strings.TrimSpace(m.config.RWorkerPath)
		if worker == "" {
			return "", nil, nil, nil, errors.New("R kernel worker is not configured")
		}
		info, err := os.Stat(worker)
		if err != nil {
			return "", nil, nil, nil, fmt.Errorf("R kernel worker is unavailable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", nil, nil, nil, errors.New("R kernel worker is not a regular file")
		}
		prefix, rscript, err := m.managedEnvironmentRuntime(spec.Environment, "Rscript")
		if err != nil {
			return "", nil, nil, nil, fmt.Errorf("resolve R environment %q: %w", spec.Environment, err)
		}
		if err := m.validateSessionRuntimeGeneration(spec); err != nil {
			return "", nil, nil, nil, err
		}
		if len(m.config.RSharedPackages) > 0 {
			repairContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = m.repairRSharedLibrary(repairContext, rscript)
			cancel()
		}
		opLogPath := ""
		mounts := []WorkerMount{}
		if !m.config.DisableROperationLog {
			var opLogFile *os.File
			opLogPath, opLogFile, err = m.ensureROperationLog(spec.Environment)
			if err != nil {
				return "", nil, nil, nil, err
			}
			mounts = append(mounts, WorkerMount{Path: opLogPath, Writable: true, regular: true, trusted: true, frozen: opLogFile})
		}
		sharedLibrary, sharedFile, diagnostic := m.rSharedLibrary(rscript)
		if sharedLibrary != "" {
			mounts = append(mounts, WorkerMount{Path: sharedLibrary, trusted: true, frozen: sharedFile})
		}
		return rscript, []string{"--no-init-file", "--no-environ", "--no-site-file", worker},
			m.runtimeEnvironmentAtPrefix(spec.Environment, "r", spec.WorkspaceDir, spec.KernelID, opLogPath, sharedLibrary, diagnostic, prefix), mounts, nil
	default:
		return "", nil, nil, nil, fmt.Errorf("unsupported kernel language %q", spec.Language)
	}
}

// validateSessionRuntimeGeneration revalidates the same immutable generation
// authority used at execution admission. The bundled scientific Python
// runtime has a stricter, content-addressed marker contract than user-managed
// environments, so routing it through the generic environment marker reader
// would deterministically reject a valid runtime before its worker starts.
func (m *Manager) validateSessionRuntimeGeneration(spec SessionSpec) error {
	expected := strings.TrimSpace(spec.RuntimeGeneration)
	if expected == "" {
		return nil
	}
	actual := ""
	found := true
	var err error
	if spec.Language == "python" && spec.Environment == managedPythonName(m.config) {
		actual, err = m.ManagedPythonActiveGeneration()
	} else if spec.Language == "r" && m.canonicalManagedREnvironment(spec.Environment) == managedRName(m.config) {
		actual, err = m.ManagedRActiveGeneration()
	} else {
		actual, found, err = m.ManagedEnvironmentActiveGeneration(spec.Environment)
	}
	if err != nil || !found || actual != expected {
		language := strings.TrimSpace(spec.Language)
		if language == "" {
			language = "runtime"
		}
		return fmt.Errorf("managed %s generation changed before worker start", language)
	}
	return nil
}

// RuntimeReady is a read-only capability check used before advertising a
// language tool. It never repairs or creates an environment.
func (m *Manager) RuntimeReady(language, environment string) bool {
	if m == nil {
		return false
	}
	language = strings.ToLower(strings.TrimSpace(language))
	switch language {
	case "python":
		if environment == "" {
			environment = "python"
		}
		if isSystemPythonEnvironment(environment) {
			_, err := absoluteExecutable(m.config.Python)
			return err == nil
		}
		if environment == managedPythonName(m.config) {
			return m.managedPythonRuntimeReady() == nil
		}
		_, err := m.managedEnvironmentExecutable(environment, "python")
		return err == nil
	case "r":
		requestedEnvironment := strings.TrimSpace(environment)
		environment = m.canonicalManagedREnvironment(environment)
		worker := strings.TrimSpace(m.config.RWorkerPath)
		if worker == "" {
			return false
		}
		info, err := os.Stat(worker)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		if environment == managedRName(m.config) {
			// Keep a narrow read-only migration path for an older `r` prefix when
			// the verified bundled installer is not configured. The service-owned
			// bundled path remains authoritative whenever provisioning is enabled;
			// execution admission still calls EnsureManagedREnvironment and never
			// treats this legacy fallback as the required core runtime.
			if !m.ManagedRProvisioningEnabled() &&
				(requestedEnvironment == "r" || requestedEnvironment == "claude-science-r") {
				return m.legacyRExecutableAvailable(requestedEnvironment)
			}
			return m.managedRRuntimeReady() == nil
		}
		_, err = m.managedEnvironmentExecutable(environment, "Rscript")
		return err == nil
	default:
		return false
	}
}

func isSystemPythonEnvironment(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "python", "system", "repl", "operon":
		return true
	default:
		return false
	}
}

func (m *Manager) managedEnvironmentExecutable(environment, executable string) (string, error) {
	_, resolved, err := m.managedEnvironmentRuntime(environment, executable)
	return resolved, err
}

func (m *Manager) managedEnvironmentRuntime(environment, executable string) (string, string, error) {
	requestedEnvironment := environment
	if strings.EqualFold(strings.TrimSpace(executable), "Rscript") {
		environment = m.canonicalManagedREnvironment(environment)
	}
	prefix, err := m.managedEnvironmentPrefix(environment)
	if err != nil {
		return "", "", err
	}
	for _, candidate := range environmentExecutableCandidates(prefix, executable) {
		info, err := os.Stat(candidate)
		if err == nil && executableRegularFile(info) {
			resolved, resolveErr := filepath.EvalSymlinks(candidate)
			if resolveErr != nil {
				return "", "", resolveErr
			}
			return prefix, resolved, nil
		}
	}
	return "", "", fmt.Errorf("%s executable is not installed in managed environment %s", executable, requestedEnvironment)
}

func (m *Manager) canonicalManagedREnvironment(environment string) string {
	environment = strings.TrimSpace(environment)
	if environment == "" || environment == "r" || environment == "claude-science-r" {
		return managedRName(m.config)
	}
	return environment
}

func (m *Manager) managedEnvironmentPrefix(environment string) (string, error) {
	if !ValidEnvironmentName(environment) {
		return "", errors.New("environment name must be a bounded path-free identifier")
	}
	root := strings.TrimSpace(m.config.CondaEnvsPath)
	if root == "" {
		return "", errors.New("managed environment root is not configured")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("managed environment root is unavailable")
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(root, environment))
	if err != nil {
		return "", fmt.Errorf("managed environment %s is unavailable", environment)
	}
	relative, err := filepath.Rel(resolvedRoot, prefix)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("managed environment resolves outside the configured root")
	}
	if marker, markerErr := readManagedEnvironmentMarker(prefix); markerErr == nil && marker.Kind == "path-venv" {
		prefix = marker.RuntimePath
	}
	return prefix, nil
}

func environmentExecutableCandidates(prefix, executable string) []string {
	if runtime.GOOS == "windows" {
		name := executable
		if !strings.HasSuffix(strings.ToLower(name), ".exe") {
			name += ".exe"
		}
		return []string{filepath.Join(prefix, name), filepath.Join(prefix, "Scripts", name), filepath.Join(prefix, "bin", name)}
	}
	return []string{filepath.Join(prefix, "bin", executable)}
}

func managedPythonExecutableAtPrefix(prefix string) (string, error) {
	for _, candidate := range environmentExecutableCandidates(prefix, "python") {
		info, err := os.Stat(candidate)
		if err != nil || !executableRegularFile(info) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
		return resolved, nil
	}
	return "", fmt.Errorf("managed Python executable is unavailable under %s", prefix)
}

func (m *Manager) runtimeEnvironment(environment, language, workspaceDir, kernelID string) []string {
	return m.runtimeEnvironmentWithR(environment, language, workspaceDir, kernelID, "", "", "")
}

func (m *Manager) runtimeEnvironmentWithR(environment, language, workspaceDir, kernelID, opLogPath, sharedLibrary, diagnostic string) []string {
	return m.runtimeEnvironmentAtPrefix(environment, language, workspaceDir, kernelID, opLogPath, sharedLibrary, diagnostic, "")
}

func (m *Manager) runtimeEnvironmentAtPrefix(environment, language, workspaceDir, kernelID, opLogPath, sharedLibrary, diagnostic, prefix string) []string {
	if language == "r" {
		environment = m.canonicalManagedREnvironment(environment)
	}
	extra := make(map[string]string, len(m.config.Environment)+6)
	for key, value := range m.config.Environment {
		extra[key] = value
	}
	if prefix == "" {
		prefix = filepath.Join(m.config.CondaEnvsPath, environment)
		if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
			prefix = resolved
		}
	}
	if !isSystemPythonEnvironment(environment) || language == "r" {
		extra["CONDA_PREFIX"] = prefix
		extra["CONDA_DEFAULT_ENV"] = environment
		extra["PATH"] = filepath.Join(prefix, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	if strings.TrimSpace(m.config.CondaHome) != "" {
		extra["MAMBA_ROOT_PREFIX"] = m.config.CondaHome
	}
	if strings.TrimSpace(workspaceDir) != "" {
		extra["OPERON_WRITABLE_ROOTS"] = filepath.Clean(workspaceDir)
	}
	if language == "r" {
		library, err := rSessionLibrary(SessionSpec{
			KernelID: kernelID, Environment: environment, WorkspaceDir: workspaceDir,
		})
		if err == nil {
			extra["R_LIBS_USER"] = library
			extra["OPERON_DLOPEN_EXEMPT"] = library
		}
		if opLogPath != "" {
			extra["OPERON_R_OPLOG_PATH"] = opLogPath
		}
		if sharedLibrary != "" {
			extra["R_LIBS_SITE"] = sharedLibrary
		}
		if diagnostic != "" {
			extra["OPERON_R_STARTUP_DIAGNOSTIC"] = diagnostic
		}
	}
	return kernelEnvironment(extra)
}

func closeFrozenWorkerMounts(mounts []WorkerMount) {
	for _, mount := range mounts {
		if mount.frozen != nil {
			_ = mount.frozen.Close()
		}
	}
}

func (m *Manager) ensureROperationLog(environment string) (string, *os.File, error) {
	environment = m.canonicalManagedREnvironment(environment)
	if !ValidEnvironmentName(environment) {
		return "", nil, errors.New("R environment name must be a bounded path-free identifier")
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(m.config.CondaEnvsPath, environment))
	if err != nil || !filepath.IsAbs(prefix) {
		return "", nil, errors.New("R operation log environment is unavailable")
	}
	return secureROperationLog(prefix)
}

func parseRMinorVersion(output string) (string, bool) {
	match := rVersionContract.FindStringSubmatch(output)
	if len(match) < 3 {
		return "", false
	}
	return match[2] + "." + match[3], true
}

func (m *Manager) rSharedLibrary(rscript string) (string, *os.File, string) {
	base := strings.TrimSpace(m.config.RSharedLibsBase)
	if base == "" {
		return "", nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := newWorkerProcessCommand(ctx, rscript, "--version")
	output := newTailBuffer(4096)
	command.Stdout, command.Stderr = output, output
	command.Env = kernelEnvironment(nil)
	if err := command.Run(); err != nil {
		return "", nil, "operon: could not parse the R version from $RSCRIPT --version; shared default_r_packages overlay not applied."
	}
	minor, ok := parseRMinorVersion(output.String())
	if !ok {
		return "", nil, "operon: could not parse the R version from $RSCRIPT --version; shared default_r_packages overlay not applied."
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil || !filepath.IsAbs(resolvedBase) {
		return "", nil, fmt.Sprintf("operon: shared default_r_packages library root %s not visible — either the daemon has not built it yet or it is missing from the sandbox bind set.", base)
	}
	info, err := os.Stat(resolvedBase)
	if err != nil || !info.IsDir() {
		return "", nil, fmt.Sprintf("operon: shared default_r_packages library root %s not visible — either the daemon has not built it yet or it is missing from the sandbox bind set.", base)
	}
	slice := filepath.Join(resolvedBase, minor)
	info, err = os.Stat(slice)
	if err != nil || !info.IsDir() {
		return "", nil, fmt.Sprintf("operon: no shared default_r_packages library for R %s (expected %s). Shared packages are built for the skeleton 'r' env's R version; to inherit them here, recreate this environment with a matching r-base version.", minor, slice)
	}
	resolved, err := filepath.EvalSymlinks(slice)
	relative, relErr := filepath.Rel(resolvedBase, resolved)
	if err != nil || relErr != nil || !filepath.IsAbs(resolved) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", nil, "R shared library is invalid"
	}
	file, err := freezeInternalKernelDirectory(resolved)
	if err != nil {
		return "", nil, "R shared library is invalid"
	}
	return resolved, file, ""
}

func rSessionLibrary(spec SessionSpec) (string, error) {
	environment := spec.Environment
	kernelID := strings.TrimSpace(spec.KernelID)
	workspaceDir := strings.TrimSpace(spec.WorkspaceDir)
	if !ValidEnvironmentName(environment) {
		return "", errors.New("R environment name must be a bounded path-free identifier")
	}
	if kernelID == "" || len(kernelID) > 128 || strings.ContainsAny(kernelID, "/\\\x00\r\n") {
		return "", errors.New("R kernel id must be a bounded path-free identifier")
	}
	if workspaceDir == "" || !filepath.IsAbs(workspaceDir) {
		return "", errors.New("R kernel workspace must be an absolute directory")
	}
	workspaceDir = filepath.Clean(workspaceDir)
	library := filepath.Join(workspaceDir, ".r-libs", kernelID, environment)
	relative, err := filepath.Rel(workspaceDir, library)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("R session library is outside the kernel workspace")
	}
	return library, nil
}

func (m *Manager) RuntimeEnvironmentStatuses(retry bool) []RuntimeEnvironmentStatus {
	python := RuntimeEnvironmentStatus{EnvironmentName: "python-kernel-sidecar", Language: "python", Status: "ready"}
	var verifyErr error
	if retry {
		verifyErr = m.RetryVerification()
	} else {
		verifyErr = m.Verify()
	}
	if verifyErr != nil {
		python.Status, python.Error = "failed", verifyErr.Error()
	}
	managedName := managedPythonName(m.config)
	provisioning := m.ManagedPythonProvisioningDetails()
	managedPython := RuntimeEnvironmentStatus{
		EnvironmentName: managedName,
		Language:        "python",
		PackageCount:    managedPackageCount(filepath.Join(m.config.CondaEnvsPath, managedName)),
		Status:          provisioning.Status,
	}
	managedPython.Phase = provisioning.Phase
	managedPython.StartedAt = provisioning.StartedAt
	managedPython.LastProgressAt = provisioning.LastProgressAt
	if managedPython.Status == "failed" {
		managedPython.Error = provisioning.Error
		if managedPython.Error == "" {
			managedPython.Error = "managed Python scientific runtime is unavailable"
		}
	}
	rName := managedRName(m.config)
	rStatus := m.runtimeRStatus(rName)
	rStatus.PackageCount = managedPackageCount(filepath.Join(m.config.CondaEnvsPath, rName))
	return []RuntimeEnvironmentStatus{python, managedPython, rStatus}
}

// runtimeRStatus keeps the public status useful while a legacy/unconfigured
// manager is being migrated. It never installs or activates that legacy path;
// the write authority remains the verified bundled R generation. Once the
// bundled catalog is configured, only its provisioning state is reported.
func (m *Manager) runtimeRStatus(name string) RuntimeEnvironmentStatus {
	if m.ManagedRProvisioningEnabled() {
		provisioning := m.ManagedRProvisioningDetails()
		status := RuntimeEnvironmentStatus{EnvironmentName: name, Language: "r", Status: provisioning.Status}
		status.Phase = provisioning.Phase
		status.StartedAt = provisioning.StartedAt
		status.LastProgressAt = provisioning.LastProgressAt
		if status.Status == "failed" {
			status.Error = provisioning.Error
			if status.Error == "" {
				status.Error = "managed R scientific runtime is unavailable"
			}
		}
		return status
	}
	status := RuntimeEnvironmentStatus{EnvironmentName: name, Language: "r", Status: "failed"}
	worker := strings.TrimSpace(m.config.RWorkerPath)
	if worker == "" {
		status.Error = "R kernel worker is not configured"
		return status
	}
	if info, err := os.Stat(worker); err != nil || !info.Mode().IsRegular() {
		status.Error = "R kernel worker is unavailable"
		return status
	}
	candidates := []string{name}
	legacy := strings.TrimSpace(m.config.DefaultREnv)
	if legacy == "" {
		legacy = "r"
	}
	if legacy != name {
		candidates = append(candidates, legacy)
	}
	for _, candidate := range candidates {
		if m.legacyRExecutableAvailable(candidate) {
			status.Status = "ready"
			status.Error = ""
			return status
		}
	}
	status.Error = "R environment is unavailable"
	return status
}

func (m *Manager) legacyRExecutableAvailable(environment string) bool {
	prefix, err := m.managedEnvironmentPrefix(environment)
	if err != nil {
		return false
	}
	for _, candidate := range environmentExecutableCandidates(prefix, "Rscript") {
		if info, statErr := os.Stat(candidate); statErr == nil && executableRegularFile(info) {
			return true
		}
	}
	return false
}

func (m *Manager) MicromambaError() error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	path := strings.TrimSpace(m.config.Micromamba)
	if path == "" {
		return errors.New("micromamba is not configured")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect micromamba: %w", err)
	}
	if !executableRegularFile(info) {
		return errors.New("micromamba must be an executable regular file")
	}
	return nil
}

func executableRegularFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
}

func (m *Manager) RepairDefaultREnvironment(ctx context.Context) error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	return m.RetryManagedREnvironment(ctx)
}

func (m *Manager) repairRSharedLibrary(ctx context.Context, rscript string) error {
	base := strings.TrimSpace(m.config.RSharedLibsBase)
	packages := append([]string(nil), m.config.RSharedPackages...)
	if base == "" || len(packages) == 0 {
		return nil
	}
	for index, name := range packages {
		packages[index] = strings.TrimSpace(name)
		if packages[index] == "" || len(packages[index]) > 128 || strings.ContainsAny(packages[index], "/\\\x00\r\n") {
			return errors.New("shared R package names must be bounded path-free strings")
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output := newTailBuffer(4096)
	probeCtx, cancelProbe := context.WithTimeout(ctx, 3*time.Second)
	defer cancelProbe()
	version := newWorkerProcessCommand(probeCtx, rscript, "--version")
	version.Stdout, version.Stderr, version.Env = output, output, kernelEnvironment(nil)
	if err := version.Run(); err != nil {
		return fmt.Errorf("inspect shared R package version: %w", err)
	}
	minor, ok := parseRMinorVersion(output.String())
	if !ok {
		return errors.New("inspect shared R package version: R version is invalid")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create shared R package root: %w", err)
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil || !filepath.IsAbs(base) {
		return errors.New("shared R package root is invalid")
	}
	release, err := lockKernelFile(ctx, filepath.Join(base, ".build-"+minor+".lock"))
	if err != nil {
		return fmt.Errorf("lock shared R package slice: %w", err)
	}
	defer release()
	target := filepath.Join(base, minor)
	if info, lstatErr := os.Lstat(target); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("shared R package slice is invalid")
	} else if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		return errors.New("shared R package slice is invalid")
	}
	targetExists := false
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		targetExists = true
		if m.verifyRSharedPackages(ctx, rscript, target, packages) == nil {
			return nil
		}
	} else if statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("shared R package slice is invalid")
	}
	staging, err := os.MkdirTemp(base, "00LOCK-"+minor+"-")
	if err != nil {
		return fmt.Errorf("create shared R package staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	copyScript := `args <- commandArgs(trailingOnly=TRUE); target <- args[[1L]]; requested <- args[-1L]; installed <- utils::installed.packages(); dependencies <- tools::package_dependencies(requested, db=installed, recursive=TRUE); packages <- unique(c(requested, unlist(dependencies, use.names=FALSE))); packages <- packages[packages %in% rownames(installed)]; if (!all(requested %in% packages)) quit(status=2L); dir.create(target, recursive=TRUE, showWarnings=FALSE); for (package in packages) { source <- tryCatch(find.package(package, quiet=TRUE), error=function(condition) ""); if (!nzchar(source) || !isTRUE(file.copy(source, target, recursive=TRUE, copy.mode=TRUE))) quit(status=3L) }`
	arguments := append([]string{"--vanilla", "-e", copyScript, "--args", staging}, packages...)
	if err := m.runManagedEnvironmentProcessWithEnv(ctx, rscript, kernelEnvironment(nil), arguments...); err != nil {
		return fmt.Errorf("stage shared R packages: %w", err)
	}
	if err := m.verifyRSharedPackages(ctx, rscript, staging, packages); err != nil {
		return err
	}
	if err := replaceKernelDirectory(staging, target, targetExists); err != nil {
		return fmt.Errorf("publish shared R package slice: %w", err)
	}
	return nil
}

func (m *Manager) verifyRSharedPackages(ctx context.Context, rscript, library string, packages []string) error {
	verifyScript := `args <- commandArgs(trailingOnly=TRUE); library <- args[[1L]]; requested <- args[-1L]; installed <- utils::installed.packages(); dependencies <- tools::package_dependencies(requested, db=installed, recursive=TRUE); packages <- unique(c(requested, unlist(dependencies, use.names=FALSE))); packages <- packages[packages %in% rownames(installed)]; ok <- all(requested %in% packages) && all(vapply(packages, function(package) nzchar(tryCatch(find.package(package, lib.loc=library, quiet=TRUE), error=function(condition) "")), logical(1L))); quit(status=if (ok) 0L else 4L)`
	arguments := append([]string{"--vanilla", "-e", verifyScript, "--args", library}, packages...)
	if err := m.runManagedEnvironmentProcessWithEnv(ctx, rscript, kernelEnvironment(nil), arguments...); err != nil {
		return fmt.Errorf("verify shared R packages: %w", err)
	}
	return nil
}

func managedPackageCount(prefix string) int {
	entries, err := filepath.Glob(filepath.Join(prefix, "conda-meta", "*.json"))
	if err != nil {
		return 0
	}
	sort.Strings(entries)
	if len(entries) > 100000 {
		return 100000
	}
	return len(entries)
}
