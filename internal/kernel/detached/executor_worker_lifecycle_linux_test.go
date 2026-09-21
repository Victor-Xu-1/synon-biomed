//go:build linux

package detached

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// A live control socket must not keep a backend ready after its worker exits.
// Use a real interpreter and SQLite, not a simulated heartbeat or worker.
func TestExecutorRetiresBackendAfterRealWorkerExit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	store, err := workspace.Open(filepath.Join(home, "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	spec := kernelruntime.SessionSpec{
		OwnerID: "owner", ProjectID: "project", FrameID: frame.ID, RootFrameID: frame.RootFrameID,
		FrameIncarnationID: frame.IncarnationID, RootFrameIncarnationID: frame.IncarnationID,
		AgentName: "OPERON", KernelKind: "operon", Language: "python", Environment: "python", WorkspaceDir: home,
	}
	spec.KernelID, err = kernelruntime.StableSessionID(spec)
	if err != nil {
		t.Fatal(err)
	}
	socketRoot, err := DefaultSocketRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.CreateKernelExecutionBackend(context.Background(), workspace.CreateKernelExecutionBackendInput{
		BackendID: "backend-worker-exit", OwnerUserID: spec.OwnerID, ProjectID: spec.ProjectID,
		RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID,
		KernelID: spec.KernelID, KernelGeneration: 1, SessionSpec: durableSessionSpec(spec),
		ExecutorInstanceID: "executor-worker-exit", MachineBootID: "test-boot", BackendGeneration: 1,
		SocketPath: filepath.Join(socketRoot, "worker-exit.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, AssetRoot: assets, ManifestPath: filepath.Join(assets, "kernel-compute.manifest.json"),
		WorkerPath: filepath.Join(assets, "kernels", "kernel_worker.py"), ShutdownTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	executor := &Executor{Store: store, Manager: manager, BackendID: backend.BackendID,
		BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID,
		SocketPath: backend.SocketPath, ResultSpoolDir: filepath.Join(home, "spool"), HeartbeatInterval: time.Second}
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx) }()
	defer func() {
		cancel()
		_, _ = manager.CloseAll(context.Background())
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("executor did not drain")
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, found, readErr := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if found && current.State == workspace.KernelExecutionBackendStateReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("executor did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	handle, err := manager.Submit(kernelruntime.SubmitRequest{
		KernelID: spec.KernelID, OwnerID: spec.OwnerID, ProjectID: spec.ProjectID, FrameID: spec.FrameID,
		FrameIncarnationID: spec.FrameIncarnationID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: "exit-cell", ToolUseID: "exit-call", Code: "import os; os._exit(7)", Origin: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.Done():
		handle.AcknowledgePersistence()
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		current, _, readErr := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.State == workspace.KernelExecutionBackendStateStopped || current.State == workspace.KernelExecutionBackendStateEvidenceLost {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dead worker still advertised as %s with heartbeat %v", current.State, current.HeartbeatAt)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecutorRejectsContainerDispatchWhileDrainingBeforeProviderAccess(t *testing.T) {
	executor := &Executor{draining: true}
	err := executor.dispatchContainerExecution(context.Background(), workspace.KernelDetachedExecutionRequestV1{ToolName: "bash"}, workspace.DetachedKernelExecution{})
	if err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("draining executor reached provider setup: %v", err)
	}
}
