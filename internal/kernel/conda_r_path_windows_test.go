//go:build windows

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsManagedRGenerationUsesCompactDirectoryAndFullIdentity(t *testing.T) {
	fullGeneration := strings.Repeat("a", 64)
	runtime := managedRRuntime{
		managedCondaRuntime: managedCondaRuntime{
			entry: condaRuntimeCatalogEntry{Name: defaultManagedREnvironment},
		},
		activationGeneration: fullGeneration,
	}
	envs := filepath.Join(t.TempDir(), "conda", "envs")
	manager := NewManager(Config{CondaEnvsPath: envs})
	got := manager.managedRGenerationPath(runtime)
	want := filepath.Join(envs, ".generations", defaultManagedREnvironment, strings.Repeat("a", 32))
	if got != want {
		t.Fatalf("Windows R generation path=%q want=%q", got, want)
	}
	if marker := manager.managedRMarker(runtime); marker.Generation != fullGeneration {
		t.Fatalf("R marker lost full generation identity: %q", marker.Generation)
	}
}

func TestWindowsManagedRGenerationCollisionPreservesOriginal(t *testing.T) {
	first := strings.Repeat("a", 32) + strings.Repeat("b", 32)
	second := strings.Repeat("a", 32) + strings.Repeat("c", 32)
	manager := NewManager(Config{CondaEnvsPath: filepath.Join(t.TempDir(), "conda", "envs")})
	runtime := managedRRuntime{
		managedCondaRuntime:  managedCondaRuntime{entry: condaRuntimeCatalogEntry{Name: defaultManagedREnvironment}},
		activationGeneration: first,
	}
	path := manager.managedRGenerationPath(runtime)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(path, managedRuntimeMarkerName)
	if err := writeManagedRuntimeMarker(markerPath, manager.managedRMarker(runtime)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime.activationGeneration = second
	if other := manager.managedRGenerationPath(runtime); other != path {
		t.Fatalf("colliding generations have different paths: %q and %q", path, other)
	}
	if err := rejectManagedRGenerationPathCollision(path, second); err == nil {
		t.Fatal("different full generation was accepted at the same Windows directory key")
	}
	after, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("collision removed original marker: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("collision changed original generation marker")
	}
}

func TestWindowsManagedRActiveGenerationReturnsFullIdentity(t *testing.T) {
	assetRoot := windowsManagedRTestAssetRoot(t)
	config := Config{
		AssetRoot:           assetRoot,
		CondaRuntimeCatalog: filepath.Join(assetRoot, "conda-runtimes", "windows-x86_64", "manifest.json"),
		ManifestPath:        filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		DefaultREnv:         defaultManagedREnvironment,
	}
	config.CondaEnvsPath = filepath.Join(t.TempDir(), "conda", "envs")
	manager := NewManager(config)
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	path := manager.managedRGenerationPath(runtime)
	rscript := environmentExecutableCandidates(path, "Rscript")[0]
	if err := os.MkdirAll(filepath.Dir(rscript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rscript, []byte("test executable fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedRuntimeMarker(filepath.Join(path, managedRuntimeMarkerName), manager.managedRMarker(runtime)); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(config.CondaEnvsPath, runtime.entry.Name)
	if err := activateManagedRuntimeGeneration(active, path); err != nil {
		t.Fatal(err)
	}
	got, err := manager.ManagedRActiveGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if got != runtime.activationGeneration || len(got) != 64 {
		t.Fatalf("active R generation=%q want full SHA-256 %q", got, runtime.activationGeneration)
	}
}

// A compiled test executable may run from outside the checkout. In that case
// the caller supplies the clean-clone asset root explicitly; ordinary go test
// discovers it by walking upward from the package working directory.
func windowsManagedRTestAssetRoot(t *testing.T) string {
	t.Helper()
	validate := func(root string) bool {
		for _, path := range []string{
			filepath.Join(root, "conda-runtimes", "windows-x86_64", "manifest.json"),
			filepath.Join(root, "kernel-compute.manifest.json"),
		} {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
		}
		return true
	}
	if configured := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_ASSET_ROOT")); configured != "" {
		if !filepath.IsAbs(configured) || !validate(configured) {
			t.Fatalf("SYNON_TEST_NATIVE_ASSET_ROOT is not an absolute checked-out asset directory: %q", configured)
		}
		return configured
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for directory := cwd; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "assets", "optional")
		if validate(candidate) {
			return candidate
		}
		if parent := filepath.Dir(directory); parent == directory {
			break
		}
	}
	t.Fatal("cannot locate checked-out Windows runtime assets; set SYNON_TEST_NATIVE_ASSET_ROOT for a standalone test binary")
	return ""
}

func TestWindowsBundledInstallerStartsWithoutDownload(t *testing.T) {
	assetRoot := windowsManagedRTestAssetRoot(t)
	installer := filepath.Join(assetRoot, "micromamba", "windows-x86_64", "micromamba.exe")
	manager := NewManager(Config{
		Micromamba: installer,
		CondaHome:  filepath.Join(t.TempDir(), "conda"),
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := manager.runManagedEnvironmentCommand(ctx, "--version"); err != nil {
		t.Fatalf("bundled Windows installer did not start: %v", err)
	}
}

// This opt-in test requires a caller-owned, absent state root with roughly the
// same depth as the default user home. It never uses the running service's data.
func TestRealWindowsManagedRColdInstallImportAndReuse(t *testing.T) {
	if os.Getenv("SYNON_TEST_NATIVE_R_COLD_INSTALL") != "1" {
		t.Skip("Windows R cold installation is opt-in")
	}
	assetRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_ASSET_ROOT"))
	stateRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_STATE_ROOT"))
	if !filepath.IsAbs(assetRoot) || !filepath.IsAbs(stateRoot) {
		t.Fatal("Windows R cold installation requires absolute asset and disposable state roots")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(stateRoot, filepath.Join(home, ".synon-go")) {
		t.Fatal("cold installation must not use the real default user state root")
	}
	if _, err := os.Lstat(stateRoot); err == nil {
		t.Fatalf("cold installation state root already exists: %s", stateRoot)
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect cold installation state root: %v", err)
	}
	verifyRealWindowsManagedRInstallAndReuse(t, assetRoot, stateRoot, false)
}

// This second opt-in entrypoint checks a retained cold-install result without
// allowing another installer run. It is useful when a host-level failure
// interrupts verification after the generation has already been installed.
func TestRealWindowsManagedRExistingInstallImportAndReuse(t *testing.T) {
	if os.Getenv("SYNON_TEST_NATIVE_R_REUSE_EXISTING") != "1" {
		t.Skip("Windows R existing installation verification is opt-in")
	}
	assetRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_ASSET_ROOT"))
	stateRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_STATE_ROOT"))
	if !filepath.IsAbs(assetRoot) || !filepath.IsAbs(stateRoot) {
		t.Fatal("Windows R reuse verification requires absolute asset and state roots")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(stateRoot, filepath.Join(home, ".synon-go")) {
		t.Fatal("reuse verification must not use the real default user state root")
	}
	info, err := os.Lstat(stateRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("existing Windows R state root is not a real directory: %v", err)
	}
	verifyRealWindowsManagedRInstallAndReuse(t, assetRoot, stateRoot, true)
}

func verifyRealWindowsManagedRInstallAndReuse(t *testing.T, assetRoot, stateRoot string, reuseOnly bool) {
	t.Helper()
	t.Setenv("SYNON_KERNEL_ASSET_ROOT", assetRoot)
	t.Setenv("SYNON_MICROMAMBA", "")
	condaHome := filepath.Join(stateRoot, "conda")
	envs := filepath.Join(condaHome, "envs")
	manager, err := DiscoverManagerWithPathsAndProxy(condaHome, envs, "")
	if err != nil {
		t.Fatal(err)
	}
	if reuseOnly {
		manager.config.Micromamba = filepath.Join(stateRoot, "installer-must-not-run")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	if err := manager.EnsureManagedREnvironment(ctx); err != nil {
		t.Fatalf("prepare managed R: %v", err)
	}
	fullGeneration, err := manager.ManagedRActiveGeneration()
	if err != nil || len(fullGeneration) != 64 {
		t.Fatalf("installed R generation=%q err=%v", fullGeneration, err)
	}
	prefix, err := manager.ManagedRActivePrefix()
	if err != nil || filepath.Base(prefix) != fullGeneration[:windowsManagedRGenerationDirectoryHex] {
		t.Fatalf("installed R prefix=%q generation=%q err=%v", prefix, fullGeneration, err)
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	checkPackages := "stopifnot(requireNamespace('data.table',quietly=TRUE)," +
		"requireNamespace('ggplot2',quietly=TRUE)," +
		"requireNamespace('jsonlite',quietly=TRUE)," +
		"requireNamespace('tidyverse',quietly=TRUE));cat(as.character(getRversion()))"
	command := exec.CommandContext(ctx, rscript, "--vanilla", "-e", checkPackages)
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "4.5.3" {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("R dependency import/version=%q err=%v stderr=%q", output, err, stderr)
	}
	restarted := NewManager(manager.config)
	restarted.config.Micromamba = filepath.Join(stateRoot, "installer-must-not-run")
	if err := restarted.EnsureManagedREnvironment(ctx); err != nil {
		t.Fatalf("restart failed to reuse R: %v", err)
	}
	reused, err := restarted.ManagedRActiveGeneration()
	if err != nil || reused != fullGeneration {
		t.Fatalf("reused generation=%q err=%v want=%q", reused, err, fullGeneration)
	}
}
