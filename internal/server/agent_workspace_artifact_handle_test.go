package server

import (
	"context"
	workspace "synon-go/internal/persistence/workspace"
	"testing"
)

func TestWorkspaceArtifactHandleResolvesSingleVersionAndPinsAmbiguousRecovery(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	save := func(content string) workspace.ArtifactVersion {
		_, v, err := f.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{ArtifactID: "work-document", ProjectID: f.identity.access.Frame.ProjectID, Name: "plan.json", Kind: "application/json", Content: []byte(content), CreatedBy: f.identity.access.UserID})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1 := save(`{"step":"acquire"}`)
	value, err := f.server.executeAgentWorkspaceFileTool(context.Background(), f.identity, "read_file", map[string]any{"version_id": "work-document"})
	if err != nil || mapValue(value)["source_version_id"] != v1.ID {
		t.Fatalf("single durable artifact handle unavailable: %v %v", value, err)
	}
	v2 := save(`{"step":"analyze"}`)
	value, err = f.server.executeAgentWorkspaceFileTool(context.Background(), f.identity, "read_file", map[string]any{"version_id": "work-document"})
	if err != nil || mapValue(value)["code"] != "artifact_version_required" {
		t.Fatalf("ambiguous handle silently selected mutable head: %v %v", value, err)
	}
	read := mapValue(mapValue(value)["read_with"])
	if read["version_id"] != v2.ID {
		t.Fatal("recovery has no immutable version")
	}
	save(`{"step":"newer"}`)
	value, err = f.server.executeAgentWorkspaceFileTool(context.Background(), f.identity, "read_file", read)
	if err != nil || mapValue(value)["source_version_id"] != v2.ID {
		t.Fatal("recovery followed changing artifact head")
	}
}
