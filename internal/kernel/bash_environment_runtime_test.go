//go:build !windows

package kernel

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashSessionSeparatesControlRuntimeFromTargetEnvironment(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	envs := t.TempDir()
	prefix := filepath.Join(envs, "native-only")
	writeExecutable(t, filepath.Join(prefix, "bin", "Rscript"), "#!/bin/sh\nprintf 'selected-runtime\\n'\n")
	manager := newLifecycleTestManager(t, Config{Python: python, CondaEnvsPath: envs})
	spec := SessionSpec{KernelID: "native-shell", OwnerID: "owner", ProjectID: "project", FrameID: "frame", RootFrameID: "frame",
		FrameIncarnationID: "frame-incarnation", RootFrameIncarnationID: "frame-incarnation",
		AgentName: "OPERON", KernelKind: "bash", Language: "python", Environment: "native-only", WorkspaceDir: t.TempDir()}
	executable, _, environment, err := manager.sessionRuntime(spec)
	if err != nil || executable != python {
		t.Fatalf("shell supervisor requires a target Python: %q %v", executable, err)
	}
	if !strings.Contains(strings.Join(environment, "\n"), "CONDA_PREFIX="+prefix) {
		t.Fatal("target environment binding lost")
	}
	worker, err := manager.StartSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	code := `import subprocess
print(subprocess.run(["/bin/bash", "-lc", "Rscript --version"], check=True, capture_output=True, text=True).stdout)`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, code, "user")
	if err != nil || response.Error != "" || !strings.Contains(response.Stdout, "selected-runtime") {
		t.Fatalf("selected native executable did not run: %#v %v", response, err)
	}
	// A Python analysis cell must still require the selected environment's
	// Python. Separating the shell supervisor must not create a silent fallback.
	spec.KernelKind = "analysis"
	if _, _, _, err := manager.sessionRuntime(spec); err == nil {
		t.Fatal("Python analysis fell back to control runtime")
	}
}
