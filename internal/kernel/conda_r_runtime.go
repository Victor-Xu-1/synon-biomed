package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const managedRReadinessCacheTTL = 5 * time.Second

// managedRReadinessCache holds one successful probe for one Manager. The
// marker/pointer and executable identity are still checked on every read;
// success expires quickly, and a new process starts with no cached evidence.
type managedRReadinessCache struct {
	mu         sync.Mutex
	prefix     string
	executable string
	info       os.FileInfo
	checkedAt  time.Time
}

var managedRRequiredPackages = []string{
	"r-base",
	"r-data.table",
	"r-ggplot2",
	"r-jsonlite",
	"r-tidyverse",
}

var managedRRequiredNamespaces = []string{
	"data.table",
	"ggplot2",
	"jsonlite",
	"tidyverse",
}

type managedRRuntime struct {
	managedCondaRuntime
	activationGeneration string
	rVersion             string
}

func managedRName(config Config) string {
	switch value := canonicalCondaRuntimeName(strings.TrimSpace(config.DefaultREnv)); value {
	case "", "r":
		return defaultManagedREnvironment
	default:
		return value
	}
}

func (m *Manager) ManagedREnvironmentName() string {
	if m == nil {
		return defaultManagedREnvironment
	}
	return managedRName(m.config)
}

func (m *Manager) ManagedRProvisioningEnabled() bool {
	return m != nil && strings.TrimSpace(m.config.CondaRuntimeCatalog) != "" &&
		strings.TrimSpace(m.config.CondaEnvsPath) != "" && strings.TrimSpace(m.config.Micromamba) != "" &&
		strings.TrimSpace(m.config.ManifestPath) != "" && strings.TrimSpace(m.config.RWorkerPath) != ""
}

func (m *Manager) ManagedRProvisioningStatus() string {
	return m.ManagedRProvisioningDetails().Status
}

func (m *Manager) ManagedRProvisioningDetails() ManagedPythonProvisioningDetails {
	if m == nil {
		return ManagedPythonProvisioningDetails{Status: "unavailable"}
	}
	return managedRuntimeProvisioningDetails(&m.managedRState, m.ManagedRProvisioningEnabled(), m.managedRRuntimeReady)
}

func (m *Manager) setManagedRProvisioningPhase(phase string) {
	if m == nil || strings.TrimSpace(phase) == "" {
		return
	}
	setManagedRuntimeProvisioningPhase(&m.managedRState, phase)
}

func (m *Manager) ProvisionManagedREnvironment(ctx context.Context) error {
	if m == nil || !m.ManagedRProvisioningEnabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedRProvisioning(ctx, ctx, true, m.ensureManagedREnvironment, true)
}

func (m *Manager) EnsureManagedREnvironment(ctx context.Context) error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedRProvisioning(ctx, nil, false, m.ensureManagedREnvironment, true)
}

func (m *Manager) RetryManagedREnvironment(ctx context.Context) error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedRProvisioning(ctx, nil, true, m.ensureManagedREnvironment, true)
}

func (m *Manager) managedRProvisioning(
	waitCtx context.Context,
	installParent context.Context,
	retry bool,
	install func(context.Context) error,
	wait bool,
) error {
	ready := func() error {
		if err := m.managedRRuntimeReady(); err != nil {
			return err
		}
		prefix, err := m.ManagedRActivePrefix()
		if err != nil {
			return err
		}
		rscript, err := managedRExecutableAtPrefix(prefix)
		if err != nil {
			return err
		}
		return m.repairRSharedLibrary(waitCtx, rscript)
	}
	return m.managedRuntimeProvisioning(
		waitCtx, installParent, retry, install, wait,
		&m.managedRState, ready,
	)
}

