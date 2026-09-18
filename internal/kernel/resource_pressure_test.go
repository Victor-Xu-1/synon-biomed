package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"synon-go/internal/processsupervisor"
	"testing"
	"time"
)

func TestResourceRecoverySettlesRealWorkerAndPreservesCheckpoint(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspace := t.TempDir()
	spec := SessionSpec{KernelID: "pressure-kernel", FrameID: "pressure-frame", RootFrameID: "pressure-frame", AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: workspace}
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(SubmitRequest{KernelID: session.ID, FrameID: spec.FrameID, KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: "pressure-cell", ToolUseID: "pressure-call", Code: "from pathlib import Path\nimport time\nPath('checkpoint.txt').write_text('completed-unit')\nprint('checkpoint-ready', flush=True)\ntime.sleep(120)", Origin: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	<-handle.Started()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workspace, "checkpoint.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real worker did not produce checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !handle.RequestResourceRecovery(processsupervisor.MemoryPressure{Status: "pressured", CurrentBytes: 95, HighBytes: 100, LimitBytes: 100, FullStallPercent: 90}) {
		t.Fatal("active execution rejected resource recovery")
	}
	outcome := waitForLifecycleOutcome(t, handle)
	var failure *ResourcePressureFailure
	if !errors.As(outcome.Err, &failure) || outcome.Response.Interrupted || outcome.TimedOut || outcome.Response.Trace["resource_pressure"] == nil {
		t.Fatalf("wrong resource terminal: %+v", outcome)
	}
	if handle.IsRunning() || handle.RequestResourceRecovery(failure.Observation) {
		t.Fatal("terminal execution accepts another intervention")
	}
	if !strings.Contains(outcome.Response.Stdout, "checkpoint-ready") {
		t.Fatalf("lost partial output: %q", outcome.Response.Stdout)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "checkpoint.txt"))
	if err != nil || string(data) != "completed-unit" {
		t.Fatal("checkpoint lost")
	}
	select {
	case <-session.Worker.Stopped():
	case <-time.After(time.Second):
		t.Fatal("settled before physical worker exit")
	}
	restarted, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	response, err := restarted.Worker.Execute(context.Background(), "from pathlib import Path\nprint(Path('checkpoint.txt').read_text())", "user")
	if err != nil || response.Error != "" || !strings.Contains(response.Stdout, "completed-unit") {
		t.Fatalf("could not resume from checkpoint: %+v %v", response, err)
	}
}

func TestResourceRecoveryCannotReclassifyCompletedWorkerExecution(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	spec := SessionSpec{KernelID: "completed-kernel", FrameID: "completed-frame", RootFrameID: "completed-frame", AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir()}
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Submit(SubmitRequest{KernelID: session.ID, FrameID: spec.FrameID, KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: "completed-cell", ToolUseID: "completed-call", Code: "print('completed')", Origin: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	outcome := waitForLifecycleOutcome(t, handle)
	if outcome.Err != nil || outcome.Response.Error != "" || !strings.Contains(outcome.Response.Stdout, "completed") {
		t.Fatalf("normal outcome: %+v", outcome)
	}
	if handle.RequestResourceRecovery(processsupervisor.MemoryPressure{Status: "pressured", LimitBytes: 100}) {
		t.Fatal("stale observation interrupted completed execution")
	}
	response, err := session.Worker.Execute(context.Background(), "print('still healthy')", "user")
	if err != nil || response.Error != "" || !strings.Contains(response.Stdout, "still healthy") {
		t.Fatalf("stale observation killed reusable worker: %+v %v", response, err)
	}
}
