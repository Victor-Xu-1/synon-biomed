package workspace

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestBuildFrameSessionExportIncludesAllBranchesEventsAndArtifacts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project-1", AgentName: "planner",
		Status: "completed", ConversationType: "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := store.CreateFrame(CreateFrameInput{
		ID: "branch", ProjectID: "project-1", ParentFrameID: root.ID,
		AgentName: "worker", Status: "completed", ConversationType: "branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "result.txt",
		Kind: "text/plain", Content: []byte("result"), CreatedBy: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1005; index++ {
		payload := map[string]any{"index": index}
		if index == 1004 {
			payload["artifactId"] = "artifact-1"
		}
		if _, err := store.AppendFrameEvent(FrameEventInput{
			FrameID: branch.ID, Type: "progress", Payload: payload,
		}); err != nil {
			t.Fatalf("append event %d: %v", index, err)
		}
	}
	exported, err := store.BuildFrameSessionExport(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Frames) != 2 || exported.Frames[0].Frame.ID != root.ID ||
		exported.Frames[1].Frame.ID != branch.ID {
		t.Fatalf("frames = %#v", exported.Frames)
	}
	if got := len(exported.Frames[1].Events); got != 1005 {
		t.Fatalf("export truncated events: got %d want 1005", got)
	}
	if len(exported.Artifacts) != 1 || exported.Artifacts[0].ID != "artifact-1" {
		t.Fatalf("artifacts = %#v", exported.Artifacts)
	}
	if exported.Frames[1].Events[1004].Payload["index"] != float64(1004) {
		t.Fatal(fmt.Sprintf("last event payload = %#v", exported.Frames[1].Events[1004].Payload))
	}
}
