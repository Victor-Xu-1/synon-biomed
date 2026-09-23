package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake installer writes an Rscript that can only run at its installation
// prefix. Conda's real R launchers have the same absolute-prefix constraint.
func managedRPrefixFixture(t *testing.T) Config {
	t.Helper()
	config := bundledManagedRConfig(t)
	config.AssetRoot = filepath.Join(repositoryRootForCondaRuntimeTest(t), "assets", "optional")
	config.WorkerPath = filepath.Join(config.AssetRoot, "kernels", "kernel_worker.py")
	config.RWorkerPath = filepath.Join(config.AssetRoot, "kernels", "kernel_worker.R")
	config.CondaHome = filepath.Join(t.TempDir(), "conda")
	config.CondaEnvsPath = filepath.Join(config.CondaHome, "envs")
	config.Micromamba = filepath.Join(t.TempDir(), "fake-micromamba")
	installer := `#!/bin/sh
set -eu
prefix=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-p" ]; then prefix="$2"; break; fi
  shift
done
test -n "$prefix"
mkdir -p "$prefix/bin"
printf '#!/bin/sh\n[ -d "%s" ] || exit 87\necho SYNON_R_VERSION=4.5.3\n' "$prefix" > "$prefix/bin/Rscript"
chmod 700 "$prefix/bin/Rscript"
`
	if err := os.WriteFile(config.Micromamba, []byte(installer), 0o700); err != nil {
		t.Fatal(err)
	}
	return config
}

