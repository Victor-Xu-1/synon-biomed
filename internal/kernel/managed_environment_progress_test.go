package kernel

import (
	"context"
	"os"
	"testing"

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
