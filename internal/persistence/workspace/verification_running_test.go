package workspace

import (
	"path/filepath"
	"testing"
)

func TestListRunningVerificationIncludesHiddenScientificReviewerProfiles(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	frames := []CreateFrameInput{
		{ID: "legacy-reviewer", ProjectID: "project", ParentFrameID: "root", AgentName: "REVIEWER", Status: "processing", ConversationType: "delegate"},
		{ID: "scientific-reviewer", ProjectID: "project", ParentFrameID: "root", AgentName: "MEDCHEM_EXPERT", Status: "processing", ConversationType: "delegate"},
		{ID: "untrusted-profile", ProjectID: "project", ParentFrameID: "root", AgentName: "MEDCHEM_EXPERT", Status: "processing", ConversationType: "delegate"},
		{ID: "completed-scientific-reviewer", ProjectID: "project", ParentFrameID: "root", AgentName: "OPERON", Status: "completed", ConversationType: "delegate"},
	}
	for _, input := range frames {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	scientificMetadata := map[string]any{
		"_review_target_frame_id": "root",
		"kind":                    "runner_scientific_review",
		"reviewer_profile":        "MEDCHEM_EXPERT",
	}
	if err := store.SetFrameSubmissionMetadata("scientific-reviewer", scientificMetadata, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("untrusted-profile", scientificMetadata, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("completed-scientific-reviewer", scientificMetadata, true); err != nil {
		t.Fatal(err)
	}

	running, err := store.ListRunningVerification("root")
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 {
		t.Fatalf("running verification = %#v", running)
	}
	byID := make(map[string]RunningVerification, len(running))
	for _, item := range running {
		byID[item.FrameID] = item
	}
	for _, frameID := range []string{"legacy-reviewer", "scientific-reviewer"} {
		item, found := byID[frameID]
		if !found || item.TargetFrameID == nil || *item.TargetFrameID != "root" {
			t.Fatalf("running reviewer %q = %#v found=%t", frameID, item, found)
		}
	}
	if _, found := byID["untrusted-profile"]; found {
		t.Fatalf("non-hidden profile metadata was accepted as a running reviewer: %#v", running)
	}
	if _, found := byID["completed-scientific-reviewer"]; found {
		t.Fatalf("completed scientific reviewer remained running: %#v", running)
	}
}
