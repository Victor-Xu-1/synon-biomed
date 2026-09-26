package kernel

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedLockedRequirementsRequireCanonicalHashPinnedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "requirements.lock")
	content := []byte("fixture==1.0 --hash=sha256:" + strings.Repeat("a", 64) + "\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	resolved, gotDigest, err := validateManagedLockedRequirements(path, digest)
	if err != nil || resolved != path || gotDigest != digest {
		t.Fatalf("locked requirements path=%q digest=%q err=%v", resolved, gotDigest, err)
	}
	if _, _, err := validateManagedLockedRequirements(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("locked requirements accepted a mismatched digest")
	}
	link := filepath.Join(root, "requirements-link.lock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateManagedLockedRequirements(link, digest); err == nil {
		t.Fatal("locked requirements accepted a symlink")
	}
}

func TestManagedEnvironmentInstallsLockedRequirementsWithHashEnforcement(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	requirementsPath := filepath.Join(root, "requirements.lock")
	requirements := []byte("fixture==1.0 --hash=sha256:" + strings.Repeat("a", 64) + "\n")
	if err := os.WriteFile(requirementsPath, requirements, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(requirements))
	pipLog := filepath.Join(root, "pip-argv.log")
	micromamba := filepath.Join(root, "micromamba")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
case "$*" in
  *"-m pip install"*) printf '%%s\n' "$*" >%q ;;
  *"SYNON_IMPORT_WITNESS_OK"*) printf 'SYNON_IMPORT_WITNESS_OK\n' ;;
  *) printf '%%s\n' '{"ok":true,"version":[3,11,9]}' ;;
esac
PY
    /bin/chmod 755 "$prefix/bin/python"
    printf '%%s\n' '{"packages":[{"name":"python","version":"3.11.9","build_string":"h1","channel":"conda-forge"},{"name":"pip","version":"25.0","build_string":"pyh","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`, pipLog)
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	environment, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "provider-locked", Language: "python", Packages: []string{"python=3.11", "pip"},
		ImportNames: []string{"fixture"}, LockedRequirementsPath: requirementsPath,
		LockedRequirementsSHA256: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if environment.Status != "ready" || environment.SpecDigest == "" {
		t.Fatalf("locked environment=%#v", environment)
	}
	logged, err := os.ReadFile(pipLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "--require-hashes -r "+requirementsPath) {
		t.Fatalf("locked pip argv=%q", logged)
	}
}

func startManagedEnvironmentSupervisor(t *testing.T, manager *Manager) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.RunManagedEnvironmentSupervisor(ctx) }()
	select {
	case <-manager.managedEnvironmentSupervisorReady:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("managed environment supervisor did not start")
	}
	if !manager.ManagedEnvironmentSupervisorReady() {
		cancel()
		t.Fatal("managed environment supervisor is not ready after startup")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("managed environment supervisor: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("managed environment supervisor did not stop")
		}
	})
	return cancel
}

func TestManagedEnvironmentSupervisorReadinessRequiresRunningServiceAuthority(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{
		Micromamba: filepath.Join(root, "micromamba"), CondaHome: filepath.Join(root, "conda"),
		CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})
	if !manager.ManagedEnvironmentSupervisorEnabled() || manager.ManagedEnvironmentSupervisorReady() {
		t.Fatalf("before start enabled=%t ready=%t", manager.ManagedEnvironmentSupervisorEnabled(), manager.ManagedEnvironmentSupervisorReady())
	}
	cancel := startManagedEnvironmentSupervisor(t, manager)
	cancel()
	deadline := time.Now().Add(time.Second)
	for manager.ManagedEnvironmentSupervisorReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.ManagedEnvironmentSupervisorReady() {
		t.Fatal("managed environment supervisor remained ready after service cancellation")
	}
}

func TestManagedEnvironmentStagingMaintenanceRemovesOnlyOldIncompletePrefixes(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, "envs")
	generationRoot := filepath.Join(envs, ".generations", "chemistry")
	oldStaging := filepath.Join(generationRoot, ".staging-old")
	freshStaging := filepath.Join(generationRoot, ".staging-fresh")
	markedStaging := filepath.Join(generationRoot, ".staging-marked")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{oldStaging, freshStaging, markedStaging, outside} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldStaging, "partial"), []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markedStaging, managedEnvironmentMarkerName), []byte("reserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(generationRoot, ".staging-link")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	for _, path := range []string{oldStaging, markedStaging} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(Config{CondaEnvsPath: envs})
	report, err := manager.sweepManagedEnvironmentStaging(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedStaging != 1 || report.RetainedStaging != 3 {
		t.Fatalf("maintenance report=%#v want one removal and three retained candidates", report)
	}
	if _, err := os.Lstat(oldStaging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old incomplete staging was not removed: %v", err)
	}
	for _, path := range []string{freshStaging, markedStaging, filepath.Join(generationRoot, ".staging-link"), outside} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("maintenance removed retained path %s: %v", filepath.Base(path), err)
		}
	}
}

func TestManagedEnvironmentFastInventorySkipsPerEnvironmentHealthProbe(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, "envs")
	name := "swr-inventory"
	packages := []string{"python=3.11.13=conda-forge"}
	generation := managedEnvironmentGeneration(name, "python", packages, "")
	generationPath := filepath.Join(envs, ".generations", name, generation)
	if err := os.MkdirAll(generationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := managedEnvironmentMarker{
		SchemaVersion:      managedEnvironmentMarkerVersion,
		ValidationRevision: managedEnvironmentValidationRevision,
		Name:               name,
		Language:           "python",
		Generation:         generation,
		Packages:           packages,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		Operation:          "test",
		Kind:               "conda",
	}
	if err := writeManagedEnvironmentMarker(filepath.Join(generationPath, managedEnvironmentMarkerName), marker); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(generationPath, filepath.Join(envs, name)); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(Config{CondaEnvsPath: envs})
	if environment, err := manager.readManagedEnvironment(name, true); err != nil {
		t.Fatalf("read inventory marker: %v", err)
	} else if environment.Generation != generation {
		t.Fatalf("read inventory generation=%q want=%q", environment.Generation, generation)
	}
	listed, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{
		Language: "python", Dependencies: []string{"python"}, IncludePackages: true, SkipHealth: true,
	})
	if err != nil || len(listed) != 1 || listed[0].Name != name || listed[0].Generation != generation {
		t.Fatalf("fast inventory=%#v err=%v", listed, err)
	}
	filteredWithoutPackages, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{
		Language: "python", Dependencies: []string{"python"}, SkipHealth: true,
	})
	if err != nil || len(filteredWithoutPackages) != 1 || filteredWithoutPackages[0].Name != name ||
		len(filteredWithoutPackages[0].Packages) != 0 {
		t.Fatalf("dependency-only projection=%#v err=%v", filteredWithoutPackages, err)
	}
	targeted, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{
		Name: name, Language: "python", Dependencies: []string{"python"}, IncludePackages: true, SkipHealth: true,
	})
	if err != nil || len(targeted) != 1 || targeted[0].Name != name || targeted[0].Generation != generation {
		t.Fatalf("targeted inventory=%#v err=%v", targeted, err)
	}
	if _, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{Name: "../outside"}); err == nil {
		t.Fatal("managed environment query accepted a path-like name")
	}

	strict, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{
		Language: "python", Dependencies: []string{"python"}, IncludePackages: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(strict) != 0 {
		t.Fatalf("strict inventory accepted an environment without a healthy runtime: %#v", strict)
	}
}

