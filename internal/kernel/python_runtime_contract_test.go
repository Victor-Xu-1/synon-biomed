package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkerUsesPythonRuntimeErrorsInsteadOfDomainPreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kernel process confinement is unavailable on Windows")
	}
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	if _, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-runtime-contract", FrameID: "frame-runtime-contract",
		RootFrameID: "root-runtime-contract", AgentName: "OPERON",
		Language: "python", Environment: "python", WorkspaceDir: workspaceDir,
	}); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(workspaceDir, "analysis.py")
	if err := os.WriteFile(scriptPath, []byte(`from pathlib import Path

def score(left, right):
    return left + right

value = score(1, 2, 3)
Path("must-not-run").write_text(str(value))
`), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := `source = open("analysis.py", encoding="utf-8").read()
exec(compile(source, "analysis.py", "exec"))`
	first, err := manager.Submit(SubmitRequest{
		KernelID: "kernel-runtime-contract", FrameID: "frame-runtime-contract",
		Language: "python", Environment: "python", ExecID: "exec-runtime-invalid",
		ToolUseID: "tool-runtime-invalid", ToolName: "python", Origin: "agent", Code: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := waitForLifecycleOutcome(t, first)
	if invalid.Err != nil || !strings.Contains(invalid.Response.Error, "TypeError") || len(invalid.Response.Preflight) != 0 {
		t.Fatalf("Python runtime failure=%#v", invalid)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "must-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime continued after the failing expression: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte(`from pathlib import Path

def score(left, right):
    return left + right

Path("runtime-ran").write_text(str(score(1, 3)))
`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Submit(SubmitRequest{
		KernelID: "kernel-runtime-contract", FrameID: "frame-runtime-contract",
		Language: "python", Environment: "python", ExecID: "exec-runtime-valid",
		ToolUseID: "tool-runtime-valid", ToolName: "python", Origin: "agent", Code: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := waitForLifecycleOutcome(t, second)
	if valid.Err != nil || valid.Response.Error != "" || len(valid.Response.Preflight) != 0 {
		t.Fatalf("Python valid execution=%#v", valid)
	}
	if contents, err := os.ReadFile(filepath.Join(workspaceDir, "runtime-ran")); err != nil || string(contents) != "4" {
		t.Fatalf("Python output=%q err=%v", contents, err)
	}
}
