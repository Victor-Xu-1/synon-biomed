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

	"sort"

	"strings"
)

func (m *Manager) managedEnvironmentRoot() (string, error) {
	root := strings.TrimSpace(m.config.CondaEnvsPath)
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("managed environment root is not configured")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", errors.New("managed environment root is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("managed environment root is unavailable")
	}
	return resolved, nil
}

func (m *Manager) readManagedEnvironment(name string, includePackages bool) (ManagedEnvironment, error) {
	if err := validateManagedEnvironmentName(name); err != nil {
		return ManagedEnvironment{}, err
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return ManagedEnvironment{}, err
	}
	active := filepath.Join(root, name)
	info, err := os.Lstat(active)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ManagedEnvironment{}, errors.New("managed environment is not atomically activated")
	}
	prefix, err := filepath.EvalSymlinks(active)
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment generation is unavailable")
	}
	relative, err := filepath.Rel(filepath.Join(root, ".generations", name), prefix)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return ManagedEnvironment{}, errors.New("managed environment generation is outside its trusted root")
	}
	marker, err := readManagedEnvironmentMarker(prefix)
	if err != nil || marker.Name != name || marker.Generation != filepath.Base(prefix) {
		return ManagedEnvironment{}, errors.New("managed environment generation marker is invalid")
	}
	packages := []string(nil)
	if includePackages {
		packages = append(packages, marker.Packages...)
	}
	return ManagedEnvironment{Name: name, Language: marker.Language, Kind: marker.Kind, Generation: marker.Generation, SpecDigest: marker.SpecDigest, Packages: packages, Status: "ready"}, nil
}

// ManagedEnvironmentActiveGeneration returns the immutable generation for an
// environment created by the generic manager. A legacy unmanaged environment
// returns found=false; a present but corrupt generic environment fails closed.
func (m *Manager) ManagedEnvironmentActiveGeneration(name string) (generation string, found bool, err error) {
	if err := validateManagedEnvironmentName(name); err != nil {
		return "", false, err
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return "", false, err
	}
	active := filepath.Join(root, name)
	info, err := os.Lstat(active)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false, nil
	}
	prefix, err := filepath.EvalSymlinks(active)
	if err != nil {
		return "", false, errors.New("managed environment generation is unavailable")
	}
	if _, err := os.Stat(filepath.Join(prefix, managedEnvironmentMarkerName)); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, errors.New("managed environment generation marker is unavailable")
	}
	environment, err := m.readManagedEnvironment(name, false)
	if err != nil {
		return "", false, err
	}
	marker, err := readManagedEnvironmentMarker(prefix)
	if err != nil {
		return "", false, err
	}
	runtimePrefix := prefix
	if marker.Kind == "path-venv" {
		runtimePrefix = marker.RuntimePath
	}
	healthContext, cancel := context.WithTimeout(context.Background(), managedEnvironmentHealthTimeout)
	defer cancel()
	if err := m.validateManagedEnvironmentGeneration(
		healthContext, marker.Generation, marker.Language, runtimePrefix, marker.Packages, marker.ImportNames,
	); err != nil {
		return "", false, err
	}
	return environment.Generation, true, nil
}

// ManagedEnvironmentRuntimePrefix returns the immutable, health-checked
// runtime prefix for an active managed environment. Callers receive no
// installer or generation-root authority; the returned path is suitable only
// for read-only runtime execution (PATH/CONDA_PREFIX binding).
func (m *Manager) ManagedEnvironmentRuntimePrefix(name string) (prefix string, found bool, err error) {
	if _, found, err = m.ManagedEnvironmentActiveGeneration(name); err != nil || !found {
		return "", found, err
	}
	marker, err := m.activeManagedEnvironmentMarker(name)
	if err != nil {
		return "", true, err
	}
	if marker.Kind == "path-venv" {
		if strings.TrimSpace(marker.RuntimePath) == "" || !filepath.IsAbs(marker.RuntimePath) {
			return "", true, errors.New("registered environment runtime path is invalid")
		}
		resolved, resolveErr := filepath.EvalSymlinks(marker.RuntimePath)
		if resolveErr != nil {
			return "", true, errors.New("registered environment runtime path is unavailable")
		}
		return resolved, true, nil
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return "", true, err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return "", true, errors.New("managed environment runtime prefix is unavailable")
	}
	return resolved, true, nil
}