func loadManagedRRuntime(config Config) (managedRRuntime, error) {
	runtime, err := loadManagedCondaRuntime(config, managedRName(config))
	if err != nil {
		return managedRRuntime{}, err
	}
	for _, name := range managedRRequiredPackages {
		if runtime.packageVersions[name] == "" {
			return managedRRuntime{}, fmt.Errorf("managed R runtime required package %s is unavailable", name)
		}
	}
	// R's immutable environment identity is derived only from its own verified
	// package manifest and explicit lock. Changes to unrelated Python helpers or
	// kernel assets must not force a second R download.
	activationHash := sha256.Sum256([]byte(
		managedRActivationContract + "\x00" + runtime.manifestDigest + "\x00" + runtime.manifest.ExplicitSHA256,
	))
	return managedRRuntime{
		managedCondaRuntime:  runtime,
		activationGeneration: hex.EncodeToString(activationHash[:]),
		rVersion:             runtime.packageVersions["r-base"],
	}, nil
}

func (m *Manager) managedRGenerationPath(runtime managedRRuntime) string {
	return filepath.Join(m.config.CondaEnvsPath, ".generations", runtime.entry.Name, runtime.activationGeneration)
}

func (m *Manager) managedRMarker(runtime managedRRuntime) managedRuntimeMarker {
	return managedRuntimeMarker{
		SchemaVersion: managedRuntimeMarkerVersion,
		Name:          runtime.entry.Name, Platform: runtime.entry.Platform,
		Generation: runtime.activationGeneration, ManifestSHA256: runtime.manifestDigest,
		ExplicitSHA256: runtime.manifest.ExplicitSHA256,
		Language:       "r", RuntimeVersion: runtime.rVersion,
	}
}

func (m *Manager) verifyManagedRGeneration(runtime managedRRuntime, prefix string) error {
	resolved, err := resolveManagedRuntimeGeneration(prefix)
	if err != nil {
		return err
	}
	expected, err := filepath.EvalSymlinks(m.managedRGenerationPath(runtime))
	if err != nil || resolved != expected {
		return errors.New("managed R active generation does not match the verified runtime")
	}
	var marker managedRuntimeMarker
	if _, err := readBoundedJSON(filepath.Join(resolved, managedRuntimeMarkerName), &marker); err != nil {
		return errors.New("managed R generation marker is unavailable")
	}
	if marker != m.managedRMarker(runtime) {
		return errors.New("managed R generation marker does not match the verified runtime")
	}
	if _, err := managedRExecutableAtPrefix(resolved); err != nil {
		return errors.New("managed R executable is unavailable")
	}
	return nil
}

func managedRExecutableAtPrefix(prefix string) (string, error) {
	for _, candidate := range environmentExecutableCandidates(prefix, "Rscript") {
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
	return "", errors.New("managed R executable is unavailable")
}

func (m *Manager) smokeManagedR(ctx context.Context, runtime managedRRuntime, prefix string) error {
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	quoted := make([]string, 0, len(managedRRequiredNamespaces))
	for _, namespace := range managedRRequiredNamespaces {
		quoted = append(quoted, fmt.Sprintf("%q", namespace))
	}
	code := "required <- c(" + strings.Join(quoted, ",") + ");" +
		"ok <- all(vapply(required, requireNamespace, logical(1L), quietly=TRUE));" +
		"if (!ok) quit(status=3L);" +
		"cat(paste0('SYNON_R_VERSION=', as.character(getRversion()), '\\n'))"
	command := newWorkerProcessCommand(ctx, rscript, "--vanilla", "-e", code)
	command.Env = kernelEnvironment(map[string]string{
		"CONDA_PREFIX": prefix, "CONDA_DEFAULT_ENV": runtime.entry.Name,
		"PATH": managedExecutableSearchPath(managedRuntimePath(prefix)), "R_LIBS_USER": "",
	})
	stdout := newTailBuffer(maxDiagnosticBytes)
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed R scientific smoke failed: %w: %s", err, boundedProcessDiagnostics(stdout.String(), stderr.String()))
	}
	versionLine := ""
	for _, line := range reverseNonEmptyOutputLines(stdout.String()) {
		if strings.HasPrefix(line, "SYNON_R_VERSION=") {
			versionLine = line
			break
		}
	}
	if version := strings.TrimPrefix(versionLine, "SYNON_R_VERSION="); version != runtime.rVersion {
		return errors.New("managed R scientific smoke returned an invalid version")
	}
	return nil
}

