package workspace

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTransitionCompatibilityFrameStatusHasSingleConcurrentWinner(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_plan_approval", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	var winners atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			changed, err := store.TransitionCompatibilityFrameStatus(
				"frame", "awaiting_plan_approval", "processing",
			)
			if err != nil {
				failures.Add(1)
				return
			}
			if changed {
				winners.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || winners.Load() != 1 {
		t.Fatalf("transition failures=%d winners=%d", failures.Load(), winners.Load())
	}
	frame, found, err := store.GetFrame("frame")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame = %#v, found=%t, err=%v", frame, found, err)
	}
}