func assertManagedRPrefixRuns(t *testing.T, manager *Manager) {
	t.Helper()
	prefix, err := manager.ManagedRActivePrefix()
	if err != nil {
		t.Fatal(err)
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(rscript, "--vanilla", "-e", "1").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "SYNON_R_VERSION=4.5.3" {
		t.Fatalf("Rscript at active prefix failed: output=%q err=%v", output, err)
	}
}

func TestManagedRInstallationKeepsTheExecutablePrefixStable(t *testing.T) {
	manager := NewManager(managedRPrefixFixture(t))
	if err := manager.EnsureManagedREnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertManagedRPrefixRuns(t, manager)
}

func TestManagedRRepairsBrokenActiveGenerationWithoutDeletingItsContents(t *testing.T) {
	manager := NewManager(managedRPrefixFixture(t))
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	generation := manager.managedRGenerationPath(runtime)
	if err := os.MkdirAll(filepath.Join(generation, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "bin", "Rscript"), []byte("#!/bin/sh\nexit 87\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "preserve-this-file"), []byte("unknown contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedRuntimeMarker(filepath.Join(generation, managedRuntimeMarkerName), manager.managedRMarker(runtime)); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(manager.config.CondaEnvsPath, runtime.entry.Name)
	if err := activateManagedRuntimeGeneration(active, generation); err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeReady("r", runtime.entry.Name) {
		t.Fatal("broken Rscript was reported ready")
	}
	if err := manager.EnsureManagedREnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertManagedRPrefixRuns(t, manager)
	quarantined, err := filepath.Glob(filepath.Join(filepath.Dir(generation), ".staging-invalid-*", "preserve-this-file"))
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("unknown generation contents were not preserved: paths=%v err=%v", quarantined, err)
	}
	if contents, err := os.ReadFile(quarantined[0]); err != nil || string(contents) != "unknown contents" {
		t.Fatalf("quarantined contents=%q err=%v", contents, err)
	}
}

func TestManagedRSharedPackageFailureDoesNotActivateGeneration(t *testing.T) {
	config := managedRPrefixFixture(t)
	config.RSharedPackages = []string{"jsonlite"}
	manager := NewManager(config)
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureManagedREnvironment(context.Background()); err == nil || !strings.Contains(err.Error(), "inspect shared R package version") {
		t.Fatalf("shared R preparation returned %v", err)
	}
	active := filepath.Join(manager.config.CondaEnvsPath, runtime.entry.Name)
	if _, err := os.Lstat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed shared R preparation published an active pointer: %v", err)
	}
	if manager.RuntimeReady("r", runtime.entry.Name) {
		t.Fatal("failed shared R preparation was reported ready")
	}
	if _, err := os.Stat(filepath.Join(manager.managedRGenerationPath(runtime), managedRuntimeMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed shared R preparation wrote a ready marker: %v", err)
	}
}

func TestManagedRCancelledHealthProbePreservesActiveGeneration(t *testing.T) {
	manager := NewManager(managedRPrefixFixture(t))
	if err := manager.EnsureManagedREnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	prefix, err := manager.ManagedRActivePrefix()
	if err != nil {
		t.Fatal(err)
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(rscript)
	if err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(t.TempDir(), "health-probe-started")
	stalled := fmt.Sprintf("#!/bin/sh\necho started > %q\nexec sleep 30\n", started)
	if err := os.WriteFile(rscript, []byte(stalled), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- manager.ensureManagedREnvironment(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("R health probe did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled health probe returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled R health probe did not stop")
	}
	if err := os.WriteFile(rscript, original, 0o700); err != nil {
		t.Fatalf("cancelled probe moved the active generation: %v", err)
	}
	assertManagedRPrefixRuns(t, manager)
}

func TestManagedRReadinessRechecksAfterExpiryReplacementAndRestart(t *testing.T) {
	manager := NewManager(managedRPrefixFixture(t))
	if err := manager.EnsureManagedREnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	prefix, err := manager.ManagedRActivePrefix()
	if err != nil {
		t.Fatal(err)
	}
	rscript, err := managedRExecutableAtPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "readiness-probes")
	script := fmt.Sprintf("#!/bin/sh\n[ -d %q ] || exit 87\necho checked >> %q\necho SYNON_R_VERSION=4.5.3\n", prefix, count)
	if err := os.WriteFile(rscript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	probeCount := func() int {
		t.Helper()
		contents, err := os.ReadFile(count)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(contents), "checked\n")
	}
	if !manager.RuntimeReady("r", defaultManagedREnvironment) || !manager.RuntimeReady("r", defaultManagedREnvironment) {
		t.Fatal("valid R generation was not ready")
	}
	if got := probeCount(); got != 1 {
		t.Fatalf("repeated readiness probe launched Rscript %d times, want 1", got)
	}
	manager.managedRReadiness.mu.Lock()
	manager.managedRReadiness.checkedAt = time.Now().Add(-managedRReadinessCacheTTL)
	manager.managedRReadiness.mu.Unlock()
	if !manager.RuntimeReady("r", defaultManagedREnvironment) || probeCount() != 2 {
		t.Fatal("expired readiness evidence was not rechecked")
	}
	restarted := NewManager(manager.config)
	results := make(chan bool, 8)
	for range 8 {
		go func() { results <- restarted.RuntimeReady("r", defaultManagedREnvironment) }()
	}
	for range 8 {
		if !<-results {
			t.Fatal("new manager did not verify the existing R generation")
		}
	}
	if probeCount() != 3 {
		t.Fatal("concurrent readiness checks repeated the cold-start R smoke")
	}
	if err := os.WriteFile(rscript, []byte("#!/bin/sh\nexit 87\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeReady("r", defaultManagedREnvironment) {
		t.Fatal("replaced broken Rscript was accepted from readiness cache")
	}
}

func TestManagedRCancelledInstallNeverActivatesPartialGeneration(t *testing.T) {
	config := managedRPrefixFixture(t)
	started := filepath.Join(t.TempDir(), "started")
	installer := fmt.Sprintf(`#!/bin/sh
set -eu
prefix=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-p" ]; then prefix="$2"; break; fi
  shift
done
test -n "$prefix"
mkdir -p "$prefix/bin"
echo partial > "$prefix/unfinished"
echo started > %q
exec sleep 30
`, started)
	if err := os.WriteFile(config.Micromamba, []byte(installer), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(config)
	runtime, err := loadManagedRRuntime(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- manager.ProvisionManagedREnvironment(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("installer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled provisioner returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled provisioner did not return")
	}
	manager.managedRState.mu.Lock()
	provision := manager.managedRState.provision
	manager.managedRState.mu.Unlock()
	if provision == nil {
		t.Fatal("installer completion record is missing")
	}
	select {
	case <-provision.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled installer did not exit")
	}
	active := filepath.Join(manager.config.CondaEnvsPath, runtime.entry.Name)
	if _, err := os.Lstat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled installation published an active pointer: %v", err)
	}
	if manager.RuntimeReady("r", runtime.entry.Name) {
		t.Fatal("cancelled installation was reported ready")
	}
	generation := manager.managedRGenerationPath(runtime)
	if _, err := os.Stat(filepath.Join(generation, "unfinished")); err != nil {
		t.Fatalf("partial generation was not retained for safe recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(generation, managedRuntimeMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled generation has a ready marker: %v", err)
	}
}
