package workspace

import (
	"path/filepath"
	"testing"
)

func TestUpdateFrameRuntimePresentationPreservesOmittedFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "GENERAL", Status: "queued", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", FrameRuntimeMetadata{ContextData: map[string]any{"preserved": true}}); err != nil {
		t.Fatal(err)
	}

	model := "model-a"
	description := "first failure"
	if err := store.UpdateFrameRuntimePresentation("frame", FrameRuntimePresentationInput{Model: &model, StatusDescription: &description}); err != nil {
		t.Fatal(err)
	}
	nextDescription := "second failure"
	if err := store.UpdateFrameRuntimePresentation("frame", FrameRuntimePresentationInput{StatusDescription: &nextDescription}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.GetFrameTraceSnapshot("frame", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root.Model != model || snapshot.Root.StatusDescription != nextDescription || snapshot.Root.ContextData["preserved"] != true {
		t.Fatalf("runtime presentation = %#v", snapshot.Root)
	}
	if err := store.UpdateFrameRuntimePresentation("missing", FrameRuntimePresentationInput{Model: &model}); err == nil {
		t.Fatal("missing frame accepted")
	}
	if err := store.UpdateFrameRuntimePresentation("frame", FrameRuntimePresentationInput{}); err == nil {
		t.Fatal("empty runtime presentation update accepted")
	}
}
