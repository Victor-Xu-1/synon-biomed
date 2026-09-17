package kernel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"synon-go/internal/executionprep"
)

// Real public creation, not the repair path or an import-only probe. Installation
// and execution use disposable runtime state, never an active task environment.
func TestManagedEnvironmentRealRCreatePublishExecute(t *testing.T) {
	if os.Getenv("SYNON_TEST_REAL_R_CREATE") != "1" || runtime.GOOS != "linux" {
		t.Skip("opt-in Linux R creation integration requiring package network access")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..")
	root := t.TempDir()
	config := Config{
		Micromamba: filepath.Join(repo, "assets/optional/micromamba/linux-x86_64/micromamba"),
		CondaHome:  filepath.Join(root, "cache"), CondaEnvsPath: filepath.Join(root, "envs"),
		RWorkerPath:      filepath.Join(repo, "assets/optional/kernels/kernel_worker.R"),
		ExecutionTimeout: 10 * time.Second,
	}
	manager := newLifecycleTestManager(t, config)
	stopSupervisor := startManagedEnvironmentSupervisor(t, manager)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	input := CreateManagedEnvironmentInput{Name: "native-r", Language: "r", Packages: []string{"r-base=4.4"}, ImportNames: []string{"stats", "jsonlite"}}
	environment, err := manager.CreateManagedEnvironment(ctx, input)
	if err != nil || environment.Status != "ready" {
		t.Fatalf("public native-R creation: %#v, %v", environment, err)
	}
	prefix := filepath.Join(config.CondaEnvsPath, ".generations", input.Name, environment.Generation)
	if _, err := managedPythonExecutableAtPrefix(prefix); err == nil {
		t.Fatal("native-only R probe unexpectedly acquired Python")
	}
	for _, test := range []struct{ language, source, effect string }{
		{"r", `utils::install.packages("package-name")`, executionprep.PackageMutation},
		{"r", `installer <- utils::install.packages; installer("package-name")`, executionprep.PackageMutation},
		{"r", `message('install.packages("example")')`, ""},
		{"r", `download.file("https://example.org/data.gz", "data.gz")`, executionprep.FileAcquisition},
		{"python", `import rpy2.robjects as bridge
bridge.r('BiocManager::install("package-name")')`, executionprep.PackageMutation},
		{"bash", `Rscript --vanilla -e 'install.packages("package-name")'`, executionprep.PackageMutation},
		{"bash", "Rscript - <<'SCRIPT'\ninstall.packages('package-name')\nSCRIPT", executionprep.PackageMutation},
	} {
		result, err := manager.PrepareExecutionSource(ctx, executionprep.Request{Language: test.language, Source: test.source, Environment: input.Name})
		if err != nil || len(result.Unresolved) != 0 || (test.effect == "" && len(result.Requirements) != 0) ||
			(test.effect != "" && (len(result.Requirements) != 1 || result.Requirements[0].Effect != test.effect)) {
			t.Fatalf("real R preparation %s %q: %#v %v", test.language, test.source, result, err)
		}
	}
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "native-r-worker", FrameID: "native-r-frame", RootFrameID: "native-r-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r", Environment: input.Name, WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := worker.Execute(ctx, `cat(sum(c(19, 23)), "\n")`, "user")
	if err != nil || response.Error != "" || strings.TrimSpace(response.Stdout) != "42" {
		t.Fatalf("created R runtime execution: %#v, %v", response, err)
	}
	shell, err := manager.StartSession(SessionSpec{
		KernelID: "native-r-shell", FrameID: "native-r-frame", RootFrameID: "native-r-frame",
		AgentName: "OPERON", KernelKind: "bash", Language: "python", Environment: input.Name, WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err = shell.Execute(ctx, `import subprocess
print(subprocess.run(["/bin/bash", "-lc", "Rscript --vanilla -e 'cat(sum(c(19,23)))'"], check=True, capture_output=True, text=True).stdout)`, "user")
	if err != nil || response.Error != "" || strings.TrimSpace(response.Stdout) != "42" {
		t.Fatalf("shell could not execute the native-only R environment: %#v %v", response, err)
	}
	stopSupervisor()
	restarted := NewManager(config)
	startManagedEnvironmentSupervisor(t, restarted)
	reused, err := restarted.CreateManagedEnvironment(ctx, input)
	if err != nil || reused.Generation != environment.Generation || reused.Status != "ready" {
		t.Fatalf("restart changed verified generation: %#v, %v", reused, err)
	}
}
