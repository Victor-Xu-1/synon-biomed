package kernel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func replayTestManager(t *testing.T) *Manager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the local installer fixture uses a POSIX shell")
	}
	root := t.TempDir()
	condaHome := filepath.Join(root, "conda")
	envs := filepath.Join(condaHome, "envs")
	if err := os.MkdirAll(envs, 0o700); err != nil {
		t.Fatal(err)
	}
	micromamba := filepath.Join(root, "micromamba")
	fixture := `#!/bin/sh
set -eu
mode=""
prefix=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|list|install) mode="$1" ;;
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
    core_source=0
    binding_source=0
    core=0
    binding=0
    extra=0
    other=0
    for arg in "$@"; do
      case "$arg" in
        https://example.test/core) core_source=1 ;;
        https://example.test/binding) binding_source=1 ;;
        core|core==1.0) core=1 ;;
        binding) binding=1 ;;
        binding==1.0) binding=1; binding_replay=1 ;;
        --no-build-isolation) no_isolation=1 ;;
        extra|extra==1.0) extra=1 ;;
        other|other==1.0) other=1 ;;
      esac
    done
    if [ "$binding" -eq 1 ] && [ ! -f "$prefix/.core" ]; then
      echo "ModuleNotFoundError: No module named 'core'" >&2
      exit 42
    fi
    if [ "${binding_replay:-0}" -eq 1 ] && [ "${no_isolation:-0}" -ne 1 ]; then
      echo 'binding replay lost build policy' >&2
      exit 46
    fi
    if [ "$core" -eq 1 ]; then
      [ "$core_source" -eq 1 ] || { echo 'core source was lost' >&2; exit 41; }
      /usr/bin/touch "$prefix/.core"
    fi
    if [ "$binding" -eq 1 ]; then
      [ -f "$prefix/.core" ] || { echo 'core was not restored first' >&2; exit 42; }
      [ "$binding_source" -eq 1 ] || { echo 'binding source was lost' >&2; exit 43; }
      /usr/bin/touch "$prefix/.binding"
    fi
    if [ "$extra" -eq 1 ]; then
      [ -f "$prefix/.core" ] && [ -f "$prefix/.binding" ] || exit 44
      /usr/bin/touch "$prefix/.extra"
    fi
    if [ "$other" -eq 1 ]; then
      [ -f "$prefix/.core" ] || exit 47
      /usr/bin/touch "$prefix/.other"
    fi
    ;;
  *"-m pip uninstall"*)
    for arg in "$@"; do
      case "$arg" in binding) /bin/rm -f "$prefix/.binding" ;; esac
    done
    ;;
  *) printf '%s\n' '{"ok":true,"version":[3,11,15]}' ;;
esac
PY
    /bin/chmod 755 "$prefix/bin/python"
    ;;
  list)
    printf '%s' '{"packages":[{"name":"python","version":"3.11.15","build_string":"h1","channel":"conda-forge"},{"name":"pip","version":"25.0","build_string":"h1","channel":"conda-forge"}'
    [ ! -f "$prefix/.core" ] || printf '%s' ',{"name":"core","version":"1.0","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.binding" ] || printf '%s' ',{"name":"binding","version":"1.0","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.extra" ] || printf '%s' ',{"name":"extra","version":"1.0","build_string":"pypi_0","channel":"pypi"}'
    [ ! -f "$prefix/.other" ] || printf '%s' ',{"name":"other","version":"1.0","build_string":"pypi_0","channel":"pypi"}'
    printf '%s\n' ']}'
    ;;
  *) exit 45 ;;
esac
`
	if err := os.WriteFile(micromamba, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Micromamba: micromamba, CondaHome: condaHome, CondaEnvsPath: envs})
	startManagedEnvironmentSupervisor(t, manager)
	return manager
}

func TestManagedPipMutationReplaysOriginalSourcesInOrder(t *testing.T) {
	manager := replayTestManager(t)
	_, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "source-replay", Language: "python", PipPhases: [][]string{{"core"}},
		PipExtraIndexURLs: []string{"https://example.test/core"},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	_, err = manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: "source-replay", Packages: []string{"binding"}, UsePip: true,
		PipExtraIndexURLs: []string{"https://example.test/binding"}, OperationID: "add-binding",
	})
	if err != nil {
		t.Fatalf("add package with distinct source: %v", err)
	}
	result, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
		Environment: "source-replay", Packages: []string{"extra"}, UsePip: true, OperationID: "add-extra",
	})
	if err != nil || !managedDependenciesSatisfied(result.Packages, []string{"core", "binding", "extra"}) {
		t.Fatalf("replay prior sources across another generation: result=%#v err=%v", result, err)
	}
	if result.Generation == "" {
		t.Fatal("successful mutation has no generation")
	}
}