func TestManagedInstallerCacheMaintenanceIsLockedAndRemovesOnlyOldExactTemps(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	cache := filepath.Join(condaHome, "pkgs", "cache")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTemp := filepath.Join(cache, "mambafabcdefghij")
	freshTemp := filepath.Join(cache, "mambaf1234567890")
	malformed := filepath.Join(cache, "mambaf-too-long")
	outside := filepath.Join(root, "outside")
	for path, value := range map[string]string{oldTemp: "old scratch", freshTemp: "fresh scratch", malformed: "package metadata", outside: "outside"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(cache, "mambaflink000000")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(cache, "mambafdir0000000")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(oldTemp, old, old); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{CondaHome: condaHome, CondaEnvsPath: envs})
	release, err := lockKernelFile(context.Background(), filepath.Join(envs, ".installer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lockedReport, err := manager.sweepManagedInstallerCacheTemps(context.Background(), now, time.Hour)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if lockedReport.RemovedCacheTemps != 0 || lockedReport.RetainedCacheTemps != 4 {
		t.Fatalf("locked cache maintenance report=%#v", lockedReport)
	}
	report, err := manager.sweepManagedInstallerCacheTemps(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedCacheTemps != 1 || report.RemovedCacheBytes != int64(len("old scratch")) || report.RetainedCacheTemps != 3 {
		t.Fatalf("cache maintenance report=%#v", report)
	}
	if _, err := os.Lstat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old cache temp was not removed: %v", err)
	}
	for _, path := range []string{freshTemp, malformed, link, directory, outside} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("cache maintenance removed retained path %s: %v", filepath.Base(path), err)
		}
	}
}

func TestManagedEnvironmentOperationFailsFastWhenSupervisorDisabled(t *testing.T) {
	manager := NewManager(Config{})
	called := false
	_, err := manager.runManagedEnvironmentOperation(context.Background(), "disabled", func(context.Context) (ManagedEnvironment, error) {
		called = true
		return ManagedEnvironment{}, nil
	})
	if err == nil || err.Error() != "managed environment supervisor is disabled" {
		t.Fatalf("disabled supervisor error = %v", err)
	}
	if called {
		t.Fatal("operation ran while the supervisor was disabled")
	}
}

func TestManagedEnvironmentOperationBoundsSupervisorStartupWait(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{
		Micromamba:    filepath.Join(root, "micromamba"),
		CondaHome:     filepath.Join(root, "conda"),
		CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})
	previous := managedEnvironmentSupervisorStartupTimeout
	managedEnvironmentSupervisorStartupTimeout = 5 * time.Millisecond
	t.Cleanup(func() { managedEnvironmentSupervisorStartupTimeout = previous })
	started := time.Now()
	_, err := manager.runManagedEnvironmentOperation(context.Background(), "not-ready", func(context.Context) (ManagedEnvironment, error) {
		return ManagedEnvironment{}, nil
	})
	if err == nil || err.Error() != "managed environment supervisor did not become ready" {
		t.Fatalf("not-ready supervisor error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("not-ready supervisor waited too long: %s", elapsed)
	}
}

func TestManagedEnvironmentCreateListDeleteUsesImmutableGeneration(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
	set -eu
	prefix=""
	mode=""
	python_spec=""
	while [ "$#" -gt 0 ]; do
	  case "$1" in
	    create|list|install|uninstall) mode="$1" ;;
	    -p) shift; prefix="$1" ;;
	    python=*) python_spec="$1" ;;
	  esac
	  shift
	done
	case "$mode" in
	  create)
	    [ "$python_spec" = "python=3.11" ] || exit 43
	    if [ -e "$prefix" ]; then exit 42; fi
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,11,9]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
	    printf '%s\n' '{"packages":[{"name":"python","version":"3.11.9","build_string":"h1","channel":"conda-forge"},{"name":"_openmp_mutex","version":"4.5","build_string":"20_gnu","channel":"conda-forge"},{"name":"rdkit","version":"2024.03.5","build_string":"py311","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	created, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "chemistry", Language: "python", PythonVersion: "3.11",
		Packages: []string{"python>=3.8", "rdkit=2024.03.5"},
	})
	if err != nil {
		t.Fatalf("create managed environment: %v", err)
	}
	if created.Name != "chemistry" || created.Language != "python" || created.Status != "ready" || !validSHA256(created.Generation) {
		t.Fatalf("unexpected created environment: %#v", created)
	}
	if generation, found, err := manager.ManagedEnvironmentActiveGeneration("chemistry"); err != nil || !found || generation != created.Generation {
		t.Fatalf("active generation = %q found=%t err=%v, want %q", generation, found, err, created.Generation)
	}
	inspected, found, err := manager.InspectManagedEnvironment(context.Background(), "chemistry")
	if err != nil || !found || inspected.Generation != created.Generation || len(inspected.Packages) != 3 {
		t.Fatalf("installed environment preflight=%#v found=%t err=%v", inspected, found, err)
	}
	active := filepath.Join(envs, "chemistry")
	info, err := os.Lstat(active)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("active environment must be a symlink: info=%v err=%v", info, err)
	}
	resolved, err := filepath.EvalSymlinks(active)
	if err != nil || filepath.Base(resolved) != created.Generation || !strings.Contains(resolved, filepath.Join(".generations", "chemistry")) {
		t.Fatalf("active generation mismatch: resolved=%q err=%v", resolved, err)
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(envs, ".generations", "chemistry", "missing-generation"), active); err != nil {
		t.Fatal(err)
	}
	if _, found, err := manager.InspectManagedEnvironment(context.Background(), "chemistry"); err == nil || !found {
		t.Fatalf("corrupt active installation preflight found=%t err=%v", found, err)
	}
	repaired, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "chemistry", Language: "python", Packages: []string{"rdkit=2024.03.5"}, OperationID: "repair-active-pointer",
	})
	if err != nil {
		t.Fatalf("repair corrupt active installation: %v", err)
	}
	if repaired.Generation != created.Generation {
		t.Fatalf("repaired generation = %q, want healthy immutable generation %q", repaired.Generation, created.Generation)
	}
	if repairedTarget, err := filepath.EvalSymlinks(active); err != nil || repairedTarget != resolved {
		t.Fatalf("repaired active target=%q err=%v want=%q", repairedTarget, err, resolved)
	}
	listed, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{
		Language: "python", Dependencies: []string{"rdkit"}, IncludePackages: true,
	})
	if err != nil || len(listed) != 1 || listed[0].Generation != created.Generation || len(listed[0].Packages) != 3 {
		t.Fatalf("unexpected list result: %#v err=%v", listed, err)
	}
	if err := manager.DeleteManagedEnvironment(context.Background(), DeleteManagedEnvironmentInput{Name: "chemistry", ExpectedGeneration: created.Generation, OperationID: "deactivate-chemistry"}); err != nil {
		t.Fatalf("delete managed environment: %v", err)
	}
	if _, err := os.Lstat(active); !os.IsNotExist(err) {
		t.Fatalf("active pointer remains after delete: %v", err)
	}
	if inspected, found, err := manager.InspectManagedEnvironment(context.Background(), "chemistry"); err != nil || found {
		t.Fatalf("deactivated environment preflight=%#v found=%t err=%v", inspected, found, err)
	}
	if _, err := os.Stat(resolved); err != nil {
		t.Fatalf("immutable generation was deleted: %v", err)
	}
	recreated, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "chemistry", Language: "python", Packages: []string{"rdkit=2024.03.5"}, OperationID: "recreate-after-deactivate",
	})
	if err != nil {
		t.Fatalf("recreate deactivated managed environment: %v", err)
	}
	if recreated.Generation != created.Generation {
		t.Fatalf("recreated generation = %q, want immutable generation %q", recreated.Generation, created.Generation)
	}
	if err := manager.DeleteManagedEnvironment(context.Background(), DeleteManagedEnvironmentInput{Name: "chemistry", ExpectedGeneration: created.Generation, OperationID: "deactivate-chemistry"}); err != nil {
		t.Fatalf("recover prior deactivation: %v", err)
	}
	if _, err := os.Lstat(active); err != nil {
		t.Fatalf("old delete removed the reactivated same generation: %v", err)
	}
	if generation, found, err := manager.ManagedEnvironmentActiveGeneration("chemistry"); err != nil || !found || generation != created.Generation {
		t.Fatalf("reactivated generation = %q found=%t err=%v, want %q", generation, found, err, created.Generation)
	}
}

