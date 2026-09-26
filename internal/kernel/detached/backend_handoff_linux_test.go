//go:build linux

package detached

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

type rejectingReplacementLauncher struct {
	err error
}

func (launcher rejectingReplacementLauncher) Launch(ExecutorLaunchRequest) error {
	return launcher.err
}

func (rejectingReplacementLauncher) Observe(context.Context, string, int64) (ExecutorLaunchState, error) {
	return ExecutorLaunchUnknown, nil
}

func TestEnsureSessionDrainsIdlePredecessorBeforeChangedConfinement(t *testing.T) {
	store, initial := startupBackendFixture(t)
	durable, err := workspace.DecodeKernelExecutionSessionSpecV1(initial.SessionSpecJSON)
	if err != nil {
		t.Fatal(err)
	}
	spec := kernelSessionSpec(durable)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python: python, AssetRoot: assets,
		ManifestPath:    filepath.Join(assets, "kernel-compute.manifest.json"),
		WorkerPath:      filepath.Join(assets, "kernels", "kernel_worker.py"),
		ShutdownTimeout: time.Second,
	})
	runCtx, stop := context.WithCancel(context.Background())
	executor := &Executor{
		Store: store, Manager: manager, BackendID: initial.BackendID,
		BackendGeneration: initial.BackendGeneration, ExecutorInstanceID: initial.ExecutorInstanceID,
		SocketPath: initial.SocketPath, ResultSpoolDir: filepath.Join(spec.WorkspaceDir, "spool"),
		HeartbeatInterval: time.Second, IdleTimeout: time.Minute,
	}
	done := make(chan error, 1)
	go func() { done <- executor.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		_, _ = manager.CloseAll(context.Background())
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("detached executor did not stop")
		}
	})
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReady()
	if _, err := (&Backend{Store: store}).waitForReady(readyCtx, initial.BackendID, initial.BackendGeneration, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	replacementErr := errors.New("replacement launch observed")
	controller := &Backend{
		Store: store, Executable: executable, HomeDir: spec.WorkspaceDir,
		SocketRoot: filepath.Dir(initial.SocketPath), LogDir: filepath.Join(spec.WorkspaceDir, "logs"),
		Launcher: rejectingReplacementLauncher{err: replacementErr},
	}
	unchanged, err := controller.EnsureSession(readyCtx, spec)
	if err != nil || unchanged.BackendID != initial.BackendID {
		t.Fatalf("unchanged session was not reused: ref=%+v err=%v", unchanged, err)
	}
	readonly := filepath.Join(spec.WorkspaceDir, "published-output")
	if err := os.Mkdir(readonly, 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Mounts = append(spec.Mounts, kernelruntime.TrustedReadOnlyDirectoryMount(readonly))
	handoffCtx, cancelHandoff := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelHandoff()
	if _, err := controller.EnsureSession(handoffCtx, spec); !errors.Is(err, replacementErr) {
		current, found, readErr := store.GetKernelExecutionBackend(context.Background(), initial.BackendID)
		t.Fatalf("changed confinement did not reach replacement launch: %v backend=%+v found=%t read_err=%v", err, current, found, readErr)
	}
	retired, found, err := store.GetKernelExecutionBackend(context.Background(), initial.BackendID)
	if err != nil || !found || retired.State != workspace.KernelExecutionBackendStateStopped {
		t.Fatalf("idle predecessor was not durably stopped: backend=%+v found=%t err=%v", retired, found, err)
	}
}
