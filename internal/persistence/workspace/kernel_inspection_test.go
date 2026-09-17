package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestKernelInspectionArtifactFiltersAndDependencyOwnership(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, project := range []CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "A"},
		{ID: "project-b", UserID: "owner-a", Name: "B"},
		{ID: "project-foreign", UserID: "owner-b", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateFrame(CreateFrameInput{ID: "root-" + project.ID, ProjectID: project.ID, AgentName: "OPERON", Status: "completed", ConversationType: "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	write := func(owner, project, id, name, content, key string) ArtifactVersion {
		t.Helper()
		_, version, err := store.WriteArtifactVersionRealtime(WithMutationIdempotencyKey(context.Background(), key), WriteArtifactVersionInput{ArtifactID: id, ProjectID: project, Name: name, ContentType: "text/plain", Content: strings.NewReader(content), RootFrameID: "root-" + project, FrameID: "root-" + project, IsUserUpload: true}, owner)
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	input := write("owner-a", "project-a", "input-a", "input-data.txt", "needle crosses content", "inspect-input")
	output := write("owner-a", "project-b", "output-b", "result-report.txt", "derived output", "inspect-output")
	foreign := write("owner-b", "project-foreign", "foreign", "foreign.txt", "private", "inspect-foreign")

	dependency, err := store.RecordArtifactVersionDependency(context.Background(), output.ID, input.ID, "source")
	if err != nil || dependency.VersionID != output.ID || dependency.DependsOnVersionID != input.ID {
		t.Fatalf("dependency=%#v err=%v", dependency, err)
	}
	if _, err := store.RecordArtifactVersionDependency(context.Background(), output.ID, foreign.ID, "must-fail"); err == nil || !strings.Contains(err.Error(), "different owners") {
		t.Fatalf("cross-owner dependency error=%v", err)
	}

	all, count, err := store.ListKernelArtifacts(context.Background(), "owner-a", "project-a", KernelArtifactBrowseOptions{ProjectID: "all", Limit: 20})
	if err != nil || count != 2 || len(all) != 2 {
		t.Fatalf("all owner artifacts=%#v count=%d err=%v", all, count, err)
	}
	content, count, err := store.ListKernelArtifacts(context.Background(), "owner-a", "project-a", KernelArtifactBrowseOptions{Content: "crosses content", Limit: 20})
	if err != nil || count != 1 || len(content) != 1 || content[0].ID != "input-a" {
		t.Fatalf("content artifacts=%#v count=%d err=%v", content, count, err)
	}
	search, count, err := store.ListKernelArtifacts(context.Background(), "owner-a", "project-a", KernelArtifactBrowseOptions{ProjectID: "all", Search: "result report", Limit: 20})
	if err != nil || count != 1 || len(search) != 1 || search[0].ID != "output-b" || search[0].Score == nil {
		t.Fatalf("search artifacts=%#v count=%d err=%v", search, count, err)
	}
	dependencies, err := store.ListArtifactVersionDependencies(context.Background(), output.ID, "up")
	if err != nil || len(dependencies) != 1 || dependencies[0].DependsOnVersionID != input.ID {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
}
