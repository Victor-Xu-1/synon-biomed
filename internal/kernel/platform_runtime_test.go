package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedRuntimePlatformMatrix(t *testing.T) {
	tests := []struct {
		goos, goarch, id, subdir, pythonLib string
		windows                             bool
	}{
		{goos: "linux", goarch: "amd64", id: "linux-x86_64", subdir: "linux-64", pythonLib: "lib"},
		{goos: "windows", goarch: "amd64", id: "windows-x86_64", subdir: "win-64", pythonLib: "Lib", windows: true},
		{goos: "darwin", goarch: "amd64", id: "darwin-x86_64", subdir: "osx-64", pythonLib: "lib"},
		{goos: "darwin", goarch: "arm64", id: "darwin-arm64", subdir: "osx-arm64", pythonLib: "lib"},
	}
	for _, test := range tests {
		platform, ok := managedRuntimePlatformFor(test.goos, test.goarch)
		if !ok || platform.ID != test.id || platform.CondaSubdir != test.subdir || platform.PythonLibDir != test.pythonLib || platform.Windows != test.windows {
			t.Fatalf("platform %s/%s = %#v ok=%t", test.goos, test.goarch, platform, ok)
		}
	}
	if _, ok := managedRuntimePlatformFor("freebsd", "amd64"); ok {
		t.Fatal("unsupported platform was accepted")
	}
}

func TestWindowsManagedInstallerCarriesNativeProcessHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows process environment")
	}
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	manager := NewManager(Config{CondaHome: condaHome, Micromamba: filepath.Join(root, "micromamba.exe")})
	environment, err := manager.managedEnvironmentInstallerEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(environment, "USERPROFILE"); got != condaHome {
		t.Fatalf("installer USERPROFILE=%q want=%q", got, condaHome)
	}
	if got, want := environmentValue(environment, "CONDA_PKGS_DIRS"), filepath.Join(root, "p"); got != want {
		t.Fatalf("installer package cache=%q want=%q", got, want)
	}
	for _, name := range []string{"SYSTEMROOT", "COMSPEC", "PATHEXT", "PROCESSOR_ARCHITECTURE"} {
		if environmentValue(environment, name) == "" {
			t.Fatalf("Windows installer omitted %s", name)
		}
	}
}

func TestWindowsManagedInstallerAttemptsLaunchWithLongUserStatePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows installer process")
	}
	long := NewManager(Config{CondaHome: filepath.Join(`C:\`, strings.Repeat("long", 16), "conda"), Micromamba: `C:\missing\micromamba.exe`})
	err := long.runManagedEnvironmentCommand(context.Background(), "--no-rc", "create")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installer launch should fail only because the executable is missing: %v", err)
	}
}

func TestManagedRuntimeInstallLockSerializesAndCancelsWaiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".install.lock")
	release, err := lockKernelFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if _, err := lockKernelFile(waitCtx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock result=%v", err)
	}
	release()
	release = nil
	secondRelease, err := lockKernelFile(context.Background(), path)
	if err != nil {
		t.Fatalf("lock was not reusable after release: %v", err)
	}
	secondRelease()
}

func TestManagedRuntimePointerActivationReplacesAndResolvesAtomically(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "envs", "synon-biomed-python")
	generation := filepath.Join(root, "envs", ".generations", "synon-biomed-python", "generation-a")
	if err := os.MkdirAll(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedRuntimeGeneration(active, generation); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(active)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("activation pointer info=%v err=%v", info, err)
	}
	resolved, err := resolveManagedRuntimeGeneration(active)
	if err != nil || resolved != generation {
		t.Fatalf("resolved=%q err=%v want=%q", resolved, err, generation)
	}
	second := filepath.Join(root, "envs", ".generations", "synon-biomed-python", "generation-b")
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedRuntimeGeneration(active, second); err != nil {
		t.Fatal(err)
	}
	resolved, err = resolveManagedRuntimeGeneration(active)
	if err != nil || resolved != second {
		t.Fatalf("replaced resolved=%q err=%v want=%q", resolved, err, second)
	}
	if _, err := os.Stat(filepath.Join(root, "envs", ".generations", "synon-biomed-python", "generation-a")); err != nil {
		t.Fatal("activation replaced the immutable generation")
	}
}

func TestManagedRuntimePointerRejectsEscapingGeneration(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "envs", "synon-biomed-python")
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active, []byte(`{"schemaVersion":1,"generation":"../../outside"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveManagedRuntimeGeneration(active); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escaping pointer accepted: %v", err)
	}
}
