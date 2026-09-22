//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSessionRuntimeSelectsManagedRAndPythonExecutables(t *testing.T) {
	for _, valid := range []string{"r", "R-4.5", "analysis_1", "env.name"} {
		if !ValidEnvironmentName(valid) {
			t.Fatalf("valid Claude environment name %q was rejected", valid)
		}
	}
	for _, invalid := range []string{"", " r ", ".hidden", "r+unsafe", "r@unsafe", "r/unsafe", strings.Repeat("a", 101), strings.Repeat("é", 51)} {
		if ValidEnvironmentName(invalid) {
			t.Fatalf("invalid Claude environment name %q was accepted", invalid)
		}
	}
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	rscript := writeExecutable(t, filepath.Join(envs, defaultManagedREnvironment, "bin", "Rscript"), "#!/bin/sh\nexit 0\n")
	python := writeExecutable(t, filepath.Join(envs, "analysis", "bin", "python"), "#!/bin/sh\nexit 0\n")
	rWorker := filepath.Join(root, "kernel_worker.R")
	if err := os.WriteFile(rWorker, []byte("# worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		Python: "/usr/bin/python3", CondaHome: condaHome, CondaEnvsPath: envs,
		WorkerPath: filepath.Join(root, "kernel_worker.py"), RWorkerPath: rWorker, DefaultREnv: "r",
	})

	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, arguments, environment, err := manager.sessionRuntime(SessionSpec{
		KernelID: "r-kernel", Language: "r", Environment: "r", WorkspaceDir: workspace,
	})
	expectedArguments := []string{"--no-init-file", "--no-environ", "--no-site-file", rWorker}
	if err != nil || executable != rscript || !reflect.DeepEqual(arguments, expectedArguments) {
		t.Fatalf("R runtime executable=%q arguments=%#v err=%v", executable, arguments, err)
	}
	joined := strings.Join(environment, "\n")
	rLibrary := filepath.Join(workspace, ".r-libs", "r-kernel", defaultManagedREnvironment)
	if !strings.Contains(joined, "CONDA_PREFIX="+filepath.Join(envs, defaultManagedREnvironment)) ||
		!strings.Contains(joined, "R_LIBS_USER="+rLibrary) ||
		!strings.Contains(joined, "OPERON_WRITABLE_ROOTS="+workspace) ||
		!strings.Contains(joined, "OPERON_DLOPEN_EXEMPT="+rLibrary) {
		t.Fatalf("R environment=%q", joined)
	}

	executable, arguments, environment, err = manager.sessionRuntime(SessionSpec{
		Language: "python", Environment: "analysis", WorkspaceDir: workspace,
	})
	if err != nil || executable != python || len(arguments) != 1 || arguments[0] != manager.config.WorkerPath {
		t.Fatalf("Python runtime executable=%q arguments=%#v err=%v", executable, arguments, err)
	}
	if joined := strings.Join(environment, "\n"); !strings.Contains(joined, "OPERON_WRITABLE_ROOTS="+workspace) {
		t.Fatalf("Python environment=%q", joined)
	}
	if _, _, _, err := manager.sessionRuntime(SessionSpec{
		Language: "python", Environment: "analysis", RuntimeGeneration: "generation-b", WorkspaceDir: workspace,
	}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale managed Python generation error=%v", err)
	}
	if _, _, _, err := manager.sessionRuntime(SessionSpec{Language: "r", Environment: "../escape"}); err == nil {
		t.Fatal("path traversal environment was accepted")
	}
	for _, invalid := range []string{strings.Repeat("a", 101), "r+unsafe", "r@unsafe"} {
		if manager.RuntimeReady("r", invalid) {
			t.Fatalf("invalid Claude environment name %q was accepted", invalid)
		}
	}
	if manager.RuntimeReady("r", "r") || !manager.RuntimeReady("python", "analysis") ||
		manager.RuntimeReady("r", "../escape") || manager.RuntimeReady("unknown", "r") {
		t.Fatalf("runtime readiness r=%t python=%t escape=%t unknown=%t",
			manager.RuntimeReady("r", "r"), manager.RuntimeReady("python", "analysis"),
			manager.RuntimeReady("r", "../escape"), manager.RuntimeReady("unknown", "r"))
	}
	missing := NewManager(Config{Python: "/missing/python", CondaEnvsPath: filepath.Join(root, "missing")})
	if missing.RuntimeReady("python", "python") || missing.RuntimeReady("r", "r") {
		t.Fatal("missing language runtimes were reported ready")
	}
}

