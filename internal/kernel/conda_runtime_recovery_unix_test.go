//go:build linux && amd64

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeCoreRuntimeInstallerFixture(t *testing.T, interpreter, output string) (string, string) {
	t.Helper()
	root := t.TempDir()
	installer := filepath.Join(root, "micromamba")
	count := filepath.Join(root, "install-count")
	script := `#!/bin/sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
count_file="$script_dir/install-count"
count=0
if [ -f "$count_file" ]; then count=$(cat "$count_file"); fi
printf '%s\n' "$((count + 1))" > "$count_file"
prefix=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -p) shift; prefix="$1" ;;
  esac
  shift
done
mkdir -p "$prefix/bin"
cat > "$prefix/bin/` + interpreter + `" <<'RUNTIME'
#!/bin/sh
` + output + `
RUNTIME
chmod 755 "$prefix/bin/` + interpreter + `"
`
	if err := os.WriteFile(installer, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return installer, count
}

func readCoreRuntimeInstallCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func coreRuntimeFixtureConfig(t *testing.T, installer string) Config {
	t.Helper()
	root := repositoryRootForCondaRuntimeTest(t)
	assetRoot := filepath.Join(root, "assets", "optional")
	condaHome := filepath.Join(t.TempDir(), "conda")
	return Config{
		CondaHome:                condaHome,
		CondaEnvsPath:            filepath.Join(condaHome, "envs"),
		CondaRuntimeCatalog:      filepath.Join(assetRoot, "conda-runtimes", "manifest.json"),
		ManagedPythonEnvironment: defaultManagedPythonEnvironment,
		ManifestPath:             filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		PythonHelperPath:         filepath.Join(assetRoot, "kernels", "cheminfo_render_helpers.py"),
		RWorkerPath:              filepath.Join(assetRoot, "kernels", "kernel_worker.R"),
		WorkerPath:               filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		AssetRoot:                assetRoot,
		Micromamba:               installer,
		ManagedEnvironmentInstallerInactivityTimeout: 2 * time.Second,
	}
}

func TestManagedPythonCorruptGenerationIsReinstalledAndHealthyGenerationIsReused(t *testing.T) {
	installer, countPath := writeCoreRuntimeInstallerFixture(t, "python", `printf '%s\n' 'warning from python' '{"ok":true,"rdkit":"2024.03.5"}'`)
	config := coreRuntimeFixtureConfig(t, installer)
	manager := NewManager(config)
	if err := manager.ensureManagedPythonEnvironment(context.Background()); err != nil {
		t.Fatalf("first Python provisioning: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 1 {
		t.Fatalf("first Python installer count=%d, want 1", got)
	}
	manager = NewManager(config)
	if err := manager.ensureManagedPythonEnvironment(context.Background()); err != nil {
		t.Fatalf("healthy Python reuse: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 1 {
		t.Fatalf("healthy Python reuse installer count=%d, want 1", got)
	}
	runtime, err := loadManagedPythonRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.managedPythonGenerationPath(runtime), managedRuntimeMarkerName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureManagedPythonEnvironment(context.Background()); err != nil {
		t.Fatalf("Python recovery after marker corruption: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 2 {
		t.Fatalf("corrupt Python recovery installer count=%d, want 2", got)
	}
	if err := manager.managedPythonRuntimeReady(); err != nil {
		t.Fatalf("recovered Python generation is not ready: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(manager.managedPythonGenerationPath(runtime)), ".staging-invalid-*")); err != nil || len(matches) != 0 {
		t.Fatalf("invalid Python quarantine remains: matches=%v err=%v", matches, err)
	}
}

func TestManagedRCorruptGenerationIsReinstalledAndHealthyGenerationIsReused(t *testing.T) {
	installer, countPath := writeCoreRuntimeInstallerFixture(t, "Rscript", `printf '%s\n' 'warning from R' 'SYNON_R_VERSION=4.5.3'`)
	config := coreRuntimeFixtureConfig(t, installer)
	config.DefaultREnv = "r"
	manager := NewManager(config)
	if err := manager.ensureManagedREnvironment(context.Background()); err != nil {
		t.Fatalf("first R provisioning: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 1 {
		t.Fatalf("first R installer count=%d, want 1", got)
	}
	manager = NewManager(config)
	if err := manager.ensureManagedREnvironment(context.Background()); err != nil {
		t.Fatalf("healthy R reuse: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 1 {
		t.Fatalf("healthy R reuse installer count=%d, want 1", got)
	}
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.managedRGenerationPath(runtime), managedRuntimeMarkerName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureManagedREnvironment(context.Background()); err != nil {
		t.Fatalf("R recovery after marker corruption: %v", err)
	}
	if got := readCoreRuntimeInstallCount(t, countPath); got != 2 {
		t.Fatalf("corrupt R recovery installer count=%d, want 2", got)
	}
	if err := manager.managedRRuntimeReady(); err != nil {
		t.Fatalf("recovered R generation is not ready: %v", err)
	}
	// A corrupt generation may contain local state not created by this attempt.
	// Recovery must quarantine it, not silently delete its contents.
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(manager.managedRGenerationPath(runtime)), ".staging-invalid-*")); err != nil || len(matches) != 1 {
		t.Fatalf("invalid R generation was not preserved: matches=%v err=%v", matches, err)
	} else if marker, err := os.ReadFile(filepath.Join(matches[0], managedRuntimeMarkerName)); err != nil || string(marker) != "{}\n" {
		t.Fatalf("quarantined R generation marker=%q err=%v", marker, err)
	}
}