func TestManagedEnvironmentCreateCanCloneVerifiedBundledRuntime(t *testing.T) {
	root := t.TempDir()
	config := bundledManagedPythonConfig(t)
	config.CondaHome = filepath.Join(root, "conda")
	config.CondaEnvsPath = filepath.Join(config.CondaHome, "envs")
	config.Micromamba = filepath.Join(root, "micromamba")
	config.PythonHelperPath = filepath.Join(repositoryRootForCondaRuntimeTest(t), "assets", "optional", "kernels", "cheminfo_render_helpers.py")
	manager := NewManager(config)
	runtimeSpec, err := loadManagedPythonRuntime(config)
	if err != nil {
		t.Fatalf("load bundled runtime: %v", err)
	}
	generationPath := manager.managedPythonGenerationPath(runtimeSpec)
	if err := os.MkdirAll(filepath.Join(generationPath, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(generationPath, "bin", "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"version\":[3,11,15]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.installManagedPythonHelpers(runtimeSpec, generationPath); err != nil {
		t.Fatalf("install bundled runtime helpers: %v", err)
	}
	if err := writeManagedRuntimeMarker(filepath.Join(generationPath, managedRuntimeMarkerName), manager.managedPythonMarker(runtimeSpec)); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(config.CondaEnvsPath, runtimeSpec.entry.Name)
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(generationPath, active); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Micromamba, []byte(`#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|install|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,11,15]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
    ;;
  install) ;;
  list)
    printf '%s\n' '{"packages":[{"name":"python","version":"3.11.15","build_string":"h1","channel":"conda-forge"},{"name":"_openmp_mutex","version":"4.5","build_string":"20_gnu","channel":"conda-forge"},{"name":"scanpy","version":"1.10.4","build_string":"py311","channel":"conda-forge"},{"name":"biopython","version":"1.85","build_string":"py311","channel":"conda-forge"}]}'
    ;;
  *) exit 31 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	startManagedEnvironmentSupervisor(t, manager)
	derived, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "scanpy-derived", Language: "python", SourceEnvironment: runtimeSpec.entry.Name,
		Packages: []string{"scanpy"}, OperationID: "derive-bundled-1",
	})
	if err != nil {
		t.Fatalf("clone bundled runtime: %v", err)
	}
	if derived.Kind != "conda" || derived.Status != "ready" || !managedDependenciesSatisfied(derived.Packages, []string{"scanpy"}) {
		t.Fatalf("derived environment = %#v", derived)
	}
	if _, err := manager.ManagedPythonActivePrefix(); err != nil {
		t.Fatalf("bundled runtime was not preserved: %v", err)
	}
	forked, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: runtimeSpec.entry.Name, Packages: []string{"biopython"}, OperationID: "install-bundled-biopython",
	})
	if err != nil {
		t.Fatalf("derive package generation from bundled runtime: %v", err)
	}
	if forked.Name == runtimeSpec.entry.Name || !strings.HasPrefix(forked.Name, "synon-pkg-") ||
		!managedDependenciesSatisfied(forked.Packages, []string{"biopython"}) {
		t.Fatalf("bundled package fork = %#v", forked)
	}
	replayed, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: runtimeSpec.entry.Name, Packages: []string{"biopython"}, OperationID: "install-bundled-biopython",
	})
	if err != nil || replayed.Name != forked.Name || replayed.Generation != forked.Generation {
		t.Fatalf("bundled package fork replay=%#v err=%v want=%#v", replayed, err, forked)
	}
	if _, err := manager.UninstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: runtimeSpec.entry.Name, Packages: []string{"rdkit"}, OperationID: "remove-bundled-rdkit",
	}); err == nil || !strings.Contains(err.Error(), "additive-only") {
		t.Fatalf("bundled baseline uninstall error=%v", err)
	}
	if _, err := manager.ManagedPythonActivePrefix(); err != nil {
		t.Fatalf("bundled runtime changed after package fork: %v", err)
	}
}

func TestManagedEnvironmentRejectsInjectionBeforeInstaller(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "installer-ran")
	micromamba := filepath.Join(root, "micromamba")
	if err := os.WriteFile(micromamba, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: root, CondaEnvsPath: filepath.Join(root, "envs")})
	_, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "unsafe", Language: "python", Packages: []string{"rdkit; touch /tmp/owned"},
	})
	if err == nil || err.Error() != `package specification at index 0 is invalid: "rdkit; touch /tmp/owned"` {
		t.Fatalf("expected bounded validation error, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("installer ran for rejected input: %v", err)
	}
	_, err = manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "unsafe", Language: "python", Channels: []string{"http://attacker.test/channel"},
	})
	if err == nil || err.Error() != "channel name is invalid" {
		t.Fatalf("expected channel validation error, got %v", err)
	}
}

func TestManagedEnvironmentNormalizesChannelPrefixPackages(t *testing.T) {
	packages, channels, err := normalizeManagedPackageInput(
		[]string{"conda-forge::vina=1.2.5", "bioconda::salmon", "rdkit"},
		[]string{"pytorch"},
	)
	if err != nil {
		t.Fatalf("normalize channel-prefixed packages: %v", err)
	}
	if strings.Join(packages, ",") != "conda-forge::vina=1.2.5,bioconda::salmon,rdkit" {
		t.Fatalf("normalized packages = %#v", packages)
	}
	if strings.Join(channels, ",") != "pytorch,conda-forge,bioconda" {
		t.Fatalf("normalized channels = %#v", channels)
	}
}

func TestManagedEnvironmentRejectsMixedDefaultsChannel(t *testing.T) {
	channels, err := validateManagedChannels([]string{"defaults"})
	if err != nil || strings.Join(channels, ",") != "defaults" {
		t.Fatalf("standalone defaults channel = %#v, %v", channels, err)
	}
	for _, input := range [][]string{
		{"conda-forge", "defaults"},
		{"defaults", "pytorch"},
		{"bioconda", "defaults"},
	} {
		if _, err := validateManagedChannels(input); err == nil || err.Error() != "defaults cannot be mixed with community conda channels" {
			t.Fatalf("mixed channels %#v returned %v", input, err)
		}
	}
}

func TestManagedEnvironmentNormalizesPipPrefixPackagesIntoFirstPhase(t *testing.T) {
	packages, channels, phases, err := normalizeManagedEnvironmentCreateInput(
		[]string{"conda-forge::rdkit", "pip::meeko", "pip::vina", "bioconda::pysam"},
		[]string{"pytorch"},
		[][]string{{"pypdb"}},
	)
	if err != nil {
		t.Fatalf("normalize pip-prefixed packages: %v", err)
	}
	if strings.Join(packages, ",") != "conda-forge::rdkit,bioconda::pysam" {
		t.Fatalf("normalized conda packages = %#v", packages)
	}
	if strings.Join(channels, ",") != "pytorch,conda-forge,bioconda" {
		t.Fatalf("normalized channels = %#v", channels)
	}
	if len(phases) != 2 || strings.Join(phases[0], ",") != "meeko,vina,gemmi" || strings.Join(phases[1], ",") != "pypdb" {
		t.Fatalf("normalized pip phases = %#v", phases)
	}
}

func TestManagedEnvironmentAcceptsCredentialFreeHTTPSPipSources(t *testing.T) {
	packages, _, phases, err := normalizeManagedEnvironmentCreateInput(
		[]string{
			"python=3.10",
			"pip::git+https://github.com/example/scientific-engine.git@v1.2.3",
			"pip::scientific-helper @ https://example.org/releases/helper-1.0.tar.gz",
		}, nil, nil,
	)
	if err != nil {
		t.Fatalf("normalize HTTPS pip sources: %v", err)
	}
	if strings.Join(packages, ",") != "python=3.10" || len(phases) != 1 ||
		strings.Join(phases[0], "|") != "git+https://github.com/example/scientific-engine.git@v1.2.3|scientific-helper @ https://example.org/releases/helper-1.0.tar.gz" {
		t.Fatalf("packages=%#v phases=%#v", packages, phases)
	}
	if got := managedPipDistributionKey(phases[0][0]); got != "scientific_engine" {
		t.Fatalf("VCS distribution key=%q", got)
	}
	if got := managedPipDistributionKey(phases[0][1]); got != "scientific_helper" {
		t.Fatalf("named direct-reference key=%q", got)
	}
	for _, unsafe := range []string{
		"git+ssh://github.com/example/private.git",
		"git+https://token@github.com/example/private.git",
		"file:///tmp/local-project",
		"--extra-index-url=https://example.org/simple",
	} {
		if _, err := validateManagedPipRequirementSpecs([]string{unsafe}); err == nil {
			t.Fatalf("unsafe pip source %q was accepted", unsafe)
		}
	}
}

func TestManagedEnvironmentPreservesExplicitPackageManagerAuthority(t *testing.T) {
	packages, channels, phases, err := normalizeManagedEnvironmentCreateInput(
		[]string{"conda-forge::native-lib", "pip::python-wrapper", "pip::native-lib"},
		nil,
		[][]string{{"pandas"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(packages, ",") != "conda-forge::native-lib" {
		t.Fatalf("conda packages = %#v", packages)
	}
	if strings.Join(channels, ",") != "conda-forge" {
		t.Fatalf("channels = %#v", channels)
	}
	if len(phases) != 2 || strings.Join(phases[0], ",") != "python-wrapper,native-lib" || strings.Join(phases[1], ",") != "pandas" {
		t.Fatalf("pip phases = %#v", phases)
	}
}

func TestManagedEnvironmentMeekoRuntimeClosurePreservesExplicitGemmiPin(t *testing.T) {
	_, _, phases, err := normalizeManagedEnvironmentCreateInput(
		nil, nil, [][]string{{"meeko==0.7.1", "gemmi==0.7.5", "vina==1.2.7"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || strings.Join(phases[0], ",") != "meeko==0.7.1,gemmi==0.7.5,vina==1.2.7" {
		t.Fatalf("explicit runtime closure = %#v", phases)
	}
}

func TestManagedEnvironmentSafeMolRuntimeClosurePinsTransformersBeforeActivation(t *testing.T) {
	_, _, phases, err := normalizeManagedEnvironmentCreateInput(
		nil, nil, [][]string{{"safe-mol>=0.1.14", "requests", "rdkit"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || strings.Join(phases[0], ",") != "safe-mol>=0.1.14,requests,rdkit,transformers<5" {
		t.Fatalf("safe-mol compatibility closure = %#v", phases)
	}

	_, _, phases, err = normalizeManagedEnvironmentCreateInput(
		nil, nil, [][]string{{"safe-mol==0.1.14"}, {"transformers==5.15.1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || strings.Join(phases[0], ",") != "safe-mol==0.1.14,transformers<5" ||
		strings.Join(phases[1], ",") != "transformers==5.15.1,transformers<5" {
		t.Fatalf("explicit incompatible transformers request escaped the compatibility fence: %#v", phases)
	}
}

func TestManagedPipRequirementsCarryForwardAcrossImmutableGenerations(t *testing.T) {
	installed := []string{
		"gemmi=0.7.5=pypi_0=pypi",
		"meeko=0.7.1=pypi_0=pypi",
		"numpy=2.4.6=py311=conda-forge",
		"pip=26.1.2=pyh8b19718_0=conda-forge",
		"vina=1.2.7=pypi_0=pypi",
	}
	requirements, err := managedPipRequirementsFromInventory(installed, []string{"gemmi==0.7.6", "new-package"})
	if err != nil {
		t.Fatalf("restore pip requirements: %v", err)
	}
	if got := strings.Join(requirements, ","); got != "meeko==0.7.1,vina==1.2.7" {
		t.Fatalf("restored pip requirements = %q", got)
	}

	// PEP 503-equivalent spelling must replace the existing distribution
	// rather than installing a duplicate pinned requirement.
	requirements, err = managedPipRequirementsFromInventory(
		[]string{"python_docx=1.2.0=pypi_0=pypi", "vina=1.2.7=pypi_0=pypi"},
		[]string{"python-docx==1.2.1"},
	)
	if err != nil || strings.Join(requirements, ",") != "vina==1.2.7" {
		t.Fatalf("normalized replacement requirements = %#v err=%v", requirements, err)
	}
}

func TestManagedPackageInstallRejectsChangesToExistingResolution(t *testing.T) {
	existing := []string{
		"numpy=2.2.0=py311=conda-forge",
		"scanpy=1.11.5=pypi_0=pypi",
	}
	if err := validateAdditiveManagedPackageResolution(existing, append(append([]string{}, existing...), "anndata=0.12.3=pypi_0=pypi"), nil); err != nil {
		t.Fatalf("additive package resolution rejected: %v", err)
	}
	changed := []string{
		"numpy=2.3.0=py311=conda-forge",
		"scanpy=1.11.5=pypi_0=pypi",
		"anndata=0.12.3=pypi_0=pypi",
	}
	if err := validateAdditiveManagedPackageResolution(existing, changed, nil); err == nil || !strings.Contains(err.Error(), "would change numpy") {
		t.Fatalf("non-additive package resolution error=%v", err)
	}
}

func TestManagedPackageInstallAllowsVerifiedGenericAuthorityMigration(t *testing.T) {
	existing := []string{
		"numpy=2.2.0=py311=conda-forge",
		"example-lib=3.2.1=pypi_0=pypi",
	}
	migrations := plannedManagedPackageAuthorityMigrations(existing, []string{"example-lib"}, false)
	if len(migrations) != 1 || migrations[0].PipDistribution != "example-lib" || migrations[0].CondaPackage != "example-lib" {
		t.Fatalf("authority migrations = %#v", migrations)
	}
	resolved := []string{
		"numpy=2.2.0=py311=conda-forge",
		"example-lib=3.1.1=py311=conda-forge",
	}
	if err := validateAdditiveManagedPackageResolution(existing, resolved, migrations); err != nil {
		t.Fatalf("verified authority migration rejected: %v", err)
	}

	withoutCondaReplacement := []string{
		"numpy=2.2.0=py311=conda-forge",
	}
	if err := validateAdditiveManagedPackageResolution(existing, withoutCondaReplacement, migrations); err == nil || !strings.Contains(err.Error(), "did not install conda package example-lib") {
		t.Fatalf("missing conda replacement error=%v", err)
	}
}

func TestManagedPackageInstallDoesNotMigrateAuthorityForPipOrUnrelatedRequests(t *testing.T) {
	existing := []string{"example-lib=3.2.1=pypi_0=pypi"}
	if migrations := plannedManagedPackageAuthorityMigrations(existing, []string{"example-lib"}, true); len(migrations) != 0 {
		t.Fatalf("pip request migrations = %#v", migrations)
	}
	if migrations := plannedManagedPackageAuthorityMigrations(existing, []string{"pandas"}, false); len(migrations) != 0 {
		t.Fatalf("unrelated request migrations = %#v", migrations)
	}
}

func TestManagedEnvironmentPipInstallPreservesPriorDistributionsAcrossGenerations(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list|install|uninstall) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
set -eu
prefix=$(/usr/bin/dirname "$(/usr/bin/dirname "$0")")
case "$*" in
  *"-m pip install"*)
    [ "${CONDA_PREFIX:-}" = "$prefix" ] || exit 72
    case "$PATH" in "$prefix/bin":*) ;; *) exit 73 ;; esac
    for package in "$@"; do
      case "$package" in
        vina|vina==*) /usr/bin/touch "$prefix/.vina" ;;
        meeko|meeko==*) /usr/bin/touch "$prefix/.meeko" ;;
        gemmi|gemmi==*) /usr/bin/touch "$prefix/.gemmi" ;;
      esac
    done
    ;;
  *"-m pip uninstall"*)
    for package in "$@"; do
      case "$package" in
        vina|vina==*) /bin/rm -f "$prefix/.vina" ;;
        meeko|meeko==*) /bin/rm -f "$prefix/.meeko" ;;
        gemmi|gemmi==*) /bin/rm -f "$prefix/.gemmi" ;;
      esac
    done
    ;;
  *) printf '%s\n' '{"ok":true,"version":[3,11,15]}' ;;
esac
PY
    /bin/chmod 755 "$prefix/bin/python"
    ;;
  list)
    printf '%s' '{"packages":[{"name":"python","version":"3.11.15","build_string":"h1","channel":"conda-forge"},{"name":"pip","version":"25.0","build_string":"h1","channel":"conda-forge"}'
    [ ! -f "$prefix/.vina" ] || printf '%s' ',{"name":"vina","version":"1.2.7","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.meeko" ] || printf '%s' ',{"name":"meeko","version":"0.7.1","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.gemmi" ] || printf '%s' ',{"name":"gemmi","version":"0.7.5","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.requests" ] || printf '%s' ',{"name":"requests","version":"2.32.5","build_string":"pyh1","channel":"conda-forge"}'
    printf '%s\n' ']}'
    ;;
  install)
    /usr/bin/touch "$prefix/.requests"
    ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	created, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "docking", Language: "python", PipPhases: [][]string{{"vina", "meeko"}},
	})
	if err != nil {
		t.Fatalf("create pip environment: %v", err)
	}
	if !managedDependenciesSatisfied(created.Packages, []string{"vina", "meeko"}) {
		t.Fatalf("initial pip packages = %#v", created.Packages)
	}

	// The test installer intentionally models micromamba clone semantics that
	// omit pip-only files. The manager must reconstruct the verified source
	// inventory before adding the next package to the replacement generation.
	mutated, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: "docking", Packages: []string{"gemmi"}, UsePip: true, OperationID: "install-gemmi",
	})
	if err != nil {
		t.Fatalf("install gemmi generation: %v", err)
	}
	if mutated.Generation == created.Generation ||
		!managedDependenciesSatisfied(mutated.Packages, []string{"vina", "meeko", "gemmi"}) {
		t.Fatalf("mutated pip packages = %#v initial=%s", mutated, created.Generation)
	}
	listed, err := manager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{IncludePackages: true})
	if err != nil || len(listed) != 1 || listed[0].Generation != mutated.Generation ||
		!managedDependenciesSatisfied(listed[0].Packages, []string{"vina", "meeko", "gemmi"}) {
		t.Fatalf("active pip generation = %#v err=%v", listed, err)
	}

	// A later conda mutation must preserve the same pip inventory as well.
	// This models the real clone behavior that dropped harmonypy while adding
	// unrelated download helpers.
	condaMutated, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: "docking", Packages: []string{"requests"}, OperationID: "install-requests",
	})
	if err != nil {
		t.Fatalf("install conda package generation: %v", err)
	}
	if condaMutated.Generation == mutated.Generation ||
		!managedDependenciesSatisfied(condaMutated.Packages, []string{"vina", "meeko", "gemmi", "requests"}) {
		t.Fatalf("conda mutation dropped pip inventory: %#v prior=%s", condaMutated, mutated.Generation)
	}
}

func TestManagedEnvironmentAcceptsBoundedCommunityChannelAndRejectsURLPrefix(t *testing.T) {
	packages, channels, err := normalizeManagedPackageInput([]string{"pyg::pyg=2.0.4"}, nil)
	if err != nil || !reflect.DeepEqual(packages, []string{"pyg::pyg=2.0.4"}) || !reflect.DeepEqual(channels, []string{"pyg"}) {
		t.Fatalf("community channel result packages=%#v channels=%#v err=%v", packages, channels, err)
	}
	_, _, err = normalizeManagedPackageInput([]string{"https://attacker.invalid::tool"}, nil)
	if err == nil || err.Error() != "channel name is invalid" {
		t.Fatalf("expected bounded-channel error, got %v", err)
	}
}

func TestManagedEnvironmentWaiterCancellationDoesNotCancelSharedInstall(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    printf 'started\n' >>'` + started + `'
    while [ ! -f '` + release + `' ]; do /bin/sleep 0.01; done
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,11,9]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
    printf '%s\n' '{"packages":[{"name":"python","version":"3.11.9","build_string":"h1","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	request := CreateManagedEnvironmentInput{Name: "shared", Language: "python"}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateManagedEnvironment(firstCtx, request)
		firstDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("installer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateManagedEnvironment(context.Background(), request)
		secondDone <- err
	}()
	deadline = time.Now().Add(2 * time.Second)
	for {
		manager.managedEnvironmentMu.Lock()
		operationWaiters := 0
		for _, operation := range manager.managedEnvironmentOperations {
			operationWaiters = operation.waiters
			break
		}
		manager.managedEnvironmentMu.Unlock()
		if operationWaiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second waiter did not join shared install: waiters=%d", operationWaiters)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter error = %v, want context canceled", err)
	}
	if err := os.WriteFile(release, []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("shared installer did not finish for second waiter: %v", err)
	}
	raw, err := os.ReadFile(started)
	if err != nil {
		t.Fatal(err)
	}
	// One publication transaction intentionally invokes the installer twice:
	// first to resolve and validate the content identity, then at the immutable
	// final prefix so relocated launchers remain valid. A cancelled waiter must
	// not create a second transaction (which would produce four invocations).
	if count := strings.Count(string(raw), "started\n"); count != 2 {
		t.Fatalf("installer phases = %d, want 2 from one shared publication transaction", count)
	}
}

func TestManagedEnvironmentLastWaiterCancellationCancelsInstall(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{
		Micromamba: filepath.Join(root, "micromamba"), CondaHome: filepath.Join(root, "conda"),
		CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})
	startManagedEnvironmentSupervisor(t, manager)
	started := make(chan struct{})
	installerCancelled := make(chan struct{})
	waitCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(waitCtx, "last-waiter", func(operationContext context.Context) (ManagedEnvironment, error) {
			close(started)
			<-operationContext.Done()
			close(installerCancelled)
			return ManagedEnvironment{}, operationContext.Err()
		})
		waiterDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("installer operation did not start")
	}
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("last waiter error = %v, want context canceled", err)
	}
	select {
	case <-installerCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("installer context survived after its last waiter left")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.managedEnvironmentMu.Lock()
		active := len(manager.managedEnvironmentOperations)
		manager.managedEnvironmentMu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled installer remained active: %d operation(s)", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagedEnvironmentStableOperationRecoversPublishedGenerationWithoutReinstall(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	starts := filepath.Join(root, "starts")
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    printf 'start\n' >>'` + starts + `'
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,13,2]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
    printf '%s\n' '{"packages":[{"name":"python","version":"3.13.2","build_string":"h1","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	request := CreateManagedEnvironmentInput{Name: "recovery", Language: "python", OperationID: "tool-call-stable-1"}
	first := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, first)
	created, err := first.CreateManagedEnvironment(context.Background(), request)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, second)
	recovered, err := second.CreateManagedEnvironment(context.Background(), request)
	if err != nil {
		t.Fatalf("recovered create: %v", err)
	}
	if recovered.Generation != created.Generation {
		t.Fatalf("recovered generation=%q want=%q", recovered.Generation, created.Generation)
	}
	request.OperationID = "tool-call-stable-2"
	reused, err := second.CreateManagedEnvironment(context.Background(), request)
	if err != nil {
		t.Fatalf("cross-task definition reuse: %v", err)
	}
	if reused.Generation != created.Generation {
		t.Fatalf("reused generation=%q want=%q", reused.Generation, created.Generation)
	}
	raw, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "start\n"); count != 2 {
		t.Fatalf("installer starts=%d want=2 (resolution staging plus final-prefix publication)", count)
	}
}

func TestManagedEnvironmentInstallerAdmissionIsHostWide(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	micromamba := filepath.Join(root, "micromamba")
	guard := filepath.Join(root, "installer-active")
	collision := filepath.Join(root, "installer-collision")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    if ! /bin/mkdir '` + guard + `' 2>/dev/null; then
      printf 'overlap\n' >>'` + collision + `'
      exit 97
    fi
    trap '/bin/rmdir "` + guard + `"' EXIT
    /bin/sleep 0.15
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,13,2]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
    printf '%s\n' '{"packages":[{"name":"python","version":"3.13.2","build_string":"h1","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs}
	managers := []*Manager{NewManager(config), NewManager(config)}
	for _, manager := range managers {
		startManagedEnvironmentSupervisor(t, manager)
	}
	start := make(chan struct{})
	results := make(chan error, len(managers))
	for index, manager := range managers {
		name := fmt.Sprintf("parallel-%d", index)
		go func() {
			<-start
			_, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
				Name: name, Language: "python",
			})
			results <- err
		}()
	}
	close(start)
	for range managers {
		if err := <-results; err != nil {
			t.Fatalf("host-wide installer admission: %v", err)
		}
	}
	if raw, err := os.ReadFile(collision); err == nil {
		t.Fatalf("installers overlapped across manager instances: %q", raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestManagedEnvironmentPublishesAgainstFinalPrefix(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' '{"ok":true,"version":[3,11,9]}'
PY
    /bin/chmod 755 "$prefix/bin/python"
    printf '%s\n' "$prefix" >"$prefix/installed-prefix.txt"
    printf '%s\n' '{"packages":[{"name":"python","version":"3.11.9","build_string":"h1","channel":"conda-forge"}]}' >"$prefix/.inventory.json"
    ;;
  list) /bin/cat "$prefix/.inventory.json" ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	created, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "final-prefix", Language: "python", OperationID: "final-prefix-operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	finalPrefix := filepath.Join(envs, ".generations", "final-prefix", created.Generation)
	raw, err := os.ReadFile(filepath.Join(finalPrefix, "installed-prefix.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != finalPrefix || strings.Contains(got, ".staging-") {
		t.Fatalf("installed prefix=%q want final immutable prefix %q", got, finalPrefix)
	}
}

func TestManagedEnvironmentPipArgumentsAreClosed(t *testing.T) {
	allowed, err := validateManagedPipArguments([]string{"--no-cache-dir", "--no-build-isolation", "--upgrade"})
	if err != nil || len(allowed) != 3 || allowed[2] != "--upgrade" {
		t.Fatalf("allowed pip arguments rejected: %#v %v", allowed, err)
	}
	if _, err := validateManagedPipArguments([]string{"--index-url=https://attacker.test"}); err == nil || err.Error() != "pip argument is not allowed" {
		t.Fatalf("untrusted pip argument was accepted: %v", err)
	}
	links, err := validateManagedPipSourceURLs([]string{"https://data.pyg.org/whl/torch-2.4.0+cu121.html"})
	if err != nil || !reflect.DeepEqual(links, []string{"https://data.pyg.org/whl/torch-2.4.0+cu121.html"}) {
		t.Fatalf("public wheel source rejected: %#v %v", links, err)
	}
	for _, unsafe := range []string{"http://data.pyg.org/whl", "https://127.0.0.1/wheels", "https://user:secret@example.org/wheels"} {
		if _, err := validateManagedPipSourceURLs([]string{unsafe}); err == nil {
			t.Fatalf("unsafe wheel source accepted: %q", unsafe)
		}
	}
}

func TestManagedDependencyMatchingPreservesChannelVersionAndAcceleratorContract(t *testing.T) {
	installed := []string{
		"python=3.8.18=hd12c33a_0_cpython=conda-forge",
		"pytorch=1.9.0=cpu_py38h4bbe6ce_2=conda-forge",
		"pytorch-cpu=1.9.0=cpu_py38h718b53a_2=conda-forge",
		"cuda-version=13.3=hcbadf70_3=conda-forge",
		"torch-geometric=1.7.2=pypi_0=pypi",
	}
	if !managedDependenciesSatisfied(installed, []string{"python=3.8", "pytorch=1.9.0"}) {
		t.Fatal("compatible version constraints were rejected")
	}
	if managedDependenciesSatisfied(installed, []string{"pytorch::pytorch=1.9.0"}) {
		t.Fatal("a conda-forge build satisfied a package-scoped pytorch channel request")
	}
	if managedDependenciesSatisfied(installed, []string{"python>=3.9"}) {
		t.Fatal("an incompatible Python version was reused")
	}
	if !managedDependenciesSatisfied(installed, []string{"pip::torch_geometric==1.7.2"}) {
		t.Fatal("pip authority alias or normalized distribution name was not matched")
	}
	if err := validateManagedRequiredAccelerator(installed, "required"); err == nil {
		t.Fatal("an explicitly CPU-only framework stack satisfied a required accelerator")
	}
	accelerated := append(installed, "pytorch-cuda=12.1=ha16c6d3_6=pytorch")
	if err := validateManagedRequiredAccelerator(accelerated, "required"); err != nil {
		t.Fatalf("accelerator-bearing stack rejected: %v", err)
	}
}

func TestManagedCondaUninstallDoesNotReceiveChannelArguments(t *testing.T) {
	arguments := managedCondaMutationArguments("uninstall", "/tmp/example", []string{"conda-forge", "pyg"}, []string{"pytorch"})
	if strings.Contains(strings.Join(arguments, " "), " -c ") {
		t.Fatalf("uninstall arguments contain solver channels: %#v", arguments)
	}
	install := managedCondaMutationArguments("install", "/tmp/example", []string{"conda-forge", "pyg"}, []string{"pyg::pyg"})
	if !strings.Contains(strings.Join(install, " "), " -c pyg ") {
		t.Fatalf("install arguments lost solver channels: %#v", install)
	}
}

func TestManagedEnvironmentInstallerUsesConfiguredProxyWithoutLeakingItToRuntime(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:relative:/usr/bin")
	t.Setenv("HTTPS_PROXY", "http://unselected.example.test:8080")
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("ALL_PROXY", strings.Repeat("x", 4097))
	manager := NewManager(Config{
		UpstreamProxy: "http://proxy.example.test:8080",
		Micromamba:    "/opt/synon/micromamba", CondaHome: "/tmp/synon-conda",
		CondaEnvsPath: "/tmp/synon-conda/envs",
	})
	installerEnv, err := manager.managedEnvironmentInstallerEnv()
	if err != nil {
		t.Fatal(err)
	}
	installer := strings.Join(installerEnv, "\n")
	if !strings.Contains(installer, "HTTPS_PROXY=http://proxy.example.test:8080") ||
		strings.Contains(installer, "NO_PROXY=") || strings.Contains(installer, "ALL_PROXY=") || strings.Contains(installer, "unselected.example") {
		t.Fatalf("installer proxy environment=%q", installer)
	}
	threadLimit := managedEnvironmentInstallerThreadLimit()
	if threadLimit < 1 || threadLimit > 4 {
		t.Fatalf("installer thread limit=%d", threadLimit)
	}
	for _, key := range []string{
		"MAMBA_DOWNLOAD_THREADS", "MAMBA_EXTRACT_THREADS", "CMAKE_BUILD_PARALLEL_LEVEL", "MAX_JOBS",
		"OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS", "NUMEXPR_NUM_THREADS", "RAYON_NUM_THREADS",
	} {
		if !strings.Contains(installer, fmt.Sprintf("%s=%d", key, threadLimit)) {
			t.Fatalf("installer thread budget %s missing from %q", key, installer)
		}
	}
	if !strings.Contains(installer, "KMP_AFFINITY=disabled") || !strings.Contains(installer, "OMP_PROC_BIND=false") {
		t.Fatalf("installer did not disable unsupported CPU affinity: %q", installer)
	}
	runtimeEnvironment := strings.Join(managedEnvironmentRuntimeEnv("/tmp/env"), "\n")
	if strings.Contains(runtimeEnvironment, "HTTPS_PROXY=") || strings.Contains(runtimeEnvironment, "NO_PROXY=") {
		t.Fatalf("runtime inherited installer proxy authority: %q", runtimeEnvironment)
	}
	installerRuntimeEnv, err := manager.managedEnvironmentInstallerRuntimeEnv("/tmp/env")
	if err != nil {
		t.Fatal(err)
	}
	installerRuntime := strings.Join(installerRuntimeEnv, "\n")
	wantBuildPath := "PATH=/tmp/env/bin:/usr/bin:/bin"
	if !strings.Contains(installerRuntime, wantBuildPath) {
		t.Fatalf("installer build PATH=%q want %q", installerRuntime, wantBuildPath)
	}
	if !strings.Contains(runtimeEnvironment, wantBuildPath) {
		t.Fatalf("runtime validation lost required OS utilities: %q", runtimeEnvironment)
	}
	runtimeThreadLimit := managedEnvironmentRuntimeThreadLimit()
	if runtimeThreadLimit < 1 || runtimeThreadLimit > 4 {
		t.Fatalf("runtime thread limit=%d", runtimeThreadLimit)
	}
	for _, key := range []string{
		"OMP_NUM_THREADS", "OMP_THREAD_LIMIT", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS",
		"BLIS_NUM_THREADS", "VECLIB_MAXIMUM_THREADS", "NUMEXPR_NUM_THREADS", "RAYON_NUM_THREADS",
	} {
		if !strings.Contains(runtimeEnvironment, fmt.Sprintf("%s=%d", key, runtimeThreadLimit)) {
			t.Fatalf("runtime thread budget %s missing from %q", key, runtimeEnvironment)
		}
	}
	if !strings.Contains(runtimeEnvironment, "KMP_AFFINITY=disabled") || !strings.Contains(runtimeEnvironment, "OMP_PROC_BIND=false") {
		t.Fatalf("runtime did not disable unsupported CPU affinity: %q", runtimeEnvironment)
	}
}

func TestManagedEnvironmentCreateRunsOrderedPipPhasesAndImportWitness(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	pythonCalls := filepath.Join(root, "python-calls")
	micromamba := filepath.Join(root, "micromamba")
	script := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' "$*" >>'` + pythonCalls + `'
case "$*" in
  *"-m pip install"*) exit 0 ;;
  *"SYNON_IMPORT_WITNESS_OK"*) printf '%s\n' 'SYNON_IMPORT_WITNESS_OK' ;;
  *) printf '%s\n' '{"ok":true,"version":[3,11,9]}' ;;
esac
PY
    /bin/chmod 755 "$prefix/bin/python"
    ;;
  list)
    printf '%s\n' '{"packages":[{"name":"python","version":"3.11.9","build_string":"h1","channel":"conda-forge"},{"name":"torch","version":"2.3.1","build_string":"pip","channel":"pypi"},{"name":"chai-lab","version":"0.6.1","build_string":"pip","channel":"pypi"}]}'
    ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	created, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "ordered", Language: "python", PythonVersion: "3.11",
		PipPhases:         [][]string{{"torch==2.3.1"}, {"chai-lab==0.6.1"}},
		PipFindLinks:      []string{"https://wheels.example.org/torch.html"},
		PipExtraIndexURLs: []string{"https://packages.example.org/simple"},
		ImportNames:       []string{"torch", "chai_lab"},
	})
	if err != nil {
		t.Fatalf("create managed environment: %v", err)
	}
	if !validSHA256(created.SpecDigest) {
		t.Fatalf("spec digest = %q", created.SpecDigest)
	}
	raw, err := os.ReadFile(pythonCalls)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	first := strings.Index(calls, "torch==2.3.1")
	second := strings.Index(calls, "chai-lab==0.6.1")
	witness := strings.Index(calls, "import importlib")
	if first < 0 || second <= first || witness <= second ||
		!strings.Contains(calls, "--find-links https://wheels.example.org/torch.html") ||
		!strings.Contains(calls, "--extra-index-url https://packages.example.org/simple") {
		t.Fatalf("Python calls are not ordered install phases followed by witness:\n%s", calls)
	}
	marker, err := readManagedEnvironmentMarker(filepath.Join(envs, ".generations", "ordered", created.Generation))
	if err != nil || marker.SpecDigest != created.SpecDigest || strings.Join(marker.ImportNames, ",") != "torch,chai_lab" {
		t.Fatalf("marker = %#v err=%v", marker, err)
	}
}

func TestManagedEnvironmentSpecificationValidationFailsClosed(t *testing.T) {
	if _, err := validateManagedPipPhases([][]string{{}}); err == nil || err.Error() != "pip phase must contain at least one package" {
		t.Fatalf("empty phase error = %v", err)
	}
	if _, err := validateManagedPipPhases([][]string{{"torch==2.7.0"}, {"--no-index", "torch_geometric"}}); err == nil ||
		err.Error() != `pip phase 1: pip package specification at index 0 is invalid: "--no-index"` {
		t.Fatalf("invalid phase error = %v", err)
	}
	if _, err := validateManagedImportNames([]string{"os;raise SystemExit()"}); err == nil || err.Error() != "import witness name is invalid" {
		t.Fatalf("unsafe import error = %v", err)
	}
	digest := managedEnvironmentSpecDigest([][]string{{"torch==2.3.1"}}, []string{"torch"})
	legacy := managedEnvironmentGeneration("chemistry", "python", []string{"python=3.11=h1=conda-forge"}, "")
	bound := managedEnvironmentGeneration("chemistry", "python", []string{"python=3.11=h1=conda-forge"}, digest)
	if !validSHA256(digest) || legacy == bound {
		t.Fatalf("digest=%q legacy=%q bound=%q", digest, legacy, bound)
	}
}

func TestManagedEnvironmentImportWitnessIgnoresBoundedThirdPartyStdoutNoise(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(prefix, "bin", "python")
	script := "#!/bin/sh\nprintf '%s\\n' 'third-party import notice'\nprintf '%s\\n' 'SYNON_IMPORT_WITNESS_OK'\n"
	if err := os.WriteFile(python, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedEnvironmentImports(context.Background(), "python", prefix, []string{"meeko"}); err != nil {
		t.Fatalf("bounded import witness noise was rejected: %v", err)
	}
}

func TestManagedEnvironmentBinaryCompatibilityRejectsHDF5ABIMismatch(t *testing.T) {
	prefix := t.TempDir()
	writeManagedEnvironmentHealthPython(t, prefix, "2.1.0", "2.2.0", "")
	err := validateManagedEnvironmentBinaryCompatibility(
		context.Background(), "python", prefix,
		[]string{"python=3.11=h1=conda-forge", "h5py=3.16.0=py311=conda-forge", "hdf5=2.2.0=h1=conda-forge"},
	)
	if err == nil || !strings.Contains(err.Error(), "h5py was built against HDF5 2.1.0 but loaded 2.2.0") ||
		!strings.Contains(err.Error(), "install a compatible h5py/HDF5 combination and retry") {
		t.Fatalf("binary compatibility error = %v", err)
	}
}

func TestManagedEnvironmentHealthAdmissionIsSingleFlightAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, "envs")
	name := "single-cell"
	packages := []string{
		"python=3.11=h1=conda-forge",
		"h5py=3.16.0=py311=conda-forge",
		"hdf5=2.1.0=h1=conda-forge",
	}
	generation := managedEnvironmentGeneration(name, "python", packages, "")
	generationPath := filepath.Join(envs, ".generations", name, generation)
	calls := filepath.Join(root, "health-calls")
	if err := os.MkdirAll(generationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeManagedEnvironmentHealthPython(t, generationPath, "2.1.0", "2.1.0", calls)
	marker := managedEnvironmentMarker{
		SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: "python",
		ValidationRevision: managedEnvironmentValidationRevision,
		Generation:         generation, Packages: packages, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Operation: "create", Kind: "conda",
	}
	if err := writeManagedEnvironmentMarker(filepath.Join(generationPath, managedEnvironmentMarkerName), marker); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(generationPath, filepath.Join(envs, name)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{CondaEnvsPath: envs})
	const concurrentAdmissions = 8
	var wait sync.WaitGroup
	errorsByAdmission := make(chan error, concurrentAdmissions)
	for range concurrentAdmissions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			actual, found, err := manager.ManagedEnvironmentActiveGeneration(name)
			if err == nil && (!found || actual != generation) {
				err = errors.New("managed environment generation was not admitted")
			}
			errorsByAdmission <- err
		}()
	}
	wait.Wait()
	close(errorsByAdmission)
	for err := range errorsByAdmission {
		if err != nil {
			t.Fatalf("concurrent admission: %v", err)
		}
	}
	rawCalls, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(string(rawCalls))); got != 2 {
		t.Fatalf("health Python invocations=%d, want smoke plus binary witness once; calls=%q", got, rawCalls)
	}

	unhealthyManager := NewManager(Config{CondaEnvsPath: envs})
	writeManagedEnvironmentHealthPython(t, generationPath, "2.1.0", "2.2.0", calls)
	if _, found, err := unhealthyManager.ManagedEnvironmentActiveGeneration(name); err == nil || found ||
		!strings.Contains(err.Error(), "binary ABI mismatch") {
		t.Fatalf("unhealthy admission found=%t err=%v", found, err)
	}
	listed, err := unhealthyManager.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{Language: "python"})
	if err != nil || len(listed) != 0 {
		t.Fatalf("unhealthy environment was advertised: listed=%#v err=%v", listed, err)
	}
}

func TestManagedEnvironmentRecoveryIgnoresUnhealthyGenerationForSameOperation(t *testing.T) {
	root := t.TempDir()
	name := "recovered-analysis"
	operationKey := strings.Repeat("a", 64)
	makeGeneration := func(packages []string, built, runtime string) string {
		generation := managedEnvironmentGeneration(name, "python", packages, "")
		path := filepath.Join(root, generation)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		writeManagedEnvironmentHealthPython(t, path, built, runtime, "")
		if err := writeManagedEnvironmentMarker(filepath.Join(path, managedEnvironmentMarkerName), managedEnvironmentMarker{
			SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: "python",
			ValidationRevision: managedEnvironmentValidationRevision,
			Generation:         generation, Packages: packages, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Operation: "create", Kind: "conda", OperationKey: operationKey,
		}); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	unhealthyPackages := []string{
		"python=3.11=h1=conda-forge", "h5py=3.16.0=py311_0=conda-forge", "hdf5=2.2.0=h1=conda-forge",
	}
	healthyPackages := []string{
		"python=3.11=h1=conda-forge", "h5py=3.16.0=py311_1=conda-forge", "hdf5=2.1.0=h1=conda-forge",
	}
	_ = makeGeneration(unhealthyPackages, "2.1.0", "2.2.0")
	healthyGeneration := makeGeneration(healthyPackages, "2.1.0", "2.1.0")
	manager := NewManager(Config{CondaEnvsPath: filepath.Dir(root)})
	recovered, found, err := manager.findManagedEnvironmentOperationGeneration(context.Background(), root, operationKey)
	if err != nil || !found || recovered.environment.Generation != healthyGeneration {
		t.Fatalf("recovered=%#v found=%t err=%v want healthy generation=%s", recovered, found, err, healthyGeneration)
	}

	if err := os.RemoveAll(filepath.Join(root, healthyGeneration)); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(Config{CondaEnvsPath: filepath.Dir(root)})
	if recovered, found, err := manager.findManagedEnvironmentOperationGeneration(context.Background(), root, operationKey); err != nil || found {
		t.Fatalf("unhealthy-only recovery=%#v found=%t err=%v", recovered, found, err)
	}
}

func writeManagedEnvironmentHealthPython(t *testing.T, prefix, built, runtime, calls string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	callLine := ""
	if calls != "" {
		callLine = "printf '%s\\n' call >>'" + strings.ReplaceAll(calls, "'", "'\\''") + "'\n"
	}
	script := "#!/bin/sh\nset -eu\n" + callLine + `
case "$*" in
  *"hdf5_built_version_tuple"*) printf '%s\n' '{"built":[` + strings.ReplaceAll(built, ".", ",") + `],"ok":` + fmt.Sprint(built == runtime) + `,"runtime":[` + strings.ReplaceAll(runtime, ".", ",") + `]}' ;;
  *) printf '%s\n' '{"ok":true,"version":[3,11,9]}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(prefix, "bin", "python"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestManagedEnvironmentRegisterUsesVerifiedExternalVenv(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	source := filepath.Join(root, "granted", "analysis")
	venv := filepath.Join(source, ".venv")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(venv, "bin", "python")
	script := `#!/bin/sh
case "$*" in
  *"pip list"*) printf '%s\n' '[{"name":"pip","version":"24.0"},{"name":"rdkit","version":"2025.3.4"}]' ;;
	*"SYNON_PYTHON_VERSION"*) printf '%s\n' 'SYNON_PYTHON_VERSION=3.13' ;;
  *) printf '%s\n' '{"ok":true,"version":[3,13,2]}' ;;
esac
`
	if err := os.WriteFile(python, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	forkCalls := filepath.Join(root, "fork-calls")
	micromamba := filepath.Join(root, "micromamba")
	micromambaScript := `#!/bin/sh
set -eu
prefix=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create)
    /bin/mkdir -p "$prefix/bin"
    /bin/cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%s\n' "$*" >>'` + forkCalls + `'
case "$*" in
  *"-m pip install"*) exit 0 ;;
  *) printf '%s\n' '{"ok":true,"version":[3,13,2]}' ;;
esac
PY
    /bin/chmod 755 "$prefix/bin/python"
    ;;
  list)
    printf '%s\n' '{"packages":[{"name":"python","version":"3.13.2","build_string":"h1","channel":"conda-forge"},{"name":"rdkit","version":"2025.3.4","build_string":"pip","channel":"pypi"},{"name":"openbabel","version":"3.1.1","build_string":"pip","channel":"pypi"}]}'
    ;;
  *) exit 31 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(micromambaScript), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	registered, err := manager.RegisterManagedEnvironment(context.Background(), RegisterManagedEnvironmentInput{
		Name: "analysis-dev", Language: "python", SourcePath: source, VenvPath: venv,
	})
	if err != nil {
		t.Fatalf("register managed environment: %v", err)
	}
	if registered.Kind != "path-venv" || registered.Generation == "" || len(registered.Packages) != 2 {
		t.Fatalf("registered environment = %#v", registered)
	}
	marker, err := readManagedEnvironmentMarker(filepath.Join(envs, ".generations", "analysis-dev", registered.Generation))
	if err != nil || marker.ValidationRevision != 0 {
		t.Fatalf("external venv incorrectly claims installer verification: %+v err=%v", marker, err)
	}
	prefix, executable, err := manager.managedEnvironmentRuntime("analysis-dev", "python")
	if err != nil || prefix != venv || executable != python {
		t.Fatalf("registered runtime prefix=%q executable=%q err=%v", prefix, executable, err)
	}
	forkInput := MutateManagedPackagesInput{
		Environment: "analysis-dev", Packages: []string{"openbabel==3.1.1"}, UsePip: true, OperationID: "tool-call-fork-1",
		PipFindLinks:      []string{"https://wheels.example.org/release.html"},
		PipExtraIndexURLs: []string{"https://packages.example.org/simple"},
	}
	forked, err := manager.InstallManagedPackages(context.Background(), forkInput)
	if err != nil {
		t.Fatalf("fork registered environment: %v", err)
	}
	replayed, err := manager.InstallManagedPackages(context.Background(), forkInput)
	if err != nil || replayed.Generation != forked.Generation {
		t.Fatalf("replayed fork=%#v err=%v want generation=%s", replayed, err, forked.Generation)
	}
	if forked.Kind != "conda" || forked.Generation == registered.Generation {
		t.Fatalf("forked environment=%#v registered=%#v", forked, registered)
	}
	forkPrefix, _, err := manager.managedEnvironmentRuntime("analysis-dev", "python")
	if err != nil || forkPrefix == venv || !strings.Contains(forkPrefix, filepath.Join(".generations", "analysis-dev")) {
		t.Fatalf("fork runtime prefix=%q err=%v", forkPrefix, err)
	}
	calls, err := os.ReadFile(forkCalls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "rdkit==2025.3.4") || !strings.Contains(string(calls), "openbabel==3.1.1") {
		t.Fatalf("fork package calls=%q", calls)
	}
	if !strings.Contains(string(calls), "--find-links https://wheels.example.org/release.html") ||
		!strings.Contains(string(calls), "--extra-index-url https://packages.example.org/simple") {
		t.Fatalf("registered fork lost reviewed installation sources: %q", calls)
	}
}
func TestManagedEnvironmentSharedOperationHonorsEarliestWaiterDeadline(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{
		Micromamba: filepath.Join(root, "micromamba"), CondaHome: filepath.Join(root, "conda"),
		CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})
	startManagedEnvironmentSupervisor(t, manager)
	started := make(chan struct{})
	operationCancelled := make(chan struct{})
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	run := func(operationContext context.Context) (ManagedEnvironment, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-operationContext.Done()
		close(operationCancelled)
		return ManagedEnvironment{}, operationContext.Err()
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(firstCtx, "earliest-deadline", run)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shared operation did not start")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(secondCtx, "earliest-deadline", run)
		secondDone <- err
	}()
	if err := <-firstDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first waiter error = %v, want deadline exceeded", err)
	}
	select {
	case <-operationCancelled:
	case <-time.After(time.Second):
		t.Fatal("shared operation outlived the earliest waiter deadline")
	}
	if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second waiter error = %v, want earliest deadline exceeded", err)
	}
}
