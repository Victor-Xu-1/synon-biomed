package server

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

func TestRefreshKernelsClosesRealWorker(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	assetRoot := filepath.Join(repositoryRoot, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python:          python,
		AssetRoot:       assetRoot,
		ManifestPath:    filepath.Join(assetRoot, "kernel-compute.manifest.json"),
		WorkerPath:      filepath.Join(assetRoot, "kernels", "kernel_worker.py"),
		ShutdownTimeout: 2 * time.Second,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = manager.CloseAll(ctx)
	})
	if _, err := manager.Start("frame-api", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	app := New(Options{FileRoot: t.TempDir(), KernelManager: manager}).Handler()
	response := postProjectControlJSON(t, app, http.MethodPost, "/api/system/refresh-kernels", map[string]any{}, http.StatusOK)
	if len(response) != 1 || response["closed_count"] != float64(1) {
		t.Fatalf("refresh response = %#v", response)
	}
	if manager.ActiveCount() != 0 {
		t.Fatalf("active kernels after refresh = %d", manager.ActiveCount())
	}
}
