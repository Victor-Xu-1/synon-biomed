package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAvailableBytesReportsWritableFilesystemCapacity(t *testing.T) {
	available := AvailableBytes(t.TempDir())
	if available == nil || *available == 0 {
		t.Fatalf("available bytes = %#v", available)
	}
}

func TestScannerRefreshCancellationAndCacheIsolation(t *testing.T) {
	root := t.TempDir()
	scanner := NewScanner(root, filepath.Join(root, "conda"), time.Hour)
	writeUsageFile(t, filepath.Join(root, "artifacts", "a"), 5)
	first := mustDiskUsage(t, scanner, false)
	if first.ScannedAt.IsZero() {
		t.Fatal("missing scan timestamp")
	}
	*first.AvailableBytes = 0
	first.Artifacts.ByProject = append(first.Artifacts.ByProject, ProjectSize{ProjectID: "untrusted"})
	writeUsageFile(t, filepath.Join(root, "artifacts", "b"), 7)
	cached := mustDiskUsage(t, scanner, false)
	if cached.Artifacts.TotalBytes != 5 || *cached.AvailableBytes == 0 || len(cached.Artifacts.ByProject) != 0 {
		t.Fatalf("cache leaked caller mutation: %#v", cached)
	}
	if refreshed := mustDiskUsage(t, scanner, true); refreshed.Artifacts.TotalBytes != 12 {
		t.Fatalf("explicit refresh used stale values: %#v", refreshed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanner.DiskUsage(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := scanner.CondaDiskUsage(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel details: %v", err)
	}
	if usage := mustDiskUsage(t, scanner, false); usage.Artifacts.TotalBytes != 12 {
		t.Fatal("cancel corrupted cache")
	}
}

func TestScannerConfiguredLogsTempAndNestedRoots(t *testing.T) {
	root := t.TempDir()
	conda := filepath.Join(root, "conda")
	writeUsageFile(t, filepath.Join(conda, "envs", "one", "package"), 11)
	writeUsageFile(t, filepath.Join(root, "custom", "logs", "log"), 13)
	writeUsageFile(t, filepath.Join(root, "custom", "temp", "tmp"), 17)
	scanner := NewScanner(root, conda, time.Minute)
	scanner.SetStorageRoots(StorageRoots{Logs: filepath.Join(root, "custom", "logs"), Temp: filepath.Join(root, "custom", "temp")})
	usage := mustDiskUsage(t, scanner, false)
	if usage.Conda.TotalBytes != 11 || usage.Logs.TotalBytes != 13 || usage.Temp.TotalBytes != 17 {
		t.Fatalf("configured and nested roots: %#v", usage)
	}
	size, err := scanDirectoryBytes(context.Background(), true, conda, filepath.Join(conda, "envs"), conda)
	if err != nil || size != 11 {
		t.Fatalf("overlapping roots: %d %v", size, err)
	}
}

func TestScanGateWaitIsCancelable(t *testing.T) {
	var gate scanGate
	if err := gate.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer gate.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked scan did not cancel: %v", err)
	}
}

func TestScannerCountsHardLinksOnceButPreservesMigrationCopyEstimate(t *testing.T) {
	root := t.TempDir()
	conda := filepath.Join(root, "conda")
	original := filepath.Join(conda, "pkgs", "package.bin")
	linked := filepath.Join(conda, "envs", "analysis", "package.bin")
	writeUsageFile(t, original, 31)
	if err := os.MkdirAll(filepath.Dir(linked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	usage := mustDiskUsage(t, NewScanner(root, conda, time.Minute), false)
	if usage.Conda.TotalBytes != 31 {
		t.Fatalf("display usage counts one file more than once: %d", usage.Conda.TotalBytes)
	}
	copyBytes, _, err := PathUsage(conda)
	if err != nil || copyBytes != 62 {
		t.Fatalf("migration still copies each entry; capacity estimate = %d, %v", copyBytes, err)
	}
}

func TestScannerDiskUsageUsesRealFilesAndExpiresCache(t *testing.T) {
	root := t.TempDir()
	condaRoot := filepath.Join(root, "conda")
	writeUsageFile(t, filepath.Join(root, "artifacts", "artifact.bin"), 7)
	writeUsageFile(t, filepath.Join(root, "workspace", "workspace.bin"), 11)
	writeUsageFile(t, filepath.Join(root, "tool-results", "result.bin"), 13)
	writeUsageFile(t, filepath.Join(condaRoot, "envs", "env-a", "package.bin"), 17)
	writeUsageFile(t, filepath.Join(condaRoot, "pkgs", "cache.bin"), 19)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	writeUsageFile(t, outside, 23)
	if err := os.Symlink(outside, filepath.Join(root, "artifacts", "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	now := time.Unix(1_700_000_000, 0)
	scanner := NewScanner(root, condaRoot, time.Minute)
	scanner.now = func() time.Time { return now }
	first := mustDiskUsage(t, scanner, false)
	if first.Artifacts.TotalBytes != 7 || first.Workspace.TotalBytes != 11 ||
		first.ToolResults.TotalBytes != 13 || first.Conda.TotalBytes != 36 ||
		first.AvailableBytes == nil || *first.AvailableBytes == 0 || len(first.Warnings) != 0 ||
		first.Artifacts.ByProject == nil {
		t.Fatalf("first disk usage = %#v", first)
	}

	writeUsageFile(t, filepath.Join(root, "artifacts", "new.bin"), 5)
	cached := mustDiskUsage(t, scanner, false)
	if cached.Artifacts.TotalBytes != 7 {
		t.Fatalf("cache was not reused = %#v", cached)
	}
	now = now.Add(time.Minute + time.Second)
	refreshed := mustDiskUsage(t, scanner, false)
	if refreshed.Artifacts.TotalBytes != 12 {
		t.Fatalf("expired cache was not refreshed = %#v", refreshed)
	}
}

func TestScannerCondaUsageIsBoundedAndDeterministic(t *testing.T) {
	root := t.TempDir()
	condaRoot := filepath.Join(root, "conda")
	condaEnvsRoot := filepath.Join(root, "external-envs")
	writeUsageFile(t, filepath.Join(condaEnvsRoot, "small", "package.bin"), 3)
	writeUsageFile(t, filepath.Join(condaEnvsRoot, "large", "package.bin"), 9)
	writeUsageFile(t, filepath.Join(condaEnvsRoot, "invalid name", "package.bin"), 20)
	writeUsageFile(t, filepath.Join(condaRoot, "pkgs", "cache.bin"), 5)
	scanner := NewScannerWithCondaEnvs(root, condaRoot, condaEnvsRoot, time.Minute)
	usage := mustCondaUsage(t, scanner)
	if usage.PkgsBytes != 5 || usage.Truncated || len(usage.Warnings) != 0 || len(usage.Envs) != 2 ||
		usage.Envs[0].Name != "large" || usage.Envs[0].Bytes != 9 ||
		usage.Envs[1].Name != "small" || usage.Envs[1].Bytes != 3 {
		t.Fatalf("conda usage = %#v", usage)
	}
}

func TestScannerGroupsRetainedGenerationsWithoutFollowingActivationLinks(t *testing.T) {
	root := t.TempDir()
	conda := filepath.Join(root, "conda")
	envs := filepath.Join(conda, "envs")
	generation := filepath.Join(envs, ".generations", "analysis", "generation-one")
	writeUsageFile(t, filepath.Join(generation, "package"), 11)
	writeUsageFile(t, filepath.Join(envs, ".generations", "analysis", "generation-two", "package"), 13)
	writeUsageFile(t, filepath.Join(envs, "legacy", "package"), 17)
	outside := t.TempDir()
	writeUsageFile(t, filepath.Join(outside, "private"), 101)
	if err := os.Symlink(generation, filepath.Join(envs, "analysis")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(envs, "external")); err != nil {
		t.Fatal(err)
	}
	usage := mustCondaUsage(t, NewScanner(root, conda, time.Minute))
	if len(usage.Envs) != 2 || usage.Envs[0].Name != "analysis" || usage.Envs[0].Bytes != 24 ||
		usage.Envs[1].Name != "legacy" || usage.Envs[1].Bytes != 17 {
		t.Fatalf("physical environment groups: %#v", usage)
	}
}

func TestScannerDiskUsageUsesConfiguredStorageRoots(t *testing.T) {
	root := t.TempDir()
	condaRoot := filepath.Join(root, "conda")
	artifactsRoot := filepath.Join(root, "generated", "task-files")
	toolResultsRoot := filepath.Join(root, "cache", "tool-results")
	writeUsageFile(t, filepath.Join(artifactsRoot, "artifact.bin"), 7)
	writeUsageFile(t, filepath.Join(toolResultsRoot, "result.bin"), 13)

	scanner := NewScannerWithStorageRoots(
		root, condaRoot, filepath.Join(condaRoot, "envs"), artifactsRoot, toolResultsRoot, time.Minute,
	)
	usage := mustDiskUsage(t, scanner, false)
	if usage.Artifacts.TotalBytes != 7 || usage.ToolResults.TotalBytes != 13 {
		t.Fatalf("configured storage usage = %#v", usage)
	}

	nextArtifactsRoot := filepath.Join(root, "next", "task-files")
	nextToolResultsRoot := filepath.Join(root, "next", "tool-results")
	writeUsageFile(t, filepath.Join(nextArtifactsRoot, "artifact.bin"), 17)
	writeUsageFile(t, filepath.Join(nextToolResultsRoot, "result.bin"), 19)
	scanner.SetStorageRoots(StorageRoots{Artifacts: nextArtifactsRoot, ToolResults: nextToolResultsRoot})
	updated := mustDiskUsage(t, scanner, false)
	if updated.Artifacts.TotalBytes != 17 || updated.ToolResults.TotalBytes != 19 {
		t.Fatalf("updated configured storage usage = %#v", updated)
	}
}

func TestScannerCondaUsageTruncatesBeforeParallelScan(t *testing.T) {
	root := t.TempDir()
	condaRoot := filepath.Join(root, "conda")
	for index := range maxCondaEnvironments + 3 {
		name := filepath.Join(condaRoot, "envs", fmt.Sprintf("env-%03d", index), "package.bin")
		writeUsageFile(t, name, index+1)
	}
	scanner := NewScanner(root, condaRoot, time.Minute)
	usage := mustCondaUsage(t, scanner)
	if !usage.Truncated || len(usage.Envs) != maxCondaEnvironments {
		t.Fatalf("bounded Conda usage = %#v", usage)
	}
	for _, environment := range usage.Envs {
		if environment.Name == "env-256" || environment.Name == "env-257" || environment.Name == "env-258" {
			t.Fatalf("environment outside deterministic bound was scanned: %#v", environment)
		}
	}
}

func TestScannerEmptyCollectionsRemainJSONArrays(t *testing.T) {
	scanner := NewScanner(t.TempDir(), filepath.Join(t.TempDir(), "conda"), time.Minute)
	if usage := mustDiskUsage(t, scanner, false); usage.Artifacts.ByProject == nil {
		t.Fatalf("artifact project collection is nil: %#v", usage)
	}
	if usage := mustCondaUsage(t, scanner); usage.Envs == nil {
		t.Fatalf("conda environment collection is nil: %#v", usage)
	}
}

func mustDiskUsage(t *testing.T, scanner *Scanner, refresh bool) DiskUsage {
	t.Helper()
	usage, err := scanner.DiskUsage(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func mustCondaUsage(t *testing.T, scanner *Scanner) CondaDiskUsage {
	t.Helper()
	usage, err := scanner.CondaDiskUsage(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func writeUsageFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}
