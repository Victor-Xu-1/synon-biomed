package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedOperationCancellationWaitsForCleanup(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{Micromamba: filepath.Join(root, "micromamba"), CondaHome: filepath.Join(root, "conda"), CondaEnvsPath: filepath.Join(root, "conda", "envs")})
	startManagedEnvironmentSupervisor(t, manager)
	started := make(chan struct{})
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	cleanupDone := make(chan struct{})
	defer func() {
		close(cleanupRelease)
		select {
		case <-cleanupDone:
		case <-time.After(time.Second):
			t.Error("cleanup did not finish")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(ctx, "audit-cleanup", func(work context.Context) (ManagedEnvironment, error) {
			close(started)
			<-work.Done()
			close(cleanupStarted)
			<-cleanupRelease
			close(cleanupDone)
			return ManagedEnvironment{}, work.Err()
		})
		returned <- err
	}()
	<-started
	cancel()
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation was not propagated")
	}
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		t.Fatal("operation returned cancelled before its underlying cleanup completed")
	case <-time.After(100 * time.Millisecond):
	}
}
func TestManagedExplicitForkRecoversPublishedReceipt(t *testing.T) {
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
	marker, err := manager.activeManagedEnvironmentMarker("analysis-dev")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.publishRegisteredManagedEnvironment(context.Background(), "analysis-dev", "python", source, venv, marker.OperationKey, func(context.Context) error {
		t.Error("registration replay repeated its preparation side effect")
		return nil
	})
	if err != nil || recovered.Generation != registered.Generation {
		t.Fatalf("registration recovery=%#v err=%v", recovered, err)
	}
	prefix, executable, err := manager.managedEnvironmentRuntime("analysis-dev", "python")
	if err != nil || prefix != venv || executable != python {
		t.Fatalf("registered runtime prefix=%q executable=%q err=%v", prefix, executable, err)
	}
	forkInput := MutateManagedPackagesInput{
		Environment: "analysis-dev", ForkTo: "analysis-fork", Packages: []string{"openbabel==3.1.1"}, UsePip: true, OperationID: "tool-call-fork-1",
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
	forkPrefix, _, err := manager.managedEnvironmentRuntime("analysis-fork", "python")
	if err != nil || forkPrefix == venv || !strings.Contains(forkPrefix, filepath.Join(".generations", "analysis-fork")) {
		t.Fatalf("fork runtime prefix=%q err=%v", forkPrefix, err)
	}
	calls, err := os.ReadFile(forkCalls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "rdkit==2025.3.4") || !strings.Contains(string(calls), "openbabel==3.1.1") {
		t.Fatalf("fork package calls=%q", calls)
	}
}

func TestManagedOperationSuccessorWaitsForCancelledOwnerToDrain(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{Micromamba: filepath.Join(root, "micromamba"), CondaHome: filepath.Join(root, "conda"), CondaEnvsPath: filepath.Join(root, "conda", "envs")})
	startManagedEnvironmentSupervisor(t, manager)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, draining, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(ctx, "shared-operation", func(work context.Context) (ManagedEnvironment, error) {
			close(started)
			<-work.Done()
			close(draining)
			<-release
			return ManagedEnvironment{}, work.Err()
		})
		firstDone <- err
	}()
	<-started
	cancel()
	<-draining
	secondStarted, secondDone := make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := manager.runManagedEnvironmentOperation(context.Background(), "shared-operation", func(context.Context) (ManagedEnvironment, error) {
			close(secondStarted)
			return ManagedEnvironment{Name: "successor"}, nil
		})
		secondDone <- err
	}()
	select {
	case <-secondStarted:
		t.Error("successor overlapped the cancelled owner")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled owner did not finish")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("successor did not run")
	}
}
