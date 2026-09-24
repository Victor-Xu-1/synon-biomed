package kernel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// This opt-in check uses the bundled micromamba and real pip in disposable
// state. It exercises immutable clone, pip replay, and active generation reuse.
func TestManagedPipReplayWithRealInstallers(t *testing.T) {
	if os.Getenv("SYNON_TEST_REAL_PIP_REPLAY") != "1" || runtime.GOOS != "linux" {
		t.Skip("opt-in Linux package installer integration requiring public package access")
	}
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(file), "..", "..")
	root := t.TempDir()
	manager := NewManager(Config{
		Micromamba: filepath.Join(repo, "assets/optional/micromamba/linux-x86_64/micromamba"),
		CondaHome:  filepath.Join(root, "conda"), CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})
	startManagedEnvironmentSupervisor(t, manager)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	created, err := manager.CreateManagedEnvironment(ctx, CreateManagedEnvironmentInput{
		Name: "pip-replay-real", Language: "python", PythonVersion: "3.11",
		Channels: []string{"conda-forge"}, PipPhases: [][]string{{"packaging==24.2"}},
		ImportNames: []string{"packaging"},
	})
	if err != nil || created.Status != "ready" {
		t.Fatalf("real create: environment=%#v err=%v", created, err)
	}
	for _, step := range []struct{ packageName, operationID string }{
		{"pygments==2.19.2", "real-add-pygments"},
		{"idna==3.10", "real-add-idna"},
	} {
		result, err := manager.InstallManagedPackages(ctx, MutateManagedPackagesInput{
			Environment: "pip-replay-real", Packages: []string{step.packageName},
			UsePip: true, OperationID: step.operationID,
		})
		if err != nil || result.Status != "ready" {
			t.Fatalf("real mutation %s: environment=%#v err=%v", step.packageName, result, err)
		}
	}
	marker, err := manager.activeManagedEnvironmentMarker("pip-replay-real")
	if err != nil || marker.PipReplayRevision != 1 || len(marker.PipReplay) != 3 {
		t.Fatalf("real replay receipt: marker=%#v err=%v", marker, err)
	}
	if err := manager.VerifyManagedEnvironmentImports(ctx, "pip-replay-real", []string{"packaging", "pygments", "idna"}); err != nil {
		t.Fatalf("real interpreter could not use restored packages: %v", err)
	}
}
