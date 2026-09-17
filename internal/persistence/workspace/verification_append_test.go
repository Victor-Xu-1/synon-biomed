package workspace

import (
	"path/filepath"
	"testing"
)

func TestAppendVerificationCheckIsIdempotentAndResolvesInPlace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	claim, evidence := "The completion is incomplete", "Required evidence is absent"
	check := VerificationCheck{ID: "review-1", Claim: &claim, Verdict: "fail", Status: "open", Evidence: &evidence}
	if err := store.AppendVerificationCheck("root", check); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendVerificationCheck("root", check); err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 || checks[0].Status != "open" {
		t.Fatalf("checks=%#v error=%v", checks, err)
	}
	if err := store.ResolveVerificationChecks("root", []string{"review-1"}, "Corrected answer passed review"); err != nil {
		t.Fatal(err)
	}
	checks, err = store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 || checks[0].Status != "resolved" || checks[0].Rebuttal == nil {
		t.Fatalf("resolved checks=%#v error=%v", checks, err)
	}
}

func TestListReviewerVerificationChecksScopesOneReviewer(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	reviewerA, reviewerB := "reviewer-a", "reviewer-b"
	for _, reviewerID := range []string{reviewerA, reviewerB} {
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: reviewerID, ProjectID: "project", ParentFrameID: "root",
			AgentName: "REVIEWER", Status: "completed", ConversationType: "delegate",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []VerificationCheck{
		{ID: "check-a", Verdict: "fail", Status: "open", ReviewerFrameID: &reviewerA},
		{ID: "check-b", Verdict: "warn", Status: "open", ReviewerFrameID: &reviewerB},
	} {
		if err := store.AppendVerificationCheck("root", check); err != nil {
			t.Fatal(err)
		}
	}
	checks, err := store.ListReviewerVerificationChecks("root", reviewerA)
	if err != nil || len(checks) != 1 || checks[0].ID != "check-a" {
		t.Fatalf("checks=%#v err=%v", checks, err)
	}
	if _, err := store.ListReviewerVerificationChecks("root", ""); err == nil {
		t.Fatal("empty reviewer frame id was accepted")
	}
}
