package runtimecontrol

import (
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
	first := scanner.DiskUsage()
	if first.Artifacts.TotalBytes != 7 || first.Workspace.TotalBytes != 11 ||
		first.ToolResults.TotalBytes != 13 || first.Conda.TotalBytes != 36 ||
		first.AvailableBytes == nil || *first.AvailableBytes == 0 || len(first.Warnings) != 0 ||
		first.Artifacts.ByProject == nil {
		t.Fatalf("first disk usage = %#v", first)
	}

	writeUsageFile(t, filepath.Join(root, "artifacts", "new.bin"), 5)
	cached := scanner.DiskUsage()
	if cached.Artifacts.TotalBytes != 7 {
		t.Fatalf("cache was not reused = %#v", cached)
	}
	now = now.Add(time.Minute + time.Second)
	refreshed := scanner.DiskUsage()
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
	usage := scanner.CondaDiskUsage()
	if usage.PkgsBytes != 5 || usage.Truncated || len(usage.Warnings) != 0 || len(usage.Envs) != 2 ||
		usage.Envs[0].Name != "large" || usage.Envs[0].Bytes != 9 ||
		usage.Envs[1].Name != "small" || usage.Envs[1].Bytes != 3 {
		t.Fatalf("conda usage = %#v", usage)
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
	usage := scanner.DiskUsage()
	if usage.Artifacts.TotalBytes != 7 || usage.ToolResults.TotalBytes != 13 {
		t.Fatalf("configured storage usage = %#v", usage)
	}

	nextArtifactsRoot := filepath.Join(root, "next", "task-files")
	nextToolResultsRoot := filepath.Join(root, "next", "tool-results")
	writeUsageFile(t, filepath.Join(nextArtifactsRoot, "artifact.bin"), 17)
	writeUsageFile(t, filepath.Join(nextToolResultsRoot, "result.bin"), 19)
	scanner.SetStorageRoots(nextArtifactsRoot, nextToolResultsRoot)
	updated := scanner.DiskUsage()
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
	usage := scanner.CondaDiskUsage()
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
	if usage := scanner.DiskUsage(); usage.Artifacts.ByProject == nil {
		t.Fatalf("artifact project collection is nil: %#v", usage)
	}
	if usage := scanner.CondaDiskUsage(); usage.Envs == nil {
		t.Fatalf("conda environment collection is nil: %#v", usage)
	}
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