func TestSessionRuntimeUsesBundledManagedPythonGenerationAuthority(t *testing.T) {
	config := bundledManagedPythonConfig(t)
	root := repositoryRootForCondaRuntimeTest(t)
	config.CondaEnvsPath = t.TempDir()
	config.PythonHelperPath = filepath.Join(root, "assets", "optional", "kernels", "cheminfo_render_helpers.py")
	manager := NewManager(config)
	runtime, err := loadManagedPythonRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	generationPath := manager.managedPythonGenerationPath(runtime)
	python := writeExecutable(t, filepath.Join(generationPath, "bin", "python"), "#!/bin/sh\nexit 0\n")
	if err := manager.installManagedPythonHelpers(runtime, generationPath); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedRuntimeMarker(
		filepath.Join(generationPath, managedRuntimeMarkerName), manager.managedPythonMarker(runtime),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(generationPath, filepath.Join(config.CondaEnvsPath, runtime.entry.Name)); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	executable, _, environment, err := manager.sessionRuntime(SessionSpec{
		Language: "python", Environment: runtime.entry.Name,
		RuntimeGeneration: runtime.activationGeneration, WorkspaceDir: workspace,
	})
	if err != nil || executable != python {
		t.Fatalf("bundled managed Python executable=%q err=%v", executable, err)
	}
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	if !strings.Contains(joined, "\nCONDA_PREFIX="+generationPath+"\n") {
		t.Fatalf("bundled managed Python environment=%q", joined)
	}
	if _, _, _, err := manager.sessionRuntime(SessionSpec{
		Language: "python", Environment: runtime.entry.Name,
		RuntimeGeneration: "stale-generation", WorkspaceDir: workspace,
	}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale bundled managed Python generation error=%v", err)
	}
}

func TestRepairDefaultREnvironmentRequiresVerifiedBundledCatalog(t *testing.T) {
	root := t.TempDir()
	argumentsPath := filepath.Join(root, "arguments.txt")
	micromamba := writeExecutable(t, filepath.Join(root, "micromamba"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argumentsPath+"\nexit 0\n")
	manager := NewManager(Config{
		Micromamba: micromamba, CondaHome: filepath.Join(root, "conda"),
		CondaEnvsPath: filepath.Join(root, "conda", "envs"), DefaultREnv: "r",
	})
	if err := manager.RepairDefaultREnvironment(context.Background()); err == nil {
		t.Fatal("R repair accepted an unverified dynamic package request")
	}
	if _, err := os.Stat(argumentsPath); !os.IsNotExist(err) {
		t.Fatalf("unverified R repair invoked micromamba: %v", err)
	}
	status := manager.RuntimeEnvironmentStatuses(false)
	var managedR *RuntimeEnvironmentStatus
	for index := range status {
		if status[index].Language == "r" && status[index].EnvironmentName == defaultManagedREnvironment {
			managedR = &status[index]
			break
		}
	}
	if managedR == nil || managedR.Status != "failed" {
		t.Fatalf("unexpected R runtime status=%#v", status)
	}
}

func TestManagerPreservesEmptyOptionalPathsAndRejectsRWorkerDirectory(t *testing.T) {
	empty := NewManager(Config{})
	if empty.config.AssetRoot != "" || empty.config.ManifestPath != "" || empty.config.WorkerPath != "" || empty.config.RWorkerPath != "" {
		t.Fatalf("empty optional paths were converted into working-directory paths: %#v", empty.config)
	}
	root := t.TempDir()
	envs := filepath.Join(root, "envs")
	writeExecutable(t, filepath.Join(envs, "r", "bin", "Rscript"), "#!/bin/sh\nexit 0\n")
	workerDirectory := filepath.Join(root, "worker-directory")
	if err := os.MkdirAll(workerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{CondaEnvsPath: envs, RWorkerPath: workerDirectory})
	if _, _, _, err := manager.sessionRuntime(SessionSpec{Language: "r", Environment: "r"}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("R worker directory error=%v", err)
	}
}

func writeExecutable(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