func TestManagedPipLegacyInventoryRestoresBuildProviderBeforeExtension(t *testing.T) {
	manager := replayTestManager(t)
	ctx := context.Background()
	name := "legacy-replay"
	operationKey := managedEnvironmentOperationKey("legacy-create", name)
	created, err := manager.publishManagedEnvironment(ctx, name, "python", "create", operationKey,
		nil, "", false, nil, nil, nil, func(prefix string) error {
			if err := manager.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "-p", prefix, "python=3.11", "pip"); err != nil {
				return err
			}
			return manager.runManagedPipInstallPlan(ctx, prefix, planManagedPipInstall(
				[][]string{{"core"}, {"binding"}}, nil, nil,
				[]string{"https://example.test/core", "https://example.test/binding"}, "",
			))
		})
	if err != nil || !managedDependenciesSatisfied(created.Packages, []string{"core", "binding"}) {
		t.Fatalf("prepare existing environment: result=%#v err=%v", created, err)
	}
	result, err := manager.InstallManagedPackages(ctx, MutateManagedPackagesInput{
		Environment: name, Packages: []string{"extra"}, UsePip: true, OperationID: "legacy-add-extra",
		PipExtraIndexURLs: []string{"https://example.test/core", "https://example.test/binding"},
	})
	if err != nil || !managedDependenciesSatisfied(result.Packages, []string{"core", "binding", "extra"}) {
		t.Fatalf("recover legacy package build order: result=%#v err=%v", result, err)
	}
	marker, err := manager.activeManagedEnvironmentMarker(name)
	if err != nil || marker.PipReplayRevision != 1 || len(marker.PipReplay) != 3 ||
		marker.PipReplay[0].Requirements[0] != "core==1.0" || marker.PipReplay[1].Requirements[0] != "binding==1.0" {
		t.Fatalf("durable replay after legacy recovery: marker=%#v err=%v", marker, err)
	}
}

func TestManagedPipReplayMarkerRejectsChangedSourceOrMissingPhases(t *testing.T) {
	manager := replayTestManager(t)
	_, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "receipt-check", Language: "python", PipPhases: [][]string{{"core"}},
		PipExtraIndexURLs: []string{"https://example.test/core"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(manager.config.CondaEnvsPath, "receipt-check"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := readManagedEnvironmentMarker(prefix)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(prefix, managedEnvironmentMarkerName)
	changed := original
	changed.PipReplay = cloneManagedPipReplay(original.PipReplay)
	changed.PipReplay[0].ExtraIndexURLs = []string{"https://example.test/other"}
	if err := writeManagedEnvironmentMarker(path, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedEnvironmentMarker(prefix); err == nil {
		t.Fatal("changed package source passed generation validation")
	}
	original.PipReplay = nil
	if err := writeManagedEnvironmentMarker(path, original); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedEnvironmentMarker(prefix); err == nil {
		t.Fatal("missing replay phases passed generation validation")
	}
}

func TestManagedPipUninstallRetiresRemovedReplayPhase(t *testing.T) {
	manager := replayTestManager(t)
	ctx := context.Background()
	_, err := manager.CreateManagedEnvironment(ctx, CreateManagedEnvironmentInput{
		Name: "uninstall-replay", Language: "python",
		PipPhases:         [][]string{{"core"}, {"binding"}},
		PipExtraIndexURLs: []string{"https://example.test/core", "https://example.test/binding"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.UninstallManagedPackages(ctx, MutateManagedPackagesInput{
		Environment: "uninstall-replay", Packages: []string{"binding"}, UsePip: true,
		OperationID: "remove-binding",
	})
	if err != nil || managedDependenciesSatisfied(result.Packages, []string{"binding"}) ||
		!managedDependenciesSatisfied(result.Packages, []string{"core"}) {
		t.Fatalf("removed distribution replayed: result=%#v err=%v", result, err)
	}
	marker, err := manager.activeManagedEnvironmentMarker("uninstall-replay")
	if err != nil || len(marker.PipReplay) != 1 || marker.PipReplay[0].Requirements[0] != "core" {
		t.Fatalf("removed replay phase persisted: marker=%#v err=%v", marker, err)
	}
	result, err = manager.InstallManagedPackages(ctx, MutateManagedPackagesInput{
		Environment: "uninstall-replay", Packages: []string{"other"}, UsePip: true,
		OperationID: "add-other",
	})
	if err != nil || !managedDependenciesSatisfied(result.Packages, []string{"core", "other"}) ||
		managedDependenciesSatisfied(result.Packages, []string{"binding"}) {
		t.Fatalf("next generation restored removed package: result=%#v err=%v", result, err)
	}
}
