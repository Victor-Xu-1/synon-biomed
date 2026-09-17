package workspace

import (
	"path/filepath"
	"testing"
)

func TestPlanTextDispatchRequiresDecisionAndActivatesAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_plan_approval", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAutoResumeDispatch("frame", "frame", "project", "OPERON", "unrelated_recovery"); err == nil {
		t.Fatal("background recovery bypassed the pending plan decision")
	}
	frame, _, err := store.GetFrame("frame")
	if err != nil || frame.Status != "awaiting_plan_approval" {
		t.Fatalf("rejected recovery changed frame status: %s err=%v", frame.Status, err)
	}
	first, err := store.CreateAutoResumeDispatch("frame", "frame", "project", "OPERON", "plan_text_decision")
	if err != nil || first.Event == nil {
		t.Fatalf("decision dispatch: %v", err)
	}
	frame, _, err = store.GetFrame("frame")
	if err != nil || frame.Status != FrameStatusProcessing {
		t.Fatalf("accepted decision did not activate: %s err=%v", frame.Status, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch(first.Event.ID)
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("activation lacks runnable dispatch: found=%v status=%s err=%v", found, dispatch.Status, err)
	}
	replay, err := store.CreateAutoResumeDispatch("frame", "frame", "project", "OPERON", "plan_text_decision")
	if err != nil || replay.Event == nil || replay.Event.ID != first.Event.ID {
		t.Fatalf("decision replay created competing work: %v", err)
	}
}
