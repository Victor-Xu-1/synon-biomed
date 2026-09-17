package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWorkspaceMemoryContextPoolExcludesContainedProfileAndOtherFrames(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, frameID := range []string{"frame-current", "frame-other"} {
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: frameID, ProjectID: "project-1", AgentName: "GENERAL", Status: FrameStatusProcessing, ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
	}
	category, err := store.CreateMemoryCategory(ctx, "user-1", "Private Assays", "Read only when explicitly requested.", false)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []CreateMemoryInput{
		{ID: "mem_visible", UserID: "user-1", Body: "Visible profile fact", Origin: "user", Evidence: "stated"},
		{ID: "mem_contained", UserID: "user-1", Body: "Contained profile fact", Origin: "user", Evidence: "stated", CategoryID: category.ID},
		{ID: "mem_project", UserID: "user-1", Body: "Project fact", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: "project-1"},
		{ID: "mem_frame_current", UserID: "user-1", Body: "Current frame fact", Origin: "agent_tool", Evidence: "observed", SubjectFrameID: "frame-current"},
		{ID: "mem_frame_other", UserID: "user-1", Body: "Other frame fact", Origin: "agent_tool", Evidence: "observed", SubjectFrameID: "frame-other"},
	}
	for _, input := range inputs {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := store.ListMemorySystemProfileRows(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile) != 1 || profile[0].ID != "mem_visible" {
		t.Fatalf("system profile rows = %#v", profile)
	}
	counts, err := store.CountActiveMemoryEntities(ctx, "user-1", "frame-current")
	if err != nil {
		t.Fatal(err)
	}
	if counts["profile"] != 2 || counts["project:project-1"] != 1 || counts["frame:frame-current"] != 1 {
		t.Fatalf("entity counts = %#v", counts)
	}
	if _, exists := counts["frame:frame-other"]; exists {
		t.Fatalf("other frame leaked into current pool: %#v", counts)
	}
}
