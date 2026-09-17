package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureAndExecuteSessionReuseStableIdentityAndRecoverRealWorker(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	workspaceDir := t.TempDir()
	spec := SessionSpec{
		OwnerID: "owner-agent", ProjectID: "project-agent", FrameID: "frame-agent",
		FrameIncarnationID: "incarnation-agent", RootFrameID: "root-agent",
		RootFrameIncarnationID: "root-incarnation-agent", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python",
		WorkspaceDir: workspaceDir,
	}
	first, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Worker == nil || first.Reused {
		t.Fatalf("first session=%#v", first)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcome, err := manager.ExecuteSession(ctx, spec, SubmitRequest{
		ExecID: "exec-1", ToolUseID: "tool-1", Code: "value = 41\nprint(value)", Origin: "agent",
	})
	if err != nil || outcome.Err != nil || strings.TrimSpace(outcome.Response.Stdout) != "41" {
		t.Fatalf("first outcome=%#v err=%v", outcome, err)
	}
	outcome, err = manager.ExecuteSession(ctx, spec, SubmitRequest{
		ExecID: "exec-file", ToolUseID: "tool-file",
		Code: "from pathlib import Path\nPath('agent-output.txt').write_text('artifact')", Origin: "agent",
	})
	if err != nil || outcome.Err != nil {
		t.Fatalf("file outcome=%#v err=%v", outcome, err)
	}
	if len(outcome.FilesWritten) != 1 || outcome.FilesWritten[0].Path != "agent-output.txt" || len(outcome.FilesWritten[0].SHA256) != 64 {
		t.Fatalf("files written=%#v", outcome.FilesWritten)
	}

	second, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.ID != first.ID || second.Worker.Generation() != first.Worker.Generation() {
		t.Fatalf("reused=%#v first=%#v", second, first)
	}
	outcome, err = manager.ExecuteSession(ctx, spec, SubmitRequest{
		ExecID: "exec-2", ToolUseID: "tool-2", Code: "print(value + 1)", Origin: "agent",
	})
	if err != nil || strings.TrimSpace(outcome.Response.Stdout) != "42" {
		t.Fatalf("persistent outcome=%#v err=%v", outcome, err)
	}
	generation := first.Worker.Generation()
	if err := first.Worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != first.ID || recovered.Reused || recovered.Worker.Generation() <= generation {
		t.Fatalf("recovered=%#v generation=%d", recovered, generation)
	}
	outcome, err = manager.ExecuteSession(ctx, spec, SubmitRequest{ExecID: "exec-3", ToolUseID: "tool-3", Code: "print('recovered')", Origin: "agent"})
	if err != nil || strings.TrimSpace(outcome.Response.Stdout) != "recovered" {
		t.Fatalf("recovered outcome=%#v err=%v", outcome, err)
	}
}

func TestEnsureSessionRestartsWhenMountAuthorityChanges(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	spec := SessionSpec{
		OwnerID: "owner-mount", ProjectID: "project-mount", FrameID: "frame-mount",
		FrameIncarnationID: "incarnation-mount", RootFrameID: "root-mount",
		RootFrameIncarnationID: "root-incarnation-mount", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}
	first, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	spec.Mounts = []WorkerMount{{Path: external, Writable: true}}
	second, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Reused || second.Worker == first.Worker || second.Worker.Generation() <= first.Worker.Generation() {
		t.Fatalf("mount authority restart first=%#v second=%#v", first, second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcome, err := manager.ExecuteSession(ctx, spec, SubmitRequest{
		ExecID: "exec-mount", ToolUseID: "tool-mount", WorkingDir: external,
		Code: "from pathlib import Path\nPath('mounted.txt').write_text('mounted')", Origin: "agent",
	})
	if err != nil || outcome.Err != nil {
		t.Fatalf("mounted execution=%#v err=%v", outcome, err)
	}
	if raw, err := os.ReadFile(filepath.Join(external, "mounted.txt")); err != nil || string(raw) != "mounted" {
		t.Fatalf("mounted file=%q err=%v", raw, err)
	}
}

func TestEnsureSessionRestartsWhenEgressAuthorityChanges(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	spec := SessionSpec{
		OwnerID: "owner-egress", ProjectID: "project-egress", FrameID: "frame-egress",
		FrameIncarnationID: "incarnation-egress", RootFrameID: "root-egress",
		RootFrameIncarnationID: "root-incarnation-egress", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}
	first, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.EgressAllowedDomains = []string{"files.rcsb.org"}
	second, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Reused || second.Worker == first.Worker ||
		second.Worker.Generation() <= first.Worker.Generation() {
		t.Fatalf("egress authority restart first=%#v second=%#v", first, second)
	}
	state := lifecycleWorkerState(second.Worker)
	if state == nil {
		t.Fatal("restarted egress worker has no lifecycle state")
	}
	state.mu.Lock()
	allowed := append([]string(nil), state.spec.EgressAllowedDomains...)
	state.mu.Unlock()
	if len(allowed) != 1 || allowed[0] != "files.rcsb.org" {
		t.Fatalf("restarted egress authority=%#v", allowed)
	}
}

func TestOperonSessionHasIndependentStableIdentityAndInventoryKind(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	spec := SessionSpec{OwnerID: "owner-agent", ProjectID: "project-agent", FrameID: "frame-agent", FrameIncarnationID: "incarnation-agent", RootFrameID: "root-agent", RootFrameIncarnationID: "root-incarnation-agent", AgentName: "OPERON", KernelKind: "operon", Language: "python", Environment: "repl", WorkspaceDir: t.TempDir()}
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	analysisID, err := StableSessionID(SessionSpec{OwnerID: spec.OwnerID, ProjectID: spec.ProjectID, FrameID: "frame-agent", FrameIncarnationID: spec.FrameIncarnationID, RootFrameID: "root-agent", RootFrameIncarnationID: spec.RootFrameIncarnationID, KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: spec.WorkspaceDir})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == analysisID {
		t.Fatalf("operon identity collided with analysis: %s", session.ID)
	}
	inventory := manager.ListSessionKernels("root-agent")
	if len(inventory) != 1 || inventory[0].KernelKind != "operon" || inventory[0].KernelID != session.ID {
		t.Fatalf("inventory=%#v", inventory)
	}
}

func TestStableSessionIdentityFencesOwnerProjectAndFrameIncarnation(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	base := SessionSpec{
		OwnerID: "owner-a", ProjectID: "project-a", FrameID: "frame-agent",
		FrameIncarnationID: "incarnation-a", RootFrameID: "root-agent",
		RootFrameIncarnationID: "root-incarnation-a", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	}
	baseID, err := StableSessionID(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []SessionSpec{base, base, base, base, base}
	mutations[0].OwnerID = "owner-b"
	mutations[1].ProjectID = "project-b"
	mutations[2].FrameIncarnationID = "incarnation-b"
	mutations[3].RootFrameIncarnationID = "root-incarnation-b"
	mutations[4].RuntimeGeneration = "generation-b"
	for _, mutated := range mutations {
		id, err := StableSessionID(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if id == baseID {
			t.Fatalf("identity mutation reused stable id %q: %#v", id, mutated)
		}
	}
	if _, err := manager.EnsureSession(base); err != nil {
		t.Fatal(err)
	}
	stale := base
	stale.FrameIncarnationID = "incarnation-b"
	if _, err := manager.Submit(SubmitRequest{
		OwnerID: stale.OwnerID, ProjectID: stale.ProjectID, FrameID: stale.FrameID,
		FrameIncarnationID: stale.FrameIncarnationID, RootFrameIncarnationID: stale.RootFrameIncarnationID,
		KernelKind: stale.KernelKind,
		Language:   stale.Language, Environment: stale.Environment, ExecID: "stale-exec",
		ToolUseID: "stale-tool", Code: "print('must not execute')", Origin: "agent",
	}); err == nil || !strings.Contains(err.Error(), "no running") {
		t.Fatalf("stale incarnation submit error=%v", err)
	}
}

func TestCloseSessionStopsOnlyMatchingRealWorkers(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{ExecutionTimeout: 5 * time.Second})
	first, err := manager.EnsureSession(SessionSpec{
		OwnerID: "owner-a", ProjectID: "project-a", FrameID: "frame-a", FrameIncarnationID: "incarnation-a",
		RootFrameID: "root-a", RootFrameIncarnationID: "root-incarnation-a", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.EnsureSession(SessionSpec{
		OwnerID: "owner-b", ProjectID: "project-b", FrameID: "frame-b", FrameIncarnationID: "incarnation-b",
		RootFrameID: "root-b", RootFrameIncarnationID: "root-incarnation-b", AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed, err := manager.CloseSession(ctx, "root-a")
	if err != nil || closed != 1 {
		t.Fatalf("closed=%d err=%v", closed, err)
	}
	select {
	case <-first.Worker.done:
	case <-time.After(time.Second):
		t.Fatal("matching worker remained alive")
	}
	select {
	case <-second.Worker.done:
		t.Fatal("unrelated worker was closed")
	default:
	}
	if inventory := manager.ListSessionKernels("root-a"); len(inventory) != 0 {
		t.Fatalf("closed inventory = %#v", inventory)
	}
	if inventory := manager.ListSessionKernels("root-b"); len(inventory) != 1 {
		t.Fatalf("unrelated inventory = %#v", inventory)
	}
}
