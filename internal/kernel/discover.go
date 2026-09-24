package kernel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"synon-go/internal/assets"
)

type DiscoveryStage string

const (
	DiscoveryStageAssetRoot   DiscoveryStage = "asset_root"
	DiscoveryStagePython      DiscoveryStage = "python"
	DiscoveryStageEnvironment DiscoveryStage = "environment"
	DiscoveryStageMicromamba  DiscoveryStage = "micromamba"
	DiscoveryStageAssets      DiscoveryStage = "assets"
)

// DiscoveryError preserves the internal startup cause without requiring API
// callers to expose filesystem paths or raw asset contents.
type DiscoveryError struct {
	Stage DiscoveryStage
	Err   error
}

func (e *DiscoveryError) Error() string {
	if e == nil || e.Err == nil {
		return "kernel runtime discovery failed"
	}
	return fmt.Sprintf("kernel runtime discovery failed at %s: %v", e.Stage, e.Err)
}

func (e *DiscoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type DiscoveryDiagnostic struct {
	Code    string
	Message string
}

// DiagnoseDiscoveryError returns a bounded path-free public diagnosis. The
// original error remains available through errors.As/Unwrap for operator-side
// debugging and tests.
func DiagnoseDiscoveryError(err error) DiscoveryDiagnostic {
	var discovery *DiscoveryError
	if !errors.As(err, &discovery) {
		return DiscoveryDiagnostic{Code: "kernel_discovery_failed", Message: "kernel runtime discovery failed"}
	}
	switch discovery.Stage {
	case DiscoveryStageAssetRoot:
		return DiscoveryDiagnostic{Code: "kernel_assets_unavailable", Message: "verified kernel assets are unavailable"}
	case DiscoveryStagePython:
		return DiscoveryDiagnostic{Code: "kernel_python_invalid", Message: "kernel Python executable verification failed"}
	case DiscoveryStageEnvironment:
		return DiscoveryDiagnostic{Code: "kernel_environment_invalid", Message: "kernel environment path verification failed"}
	case DiscoveryStageMicromamba:
		return DiscoveryDiagnostic{Code: "kernel_micromamba_invalid", Message: "kernel micromamba verification failed"}
	case DiscoveryStageAssets:
		return DiscoveryDiagnostic{Code: "kernel_assets_invalid", Message: "kernel asset verification failed"}
	default:
		return DiscoveryDiagnostic{Code: "kernel_discovery_failed", Message: "kernel runtime discovery failed"}
	}
}

func discoveryFailure(stage DiscoveryStage, err error) error {
	if err == nil {
		return nil
	}
	return &DiscoveryError{Stage: stage, Err: err}
}

func DiscoverManager() (*Manager, error) {
	return DiscoverManagerWithPathsAndProxy(
		os.Getenv("SYNON_CONDA_HOME"), os.Getenv("SYNON_CONDA_ENVS_PATH"), os.Getenv("SYNON_NETWORK_PROXY"),
	)
}

func DiscoverManagerWithPaths(condaHome, condaEnvsPath string) (*Manager, error) {
	return DiscoverManagerWithPathsAndProxy(condaHome, condaEnvsPath, "")
}

// DiscoverManagerWithPathsAndProxy keeps the normalized product proxy scoped
// to installer processes while preserving the legacy two-path entrypoint.
func DiscoverManagerWithPathsAndProxy(condaHome, condaEnvsPath, installerProxy string) (*Manager, error) {
	assetRoot, err := discoverAssetRoot()
	if err != nil {
		return nil, discoveryFailure(DiscoveryStageAssetRoot, err)
	}
	python := strings.TrimSpace(os.Getenv("SYNON_KERNEL_PYTHON"))
	if python == "" {
		for _, candidate := range []string{"python3", "python"} {
			if path, lookupErr := exec.LookPath(candidate); lookupErr == nil {
				python = path
				break
			}
		}
	}
	if python != "" {
		python, err = absoluteExecutable(python)
		if err != nil {
			return nil, discoveryFailure(DiscoveryStagePython, fmt.Errorf("resolve kernel Python: %w", err))
		}
	}
	condaHome = strings.TrimSpace(condaHome)
	if condaHome == "" {
		if home := strings.TrimSpace(os.Getenv("SYNON_HOME")); home != "" {
			condaHome = filepath.Join(home, "conda")
		} else if home, homeErr := os.UserHomeDir(); homeErr == nil {
			condaHome = filepath.Join(home, ".synon-go", "conda")
		}
	}
	if condaHome != "" {
		condaHome, err = filepath.Abs(condaHome)
		if err != nil {
			return nil, discoveryFailure(DiscoveryStageEnvironment, fmt.Errorf("resolve Conda home: %w", err))
		}
	}
	condaEnvsPath = strings.TrimSpace(condaEnvsPath)
	if condaEnvsPath == "" && condaHome != "" {
		condaEnvsPath = filepath.Join(condaHome, "envs")
	}
	if condaEnvsPath != "" {
		condaEnvsPath, err = filepath.Abs(condaEnvsPath)
		if err != nil {
			return nil, discoveryFailure(DiscoveryStageEnvironment, fmt.Errorf("resolve Conda environment root: %w", err))
		}
	}
	micromamba, err := discoverMicromamba(assetRoot)
	if err != nil {
		return nil, discoveryFailure(DiscoveryStageMicromamba, err)
	}
	manager := NewManager(Config{
		Python: python, Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: condaEnvsPath,
		InstallerProxy: strings.TrimSpace(installerProxy),
		AssetRoot:      assetRoot, ManifestPath: filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:               filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		CondaRuntimeCatalog:      filepath.Join(assetRoot, "conda-runtimes", "manifest.json"),
		ManagedPythonEnvironment: defaultManagedPythonEnvironment,
		PythonHelperPath:         filepath.Join(assetRoot, "kernels", "cheminfo_render_helpers.py"),
		SDFValidatorPath:         filepath.Join(assetRoot, "kernels", "sdf_artifact_validator.py"),
		RWorkerPath:              filepath.Join(assetRoot, "kernels", "kernel_worker.R"), DefaultREnv: defaultManagedREnvironment,
		RSharedPackages: []string{"tidyverse", "jsonlite", "ggplot2"},
	})
	if err := manager.Verify(); err != nil {
		return nil, discoveryFailure(DiscoveryStageAssets, err)
	}
	return manager, nil
}

func discoverMicromamba(assetRoot string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("SYNON_MICROMAMBA")); configured != "" {
		path, err := absoluteExecutable(configured)
		if err != nil {
			return "", fmt.Errorf("configured micromamba: %w", err)
		}
		return path, nil
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		platform = "linux-x86_64"
	}
	bundledRoot := filepath.Join(assetRoot, "micromamba")
	bundled := filepath.Join(bundledRoot, platform, executableName("micromamba"))
	if info, err := os.Stat(bundled); err == nil && info.Mode().IsRegular() {
		manifest, loadErr := assets.Load(filepath.Join(bundledRoot, "manifest.json"))
		if loadErr != nil {
			return "", fmt.Errorf("load bundled micromamba manifest: %w", loadErr)
		}
		if manifest.Entrypoint != filepath.ToSlash(filepath.Join(platform, executableName("micromamba"))) {
			return "", errors.New("bundled micromamba manifest does not match this platform")
		}
		if _, verifyErr := assets.Verify(bundledRoot, manifest); verifyErr != nil {
			return "", fmt.Errorf("verify bundled micromamba: %w", verifyErr)
		}
		return absoluteExecutable(bundled)
	}
	if path, err := exec.LookPath(executableName("micromamba")); err == nil {
		return absoluteExecutable(path)
	}
	return "", nil
}

func absoluteExecutable(value string) (string, error) {
	if !filepath.IsAbs(value) {
		resolved, err := exec.LookPath(value)
		if err != nil {
			return "", err
		}
		value = resolved
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !executableRegularFile(info) {
		return "", errors.New("runtime executable must be an executable regular file")
	}
	return resolved, nil
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func discoverAssetRoot() (string, error) {
	candidates := []string{}
	if configured := strings.TrimSpace(os.Getenv("SYNON_KERNEL_ASSET_ROOT")); configured != "" {
		candidates = append(candidates, configured)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "assets", "optional"))
	}
	if executable, err := os.Executable(); err == nil {
		directory := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(directory, "assets", "optional"),
			filepath.Join(directory, "..", "assets", "optional"),
		)
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		if info, err := os.Stat(filepath.Join(absolute, "kernel-compute.manifest.json")); err == nil && info.Mode().IsRegular() {
			return absolute, nil
		}
	}
	return "", errors.New("verified optional kernel asset root was not found")
}