// RegisteredEnvironmentPaths returns the external read-only authority behind
// an active path-venv registration. Callers must revalidate owner grants and
// protected paths immediately before deriving a managed package generation.
func (m *Manager) RegisteredEnvironmentPaths(name string) (sourcePath, runtimePath string, found bool, err error) {
	if err := validateManagedEnvironmentName(name); err != nil {
		return "", "", false, err
	}
	marker, err := m.activeManagedEnvironmentMarker(name)
	if err != nil {
		return "", "", false, err
	}
	if marker.Kind != "path-venv" {
		return "", "", false, nil
	}
	return marker.SourcePath, marker.RuntimePath, true, nil
}

func (m *Manager) inspectManagedEnvironmentPackages(ctx context.Context, prefix string) ([]string, error) {
	command := newWorkerProcessCommand(ctx, m.config.Micromamba, "--no-rc", "list", "-p", prefix, "--json")
	command.Env = m.managedEnvironmentInstallerEnv()
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return nil, fmt.Errorf("inspect managed environment packages: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() <= 0 || stdout.Len() > maxManagedEnvironmentListBytes {
		return nil, errors.New("managed environment package inventory is outside the supported size")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout.Bytes()), maxManagedEnvironmentListBytes+1))
	var inventory []micromambaPackage
	if err := decoder.Decode(&inventory); err != nil || len(inventory) == 0 {
		return nil, errors.New("managed environment package inventory is invalid")
	}
	packages := make([]string, 0, len(inventory))
	for index, item := range inventory {
		name := strings.ToLower(strings.TrimSpace(item.Name))
		version := strings.TrimSpace(item.Version)
		build := strings.TrimSpace(item.Build)
		channel := strings.TrimSpace(item.Channel)
		if !managedPackageName.MatchString(name) {
			return nil, fmt.Errorf("managed environment package inventory contains an invalid record at index %d: package name is invalid", index)
		}
		if version == "" {
			return nil, fmt.Errorf("managed environment package inventory contains an invalid record at index %d: package version is missing", index)
		}
		if strings.ContainsAny(version+build+channel, "\x00\r\n") {
			return nil, fmt.Errorf("managed environment package inventory contains an invalid record at index %d: package metadata contains a control character", index)
		}
		packages = append(packages, strings.Join([]string{name, version, build, channel}, "="))
	}
	sort.Strings(packages)
	return packages, nil
}

func inspectRegisteredPythonPackages(ctx context.Context, prefix string) ([]string, error) {
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return nil, errors.New("registered environment Python interpreter is unavailable")
	}
	if info, err := os.Stat(filepath.Join(prefix, "pyvenv.cfg")); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("registered environment is not a Python virtual environment")
	}
	command := newWorkerProcessCommand(ctx, python, "-I", "-m", "pip", "list", "--format=json", "--disable-pip-version-check")
	command.Env = kernelEnvironment(map[string]string{
		"VIRTUAL_ENV": prefix, "PATH": filepath.Join(prefix, "bin"), "PIP_CONFIG_FILE": os.DevNull, "PYTHONNOUSERSITE": "1",
	})
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return nil, fmt.Errorf("inspect registered environment packages: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() <= 0 || stdout.Len() > maxManagedEnvironmentListBytes {
		return nil, errors.New("registered environment package inventory is outside the supported size")
	}
	var inventory []struct {
		Name, Version string
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout.Bytes()), maxManagedEnvironmentListBytes+1))
	if err := decoder.Decode(&inventory); err != nil || len(inventory) == 0 || decoder.Decode(&struct{}{}) == nil {
		return nil, errors.New("registered environment package inventory is invalid")
	}
	packages := make([]string, 0, len(inventory))
	for _, item := range inventory {
		name := strings.ToLower(strings.TrimSpace(item.Name))
		version := strings.TrimSpace(item.Version)
		if !managedPackageName.MatchString(name) || version == "" || strings.ContainsAny(version, "\x00\r\n") {
			return nil, errors.New("registered environment package inventory contains an invalid record")
		}
		packages = append(packages, name+"="+version+"==pip")
	}
	sort.Strings(packages)
	return packages, nil
}

func inspectRegisteredPythonVersion(ctx context.Context, prefix string) (string, error) {
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return "", errors.New("registered environment Python interpreter is unavailable")
	}
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", "import sys;print(f'SYNON_PYTHON_VERSION={sys.version_info[0]}.{sys.version_info[1]}')")
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return "", fmt.Errorf("inspect registered environment Python version: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	version := strings.TrimPrefix(strings.TrimSpace(stdout.String()), "SYNON_PYTHON_VERSION=")
	if !managedPythonVersion.MatchString(version) {
		return "", errors.New("registered environment Python version is invalid")
	}
	return version, nil
}
