//go:build linux

package detached

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func startupBackendFixture(t *testing.T) (*workspace.Store, workspace.KernelExecutionBackend) {
	t.Helper()
	home := t.TempDir()
	store, err := workspace.Open(filepath.Join(home, "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	spec := kernelruntime.SessionSpec{OwnerID: "owner", ProjectID: "project", FrameID: frame.ID, RootFrameID: frame.RootFrameID, FrameIncarnationID: frame.IncarnationID, RootFrameIncarnationID: frame.IncarnationID, AgentName: "OPERON", KernelKind: "operon", Language: "python", Environment: "python", WorkspaceDir: home}
	spec.KernelID, err = kernelruntime.StableSessionID(spec)
	if err != nil {
		t.Fatal(err)
	}
	socketRoot, err := DefaultSocketRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.CreateKernelExecutionBackend(context.Background(), workspace.CreateKernelExecutionBackendInput{
		BackendID: "backend-startup", OwnerUserID: spec.OwnerID, ProjectID: spec.ProjectID, RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID, KernelID: spec.KernelID, KernelGeneration: 1, SessionSpec: durableSessionSpec(spec),
		ExecutorInstanceID: "executor-startup", MachineBootID: "test-boot", BackendGeneration: 1, SocketPath: filepath.Join(socketRoot, "startup.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, backend
}

func TestExecutorStartupFailureSettlesBeforeReadinessTimeout(t *testing.T) {
	store, backend := startupBackendFixture(t)
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(repo, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{Python: filepath.Join(t.TempDir(), "missing-python"), AssetRoot: assets, ManifestPath: filepath.Join(assets, "kernel-compute.manifest.json"), WorkerPath: filepath.Join(assets, "kernels", "kernel_worker.py")})
	executor := &Executor{Store: store, Manager: manager, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID, SocketPath: backend.SocketPath}
	if err := executor.Run(context.Background()); err == nil {
		t.Fatal("missing interpreter unexpectedly started")
	}
	current, _, err := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
	if err != nil || current.State != workspace.KernelExecutionBackendStateStopped || current.ExecutorPID != int64(os.Getpid()) || current.WorkerPID != 0 {
		t.Fatalf("startup failure left a starting backend: %#v %v", current, err)
	}
	started := time.Now()
	_, err = (&Backend{Store: store}).waitForReady(context.Background(), backend.BackendID, backend.BackendGeneration, time.Second)
	var failure *StartupFailureError
	if !errors.As(err, &failure) || failure.Receipt.Stage != "worker_start" || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("startup cause hidden behind timeout: %v", err)
	}
}

func TestStartingLiveExecutorCannotBeReplacedByElapsedTime(t *testing.T) {
	store, backend := startupBackendFixture(t)
	executor := &Executor{Store: store, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID}
	if err := executor.ClaimStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _, err := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Backend{Store: store}).settleExitedStartup(context.Background(), current); err == nil {
		t.Fatal("live initialization was retired")
	}
	after, _, err := store.GetKernelExecutionBackend(context.Background(), backend.BackendID)
	if err != nil || after.State != workspace.KernelExecutionBackendStateStarting || after.BackendGeneration != current.BackendGeneration {
		t.Fatalf("live authority changed: %#v %v", after, err)
	}
}

func TestSupervisorObservesAbsentGenerationWithoutLaunching(t *testing.T) {
	launcher, err := NewSystemdUserExecutorLauncher()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := launcher.Observe(ctx, "kernel-backend-00000000-0000-4000-8000-000000000069", 1)
	if err != nil {
		t.Skipf("user systemd observation unavailable: %v", err)
	}
	if state != ExecutorLaunchExited {
		t.Fatalf("absent executor observation=%s", state)
	}
}

func TestStartupReconciliationSettlesExitedGenerationWithoutNewToolCall(t *testing.T) {
	store, backend := startupBackendFixture(t)
	launcher, err := NewSystemdUserExecutorLauncher()
	if err != nil {
		t.Fatal(err)
	}
	controller := &Backend{Store: store, Launcher: launcher, StartTimeout: time.Nanosecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := controller.ReconcileStartups(ctx); err != nil {
		t.Fatal(err)
	}
	current, _, err := store.GetKernelExecutionBackend(ctx, backend.BackendID)
	if err != nil || current.State != workspace.KernelExecutionBackendStateStopped || current.ExecutorPID != 0 {
		t.Fatalf("unobserved exit still advertises initialization: %#v %v", current, err)
	}
	receipt, found, err := store.GetKernelStartupFailure(ctx, backend.BackendID, backend.BackendGeneration)
	if err != nil || !found || receipt.Stage != "process_exit" {
		t.Fatalf("reconciled failure: %#v %v", receipt, err)
	}
}
