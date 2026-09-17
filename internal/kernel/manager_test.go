package kernel

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestKernelEnvironmentExplicitValuesOverrideInheritedExactlyOnce(t *testing.T) {
	t.Setenv("PATH", "/host/bin")
	t.Setenv("LANG", "host-locale")

	environment := kernelEnvironment(map[string]string{
		"PATH":         "/managed/bin:/host/bin",
		"LANG":         "managed-locale",
		"CONDA_PREFIX": "/managed",
	})
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", item)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %q in %q", key, environment)
		}
		values[key] = value
	}
	if values["PATH"] != "/managed/bin:/host/bin" || values["LANG"] != "managed-locale" ||
		values["CONDA_PREFIX"] != "/managed" || values["PYTHONUNBUFFERED"] != "1" {
		t.Fatalf("managed environment override was not preserved: %#v", values)
	}
}

func TestManagerExecutesVerifiedWorkerAndClosesAll(t *testing.T) {
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
	manager := NewManager(Config{
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

	worker, err := manager.Start("frame-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := worker.Execute(ctx, "value = 6 * 7\nprint(value)", "user")
	if err != nil {
		t.Fatal(err)
	}
	if first.Error != "" || strings.TrimSpace(first.Stdout) != "42" {
		t.Fatalf("first response = %#v", first)
	}
	second, err := worker.Execute(ctx, "print(value + 1)", "user")
	if err != nil {
		t.Fatal(err)
	}
	if second.Error != "" || strings.TrimSpace(second.Stdout) != "43" {
		t.Fatalf("persistent response = %#v", second)
	}
	if manager.ActiveCount() != 1 {
		t.Fatalf("active kernels = %d", manager.ActiveCount())
	}
	closed, err := manager.CloseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if closed != 1 || manager.ActiveCount() != 0 {
		t.Fatalf("closed = %d, active = %d", closed, manager.ActiveCount())
	}
}
