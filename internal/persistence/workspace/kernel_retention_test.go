package workspace

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyKernelOperationChangedClosesCurrentWakeGeneration(t *testing.T) {
	store := &Store{}
	wake := store.KernelRetentionWake()
	store.NotifyKernelOperationChanged()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("kernel operation notification did not wake recovery")
	}
}

func TestListKernelRetentionStatesBatchesTimeoutAndDurableProtection(t *testing.T) {
	if DefaultKernelIdleTimeoutSeconds != 5 {
		t.Fatalf("default kernel idle timeout=%d, want task-terminal convergence grace 5", DefaultKernelIdleTimeoutSeconds)
	}
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "worker",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListKernelRetentionStates(context.Background(), []string{"root", "missing"})
	if err != nil || len(states) != 2 || !states[0].Exists || states[0].Protected ||
		states[0].IdleTimeoutSeconds != DefaultKernelIdleTimeoutSeconds || states[1].Exists {
		t.Fatalf("initial retention states=%#v err=%v", states, err)
	}
	if _, err := store.PatchCompatibilitySessionConfig("child", map[string]any{"kernel_idle_timeout": 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status='awaiting_user_response' WHERE id='child'`); err != nil {
		t.Fatal(err)
	}
	states, err = store.ListKernelRetentionStates(context.Background(), []string{"root", "root"})
	if err != nil || len(states) != 1 || !states[0].Protected || states[0].IdleTimeoutSeconds != 60 {
		t.Fatalf("protected retention states=%#v err=%v", states, err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status='completed' WHERE id='child'`); err != nil {
		t.Fatal(err)
	}
	states, err = store.ListKernelRetentionStates(context.Background(), []string{"root"})
	if err != nil || len(states) != 1 || states[0].Protected || states[0].IdleTimeoutSeconds != 60 {
		t.Fatalf("terminal retention states=%#v err=%v", states, err)
	}
}
