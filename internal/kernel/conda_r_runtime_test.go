package kernel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRSharedPackageExpressionPlacesLibraryBeforePackages(t *testing.T) {
	got := rSharedPackageExpressionArgs("cat(1)", "/shared/r", []string{"tidyverse", "jsonlite"})
	want := []string{"--vanilla", "-e", "cat(1)", "/shared/r", "tidyverse", "jsonlite"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rscript expression arguments=%q want=%q", got, want)
	}
}

func bundledManagedRConfig(t *testing.T) Config {
	t.Helper()
	config := bundledManagedPythonConfig(t)
	config.DefaultREnv = "r"
	return config
}

func TestBundledManagedRRuntimeContractIncludesRequiredScientificPackages(t *testing.T) {
	runtime, err := loadManagedRRuntime(bundledManagedRConfig(t))
	if err != nil {
		t.Fatalf("load bundled managed R runtime: %v", err)
	}
	if runtime.entry.Name != defaultManagedREnvironment || runtime.rVersion != "4.5.3" {
		t.Fatalf("R runtime identity/version=%#v %q", runtime.entry, runtime.rVersion)
	}
	for _, packageName := range managedRRequiredPackages {
		if runtime.packageVersions[packageName] == "" {
			t.Fatalf("required R package %q is missing", packageName)
		}
	}
	if len(runtime.manifest.Packages) < len(managedRRequiredPackages) {
		t.Fatalf("R package inventory is incomplete: %d", len(runtime.manifest.Packages))
	}
}

func TestManagedScientificRuntimePathsAdaptToEachUserRoot(t *testing.T) {
	explicitEnvs := filepath.Join(t.TempDir(), "custom", "envs")
	derived := NewManager(Config{CondaEnvsPath: explicitEnvs})
	if derived.config.CondaHome != filepath.Dir(explicitEnvs) {
		t.Fatalf("explicit environment root derived Conda home=%q, want %q", derived.config.CondaHome, filepath.Dir(explicitEnvs))
	}
	config := bundledManagedRConfig(t)
	config.CondaEnvsPath = ""
	for _, userRoot := range []string{t.TempDir(), t.TempDir()} {
		config.CondaHome = filepath.Join(userRoot, "scientific-data")
		manager := NewManager(config)
		if !strings.HasPrefix(manager.config.CondaEnvsPath, config.CondaHome+string(filepath.Separator)) {
			t.Fatalf("managed environment root escaped user root: home=%q envs=%q", config.CondaHome, manager.config.CondaEnvsPath)
		}
		runtime, err := loadManagedRRuntime(manager.config)
		if err != nil {
			t.Fatalf("load R runtime for user root %q: %v", config.CondaHome, err)
		}
		generation := manager.managedRGenerationPath(runtime)
		if !strings.HasPrefix(generation, manager.config.CondaEnvsPath+string(filepath.Separator)) {
			t.Fatalf("R generation escaped managed environment root: %q", generation)
		}
		if filepath.Base(filepath.Dir(generation)) != defaultManagedREnvironment {
			t.Fatalf("R generation identity=%q", generation)
		}
	}
}

func TestManagedRNameCanonicalizesLegacyAliases(t *testing.T) {
	for _, alias := range []string{"", "r", "claude-science-r"} {
		if got := managedRName(Config{DefaultREnv: alias}); got != defaultManagedREnvironment {
			t.Fatalf("R alias %q resolved to %q", alias, got)
		}
	}
	manager := NewManager(Config{DefaultREnv: "custom-r"})
	if got := manager.canonicalManagedREnvironment("claude-science-r"); got != "custom-r" {
		t.Fatalf("persisted R alias resolved to %q instead of configured environment", got)
	}
}

func TestPersistedRAliasUsesConfiguredLegacyRuntimeWhenBundledProvisioningIsDisabled(t *testing.T) {
	root := t.TempDir()
	worker := filepath.Join(root, "kernel_worker.R")
	if err := os.WriteFile(worker, []byte("cat(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, "envs", "custom-r")
	rscript := environmentExecutableCandidates(prefix, "Rscript")[0]
	if err := os.MkdirAll(filepath.Dir(rscript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rscript, []byte(""), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		CondaEnvsPath: filepath.Join(root, "envs"),
		DefaultREnv:   "custom-r",
		RWorkerPath:   worker,
	})
	if !manager.RuntimeReady("r", persistedRRuntimeAlias) {
		t.Fatal("persisted R alias did not resolve the configured legacy runtime")
	}
}

func TestManagedRGenerationValidationUsesBundledMarkerAuthority(t *testing.T) {
	config := bundledManagedRConfig(t)
	config.CondaEnvsPath = t.TempDir()
	manager := NewManager(config)
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	generation := manager.managedRGenerationPath(runtime)
	rscript := environmentExecutableCandidates(generation, "Rscript")[0]
	if err := os.MkdirAll(filepath.Dir(rscript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rscript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedRuntimeMarker(filepath.Join(generation, managedRuntimeMarkerName), manager.managedRMarker(runtime)); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedRuntimeGeneration(filepath.Join(config.CondaEnvsPath, runtime.entry.Name), generation); err != nil {
		t.Skipf("managed runtime activation is unavailable on this test host: %v", err)
	}
	if err := manager.validateSessionRuntimeGeneration(SessionSpec{
		Language: "r", Environment: "r", RuntimeGeneration: runtime.activationGeneration,
	}); err != nil {
		t.Fatalf("validate managed R generation: %v", err)
	}
	if err := manager.validateSessionRuntimeGeneration(SessionSpec{
		Language: "r", Environment: "r", RuntimeGeneration: "stale-generation",
	}); err == nil {
		t.Fatal("stale managed R generation was accepted")
	}
}

func TestManagedRReadyGenerationIsReusedWithoutReinstall(t *testing.T) {
	config := bundledManagedRConfig(t)
	config.AssetRoot = filepath.Join(repositoryRootForCondaRuntimeTest(t), "assets", "optional")
	config.WorkerPath = filepath.Join(config.AssetRoot, "kernels", "kernel_worker.py")
	config.CondaEnvsPath = t.TempDir()
	config.Micromamba = filepath.Join(t.TempDir(), "micromamba-that-must-not-run")
	config.RWorkerPath = filepath.Join(t.TempDir(), "kernel_worker.R")
	if err := os.WriteFile(config.RWorkerPath, []byte("# worker fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(config)
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	generation := manager.managedRGenerationPath(runtime)
	rscript := environmentExecutableCandidates(generation, "Rscript")[0]
	if err := os.MkdirAll(filepath.Dir(rscript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rscript, []byte("#!/bin/sh\necho SYNON_R_VERSION=4.5.3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedRuntimeMarker(filepath.Join(generation, managedRuntimeMarkerName), manager.managedRMarker(runtime)); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedRuntimeGeneration(filepath.Join(config.CondaEnvsPath, runtime.entry.Name), generation); err != nil {
		t.Skipf("managed runtime activation is unavailable on this test host: %v", err)
	}
	if err := manager.EnsureManagedREnvironment(nil); err != nil {
		t.Fatalf("ready managed R generation was not reused: %v", err)
	}
}