func (m *Manager) ensureManagedREnvironment(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.environmentMu.Lock()
	defer m.environmentMu.Unlock()
	m.setManagedRProvisioningPhase("verifying-assets")
	if err := m.Verify(); err != nil {
		return fmt.Errorf("verify managed R assets: %w", err)
	}
	m.setManagedRProvisioningPhase("loading-runtime")
	runtime, err := loadManagedRRuntime(m.config)
	if err != nil {
		return err
	}
	generationRoot := filepath.Join(m.config.CondaEnvsPath, ".generations", runtime.entry.Name)
	if err := os.MkdirAll(generationRoot, 0o700); err != nil {
		return fmt.Errorf("create managed R generation root: %w", err)
	}
	m.setManagedRProvisioningPhase("waiting-for-install-lock")
	release, err := lockKernelFile(ctx, filepath.Join(generationRoot, ".install.lock"))
	if err != nil {
		return fmt.Errorf("lock managed R installation: %w", err)
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	m.setManagedRProvisioningPhase("checking-active-generation")
	if err := m.verifyManagedRGeneration(runtime, active); err == nil {
		resolvedActive, err := resolveManagedRuntimeGeneration(active)
		if err != nil {
			return err
		}
		// Earlier installers could move a Conda R prefix after installation.
		// Its marker and Rscript file still exist, but the binary embeds the
		// vanished install prefix. Never reuse it without executing R.
		if err := m.smokeManagedR(ctx, runtime, resolvedActive); err == nil {
			rscript, err := managedRExecutableAtPrefix(resolvedActive)
			if err != nil {
				return err
			}
			return m.repairRSharedLibrary(ctx, rscript)
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	generationPath := m.managedRGenerationPath(runtime)
	if err := ctx.Err(); err != nil {
		return err
	}
	generationReady, _, err := prepareManagedRuntimeGeneration(generationPath, func() error {
		return m.verifyManagedRGeneration(runtime, generationPath)
	})
	if err != nil {
		return fmt.Errorf("inspect managed R generation: %w", err)
	}
	// The old generation may contain unknown local data. The preparation step
	// moves it aside under the install lock; do not silently delete it.
	if generationReady {
		// A marker alone cannot establish that a preexisting Conda prefix is
		// still executable. A cancelled probe is inconclusive and must never
		// cause a healthy active generation to be moved aside.
		if err := m.smokeManagedR(ctx, runtime, generationPath); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := quarantineManagedRuntimeGeneration(generationPath); err != nil {
				return fmt.Errorf("quarantine unusable managed R generation: %w", err)
			}
			generationReady = false
		}
	}
	if !generationReady {
		m.setManagedRProvisioningPhase("installing-bundled-generation")
		// Conda R embeds its installation prefix in launchers and scripts.
		// Install directly at the stable generation path; renaming a complete
		// Conda prefix would leave those references pointing at a dead path.
		if err := m.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "-p", generationPath, "-f", runtime.explicitPath); err != nil {
			return fmt.Errorf("install managed R generation: %w", err)
		}
		m.setManagedRProvisioningPhase("running-scientific-smoke-test")
		if err := m.smokeManagedR(ctx, runtime, generationPath); err != nil {
			return err
		}
	}
	rscript, err := managedRExecutableAtPrefix(generationPath)
	if err != nil {
		return err
	}
	// Shared-library staging is part of first-run readiness on Linux. Do it
	// before publishing the pointer so a failed or cancelled copy cannot make
	// a newly installed generation appear ready.
	if err := m.repairRSharedLibrary(ctx, rscript); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !generationReady {
		if err := writeManagedRuntimeMarker(filepath.Join(generationPath, managedRuntimeMarkerName), m.managedRMarker(runtime)); err != nil {
			return err
		}
	}
	if err := m.verifyManagedRGeneration(runtime, generationPath); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.setManagedRProvisioningPhase("activating-generation")
	if err := activateManagedRuntimeGeneration(active, generationPath); err != nil {
		return fmt.Errorf("activate managed R generation: %w", err)
	}
	m.setManagedRProvisioningPhase("verifying-generation")
	return m.verifyManagedRGeneration(runtime, active)
}

func (m *Manager) managedRRuntimeReady() error {
	runtime, err := loadManagedRRuntime(m.config)
	if err != nil {
		return err
	}
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	if err := m.verifyManagedRGeneration(runtime, active); err != nil {
		return err
	}
	prefix, err := resolveManagedRuntimeGeneration(active)
	if err != nil {
		return err
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	info, err := os.Stat(rscript)
	if err != nil {
		return err
	}
	cache := &m.managedRReadiness
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.info != nil && cache.prefix == prefix && cache.executable == rscript &&
		time.Since(cache.checkedAt) < managedRReadinessCacheTTL &&
		os.SameFile(cache.info, info) && cache.info.Size() == info.Size() && cache.info.ModTime().Equal(info.ModTime()) {
		return nil
	}
	cache.info = nil
	ctx, cancel := context.WithTimeout(context.Background(), managedEnvironmentHealthTimeout)
	defer cancel()
	if err := m.smokeManagedR(ctx, runtime, prefix); err != nil {
		return err
	}
	// Avoid caching a probe whose executable or active pointer changed while
	// R was starting. The next caller must retry from the current authority.
	currentInfo, err := os.Stat(rscript)
	if err != nil || !os.SameFile(info, currentInfo) || info.Size() != currentInfo.Size() || !info.ModTime().Equal(currentInfo.ModTime()) {
		return errors.New("managed R executable changed during readiness validation")
	}
	if err := m.verifyManagedRGeneration(runtime, active); err != nil {
		return err
	}
	currentPrefix, err := resolveManagedRuntimeGeneration(active)
	if err != nil || currentPrefix != prefix {
		return errors.New("managed R active generation changed during readiness validation")
	}
	cache.prefix, cache.executable, cache.info, cache.checkedAt = prefix, rscript, currentInfo, time.Now()
	return nil
}

func (m *Manager) ManagedRActiveGeneration() (string, error) {
	prefix, runtime, err := m.managedRActivePrefix()
	if err != nil {
		return "", err
	}
	if filepath.Base(prefix) != runtime.activationGeneration {
		return "", errors.New("managed R active generation is invalid")
	}
	return runtime.activationGeneration, nil
}

func (m *Manager) ManagedRActivePrefix() (string, error) {
	prefix, _, err := m.managedRActivePrefix()
	return prefix, err
}

func (m *Manager) managedRActivePrefix() (string, managedRRuntime, error) {
	if m == nil {
		return "", managedRRuntime{}, errors.New("kernel manager is not configured")
	}
	runtime, err := loadManagedRRuntime(m.config)
	if err != nil {
		return "", managedRRuntime{}, err
	}
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	if err := m.verifyManagedRGeneration(runtime, active); err != nil {
		return "", managedRRuntime{}, err
	}
	resolved, err := resolveManagedRuntimeGeneration(active)
	if err != nil || filepath.Base(resolved) != runtime.activationGeneration {
		return "", managedRRuntime{}, errors.New("managed R active generation is invalid")
	}
	return resolved, runtime, nil
}

func (m *Manager) ManagedRPackages() ([]string, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	runtime, err := loadManagedRRuntime(m.config)
	if err != nil {
		return nil, err
	}
	packages := make([]string, 0, len(runtime.manifest.Packages))
	for _, item := range runtime.manifest.Packages {
		packages = append(packages, item.Name)
	}
	sort.Strings(packages)
	return packages, nil
}
