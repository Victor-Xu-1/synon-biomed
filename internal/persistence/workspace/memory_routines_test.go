package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMemorySupersessionAndRoutineLeaseAreDurable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	root, err := store.CreateFrame(CreateFrameInput{ID: "frame-root", ProjectID: project.ID, AgentName: "planner", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	oldMemory, err := store.CreateMemory(CreateMemoryInput{ID: "memory-old", UserID: "user-1", Body: "old decision", Origin: "user", SubjectProjectID: project.ID})
	if err != nil {
		t.Fatalf("create old memory: %v", err)
	}
	newMemory, err := store.CreateMemory(CreateMemoryInput{ID: "memory-new", UserID: "user-1", Body: "new decision", Origin: "user", SubjectProjectID: project.ID})
	if err != nil {
		t.Fatalf("create new memory: %v", err)
	}
	if err := store.SupersedeMemory(oldMemory.ID, newMemory.ID); err != nil {
		t.Fatalf("supersede memory: %v", err)
	}
	active, err := store.ListActiveMemories("user-1", project.ID)
	if err != nil {
		t.Fatalf("list active memories: %v", err)
	}
	if len(active) != 1 || active[0].ID != newMemory.ID {
		t.Fatalf("active memories = %#v", active)
	}

	now := time.Now().UTC().Truncate(time.Second)
	routine, err := store.CreateRoutine(CreateRoutineInput{ID: "routine-1", RootFrameID: root.ID, OwnerUserID: "user-1", Label: "Daily", OnTick: "summarize", EveryMinutes: 60, Enabled: true, NextDue: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}
	claimed, ok, err := store.ClaimNextDueRoutine(now, 30*time.Second)
	if err != nil {
		t.Fatalf("claim routine: %v", err)
	}
	if !ok || claimed.ID != routine.ID || claimed.LockedAt == nil {
		t.Fatalf("claimed routine = %#v, ok=%v", claimed, ok)
	}
	if err := store.CompleteRoutineTick(routine.ID, now, true, "completed"); err != nil {
		t.Fatalf("complete routine tick: %v", err)
	}
	updated, err := store.GetRoutine(routine.ID)
	if err != nil {
		t.Fatalf("get routine: %v", err)
	}
	if updated.TickCount != 1 || updated.LastOKAt == nil || updated.LockedAt != nil || !updated.NextDue.Equal(now.Add(time.Hour)) {
		t.Fatalf("updated routine = %#v", updated)
	}
}
