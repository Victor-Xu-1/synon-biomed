package kernel

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"synon-go/internal/toolprogress"
)

func TestManagedEnvironmentProgressObserverReportsPhasesAndRealPercent(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/micromamba", []string{"create", "-p", "/tmp/env"})
	_, _ = observer.Write([]byte("Downloading and Extracting Packages:\npackage 4.0 MB/10.0 MB 200 KB/s\rExtracting package\n"))
	observer.Complete()

	if len(updates) < 4 {
		t.Fatalf("updates=%#v", updates)
	}
	foundPercent := false
	foundExtract := false
	for _, update := range updates {
		if update.Phase == "downloading_packages" && update.PhasePercent != nil && *update.PhasePercent == 40 &&
			update.BytesPerSecond != nil && *update.BytesPerSecond == 200000 &&
			update.BytesCompleted != nil && *update.BytesCompleted == 4000000 &&
			update.BytesTotal != nil && *update.BytesTotal == 10000000 {
			foundPercent = true
		}
		if update.Phase == "extracting_packages" {
			foundExtract = true
		}
	}
	if !foundPercent || !foundExtract || updates[len(updates)-1].Phase != "installer_process_completed" {
		t.Fatalf("updates=%#v", updates)
	}
}

func TestManagedEnvironmentProcessTerminatesAfterObservableInactivity(t *testing.T) {
	manager := NewManager(Config{ManagedEnvironmentInstallerInactivityTimeout: 120 * time.Millisecond})
	started := time.Now()
	err := manager.runManagedEnvironmentProcessWithEnv(
		context.Background(), "/bin/sh", os.Environ(), "-c", "while :; do sleep 10; done",
	)
	var inactivity *ManagedEnvironmentInstallerInactivityError
	if !errors.As(err, &inactivity) || inactivity.Duration != 120*time.Millisecond {
		t.Fatalf("error=%v inactivity=%#v", err, inactivity)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("inactive installer took %s to settle", elapsed)
	}
}

func TestManagedEnvironmentProcessKeepsSilentCPUWorkAlive(t *testing.T) {
	if os.Getenv("SYNON_BUSY_MANAGED_INSTALLER_HELPER") == "1" {
		until := time.Now().Add(750 * time.Millisecond)
		for time.Now().Before(until) {
		}
		return
	}
	manager := NewManager(Config{ManagedEnvironmentInstallerInactivityTimeout: 250 * time.Millisecond})
	environment := append(os.Environ(), "SYNON_BUSY_MANAGED_INSTALLER_HELPER=1", "GORACE=atexit_sleep_ms=0")
	err := manager.runManagedEnvironmentProcessWithEnv(
		context.Background(), os.Args[0], environment,
		"-test.run=^TestManagedEnvironmentProcessKeepsSilentCPUWorkAlive$",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedEnvironmentProcessStreamsObservedProgress(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	manager := &Manager{}
	err := manager.runManagedEnvironmentProcessWithEnv(
		ctx, "/bin/sh", os.Environ(), "-c",
		"printf 'Collecting fixture\\nDownloading fixture 50%%\\nInstalling collected packages\\nSuccessfully installed fixture\\n'",
	)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, update := range updates {
		seen[update.Phase] = true
	}
	for _, phase := range []string{"resolving_dependencies", "downloading_packages", "installing_packages", "verifying_environment", "installer_process_completed"} {
		if !seen[phase] {
			t.Fatalf("phase %q missing from %#v", phase, updates)
		}
	}
}

func TestManagedEnvironmentProcessKeepsSlowObservableWorkAlive(t *testing.T) {
	manager := NewManager(Config{ManagedEnvironmentInstallerInactivityTimeout: 400 * time.Millisecond})
	started := time.Now()
	err := manager.runManagedEnvironmentProcessWithEnv(context.Background(), "/bin/sh", os.Environ(),
		"-c", "for i in 1 2 3 4 5 6 7 8; do printf '.'; sleep 0.12; done")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 800*time.Millisecond {
		t.Fatal("slow operation did not run past multiple inactivity windows")
	}
}
