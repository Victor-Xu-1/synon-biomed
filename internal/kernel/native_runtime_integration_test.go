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

// This opt-in test exercises the packaged native installer, verified locks,
// scientific smoke, atomic activation, and reuse after manager restart. It
// deliberately uses an operator-provided disposable state root; it never
// touches a service's configured user data or the shared deployment.
func TestRealNativeCorePythonInstallAndReuse(t *testing.T) {
	if os.Getenv("SYNON_TEST_NATIVE_CORE_RUNTIME") != "1" {
		t.Skip("native core runtime integration is opt-in")
	}
	assetRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_ASSET_ROOT"))
	stateRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_STATE_ROOT"))
	if !filepath.IsAbs(assetRoot) || !filepath.IsAbs(stateRoot) {
		t.Fatal("native runtime test requires absolute asset and disposable state roots")
	}
	t.Setenv("SYNON_KERNEL_ASSET_ROOT", assetRoot)
	t.Setenv("SYNON_MICROMAMBA", "")
	condaHome := filepath.Join(stateRoot, "conda")
	envs := filepath.Join(condaHome, "envs")
	manager, err := DiscoverManagerWithPathsAndProxy(condaHome, envs, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	if err := manager.EnsureManagedPythonEnvironment(ctx); err != nil {
		t.Fatalf("install managed Python: %v", err)
	}
	if !manager.RuntimeReady("python", defaultManagedPythonEnvironment) {
		t.Fatal("managed Python not ready after native installation")
	}
	firstGeneration, err := manager.ManagedPythonActiveGeneration()
	if err != nil || firstGeneration == "" {
		t.Fatalf("Python generation=%q err=%v", firstGeneration, err)
	}
	python, err := manager.ManagedPythonExecutable()
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(ctx, python, "-I", "-c", "import rdkit,py3Dmol;print(rdkit.__version__+'|'+py3Dmol.__version__)").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "2024.03.5|2.5.4" {
		t.Fatalf("native Python import=%q err=%v", output, err)
	}
	pythonWorker := startNativeRuntimeTestWorker(t, manager, SessionSpec{
		KernelID: "native-python-worker", FrameID: "native-python-frame", RootFrameID: "native-python-frame",
		AgentName: "OPERON", KernelKind: "analysis", Language: "python",
		Environment: defaultManagedPythonEnvironment, WorkspaceDir: t.TempDir(),
	})
	pythonResult, err := pythonWorker.Execute(ctx, `from rdkit import Chem; print(Chem.MolFromSmiles("CCO").GetNumAtoms())`, "user")
	if err != nil || strings.TrimSpace(pythonResult.Stdout) != "3" || pythonResult.Error != "" {
		t.Fatalf("native Python worker response=%#v err=%v", pythonResult, err)
	}
	second := NewManager(manager.config)
	second.config.Micromamba = filepath.Join(stateRoot, "installer-must-not-run")
	if err := second.EnsureManagedPythonEnvironment(ctx); err != nil {
		t.Fatalf("restart did not reuse verified Python generation: %v", err)
	}
	secondGeneration, err := second.ManagedPythonActiveGeneration()
	if err != nil || secondGeneration != firstGeneration {
		t.Fatalf("reused generation=%q err=%v want=%q", secondGeneration, err, firstGeneration)
	}
}

func TestRealNativeCoreRInstallAndReuse(t *testing.T) {
	if os.Getenv("SYNON_TEST_NATIVE_CORE_RUNTIME") != "1" {
		t.Skip("native core runtime integration is opt-in")
	}
	assetRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_ASSET_ROOT"))
	stateRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_NATIVE_STATE_ROOT"))
	if !filepath.IsAbs(assetRoot) || !filepath.IsAbs(stateRoot) {
		t.Fatal("native runtime test requires absolute asset and disposable state roots")
	}
	t.Setenv("SYNON_KERNEL_ASSET_ROOT", assetRoot)
	t.Setenv("SYNON_MICROMAMBA", "")
	condaHome := filepath.Join(stateRoot, "conda")
	envs := filepath.Join(condaHome, "envs")
	manager, err := DiscoverManagerWithPathsAndProxy(condaHome, envs, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	if err := manager.EnsureManagedREnvironment(ctx); err != nil {
		t.Fatalf("install managed R: %v", err)
	}
	if !manager.RuntimeReady("r", defaultManagedREnvironment) {
		t.Fatal("managed R not ready after native installation")
	}
	firstGeneration, err := manager.ManagedRActiveGeneration()
	if err != nil || firstGeneration == "" {
		t.Fatalf("R generation=%q err=%v", firstGeneration, err)
	}
	prefix, err := manager.ManagedRActivePrefix()
	if err != nil {
		t.Fatal(err)
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, rscript, "--vanilla", "-e",
		"stopifnot(requireNamespace(\"data.table\", quietly=TRUE), requireNamespace(\"ggplot2\", quietly=TRUE), requireNamespace(\"jsonlite\", quietly=TRUE), requireNamespace(\"tidyverse\", quietly=TRUE));cat(as.character(getRversion()))")
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "4.5.3" {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("native R import=%q err=%v stderr=%q", output, err, stderr)
	}
	rWorker := startNativeRuntimeTestWorker(t, manager, SessionSpec{
		KernelID: "native-r-worker", FrameID: "native-r-frame", RootFrameID: "native-r-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r",
		Environment: defaultManagedREnvironment, WorkspaceDir: t.TempDir(),
	})
	rResult, err := rWorker.Execute(ctx, `cat(21 * 2, "\n")`, "user")
	if err != nil || strings.TrimSpace(rResult.Stdout) != "42" || rResult.Error != "" {
		t.Fatalf("native R worker response=%#v err=%v", rResult, err)
	}
	second := NewManager(manager.config)
	second.config.Micromamba = filepath.Join(stateRoot, "installer-must-not-run")
	if err := second.EnsureManagedREnvironment(ctx); err != nil {
		t.Fatalf("restart did not reuse verified R generation: %v", err)
	}
	secondGeneration, err := second.ManagedRActiveGeneration()
	if err != nil || secondGeneration != firstGeneration {
		t.Fatalf("reused R generation=%q err=%v want=%q", secondGeneration, err, firstGeneration)
	}
}

func startNativeRuntimeTestWorker(t *testing.T, manager *Manager, spec SessionSpec) *Worker {
	t.Helper()
	worker, err := manager.StartSession(spec)
	if err != nil {
		t.Fatalf("start native %s worker: %v", spec.Language, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Close(ctx); err != nil {
			t.Errorf("close native %s worker: %v", spec.Language, err)
		}
	})
	return worker
}
