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

func TestManagedEnvironmentProgressObserverPublishesProcessName(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/micromamba", []string{"create", "-p", "/tmp/env"})
	_, _ = observer.Write([]byte("Transaction starting\n"))
	observer.Complete()
	if len(updates) == 0 {
		t.Fatal("updates missing")
	}
	for _, update := range updates {
		if update.Process != "micromamba" {
			t.Fatalf("process=%q updates=%#v", update.Process, updates)
		}
	}
}

func TestManagedEnvironmentProgressObserverTracksPipProcessAndTransfer(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/env/bin/python", []string{"-m", "pip", "install", "requests"})
	current := time.Unix(100, 0)
	observer.clock = func() time.Time { return current }
	_, _ = observer.Write([]byte("0/1.5 MB\n"))
	current = current.Add(2 * time.Second)
	_, _ = observer.Write([]byte("0.75/1.5 MB\n"))
	observer.Complete()
	foundProcess := false
	foundBytes := false
	foundRate := false
	for _, update := range updates {
		if update.Process == "pip" {
			foundProcess = true
		}
		if update.BytesCompleted != nil && update.BytesTotal != nil &&
			*update.BytesCompleted == 750000 && *update.BytesTotal == 1500000 {
			foundBytes = true
			if update.Indeterminate {
				t.Fatalf("byte progress was marked indeterminate: %#v", update)
			}
		}
		if update.BytesPerSecond != nil && *update.BytesPerSecond == 375000 {
			foundRate = true
		}
	}
	if !foundProcess || !foundBytes || !foundRate {
		t.Fatalf("process=%#v updates=%#v", updates[len(updates)-1].Process, updates)
	}
}

func TestManagedEnvironmentProgressObserverReportsPackageCountMilestone(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/env/bin/python", []string{"-m", "pip", "install", "a"})
	_, _ = observer.Write([]byte("Installing collected packages: alpha, beta, gamma\n"))
	observer.Complete()
	found := false
	for _, update := range updates {
		if update.CompletedItems != nil && update.TotalItems != nil &&
			*update.CompletedItems == 0 && *update.TotalItems == 3 && update.Process == "pip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("updates=%#v", updates)
	}
}

func TestManagedEnvironmentProgressObserverReportsPlannedTotalAndCompletion(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/micromamba", []string{"create", "-p", "/tmp/env"})
	_, _ = observer.Write([]byte("Total download: 41MB\nTransaction starting\nExtracting package\nTransaction finished\n"))
	observer.Complete()
	total := int64(41000000)
	var sawPlanned, sawDone bool
	for _, update := range updates {
		if update.BytesTotal != nil && *update.BytesTotal == total && update.BytesCompleted != nil && *update.BytesCompleted == 0 {
			sawPlanned = true
		}
		if update.BytesTotal != nil && *update.BytesTotal == total && update.BytesCompleted != nil && *update.BytesCompleted == total {
			sawDone = true
		}
	}
	if !sawPlanned || !sawDone {
		t.Fatalf("planned=%v done=%v updates=%#v", sawPlanned, sawDone, updates)
	}
}

func TestManagedEnvironmentProcessNameDoesNotMisclassifyGenericExecutablesAsR(t *testing.T) {
	if got := managedEnvironmentProcessName("/usr/bin/curl", nil); got != "curl" {
		t.Fatalf("curl process=%q, want curl", got)
	}
	if got := managedEnvironmentProcessName("/usr/bin/Rscript", []string{"--vanilla"}); got != "r" {
		t.Fatalf("Rscript process=%q, want r", got)
	}
}

func TestManagedEnvironmentProgressObserverDeduplicatesRepeatedPackageList(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	observer := newManagedEnvironmentProgressObserver(ctx, "/opt/env/bin/python", []string{"-m", "pip", "install", "a"})
	_, _ = observer.Write([]byte("Installing collected packages: alpha, beta\nInstalling collected packages: alpha, beta\n"))
	countUpdates := 0
	for _, update := range updates {
		if update.CompletedItems != nil && update.TotalItems != nil {
			countUpdates++
		}
	}
	if countUpdates != 1 {
		t.Fatalf("package milestone updates=%d, updates=%#v", countUpdates, updates)
	}
}
