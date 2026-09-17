package kernel

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWorkerRecordsReadDependenciesWithoutOutputOrCrossCellContamination(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{})
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "input.csv"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	worker, err := manager.StartSession(SessionSpec{KernelID: "dependency-kernel", FrameID: "dependency-frame", RootFrameID: "dependency-frame", AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, "from pathlib import Path\nPath('progress.md').write_text('output')\nint(Path('input.csv').read_text())", "user")
	if err != nil || response.Error == "" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	reads, _ := response.Trace["execution_reads"].(map[string]any)
	items, _ := reads["paths"].([]any)
	var paths []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			paths = append(paths, s)
		}
	}
	if !slices.Contains(paths, "input.csv") || slices.Contains(paths, "progress.md") {
		t.Fatalf("read dependencies=%#v", reads)
	}
	response, err = worker.Execute(ctx, "print('next cell')", "user")
	if err != nil {
		t.Fatal(err)
	}
	reads, _ = response.Trace["execution_reads"].(map[string]any)
	items, _ = reads["paths"].([]any)
	for _, item := range items {
		if item == "input.csv" {
			t.Fatalf("old cell leaked into next: %#v", reads)
		}
	}
}
