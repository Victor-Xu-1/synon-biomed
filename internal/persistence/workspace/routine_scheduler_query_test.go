package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNextRoutineWakeAtAccountsForLeaseExpiry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(CreateProjectInput{ID: "project-scheduler", UserID: "user-1", Name: "Scheduler"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	root, err := store.CreateFrame(CreateFrameInput{ID: "frame-scheduler", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = store.CreateRoutine(CreateRoutineInput{ID: "routine-scheduler", RootFrameID: root.ID, OwnerUserID: "user-1", OnTick: "continue", EveryMinutes: 1, Enabled: true, NextDue: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}
	if _, claimed, err := store.ClaimNextDueRoutine(now, 30*time.Second); err != nil || !claimed {
		t.Fatalf("claim routine: claimed=%v err=%v", claimed, err)
	}
	wakeAt, found, err := store.NextRoutineWakeAt(30 * time.Second)
	if err != nil {
		t.Fatalf("next wake: %v", err)
	}
	if !found || !wakeAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("wakeAt=%v found=%v, want %v", wakeAt, found, now.Add(30*time.Second))
	}
}
