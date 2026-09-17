package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentTaskPlanReferenceIsExactAndFailsClosed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-plan-ref", UserID: "local", Name: "Plan ref"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-plan-ref", ProjectID: "project-plan-ref", AgentName: "OPERON",
		Status: FrameStatusProcessing, ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	_, planVersion, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "plan-ref", ProjectID: "project-plan-ref", Name: "plan_current.json",
		ContentType: "application/json", Content: strings.NewReader(`{"title":"Current"}`),
		CreatedBy: "agent", RootFrameID: "frame-plan-ref", FrameID: "frame-plan-ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "report-ref", ProjectID: "project-plan-ref", Name: "report.json",
		ContentType: "application/json", Content: strings.NewReader(`{"result":true}`),
		CreatedBy: "agent", RootFrameID: "frame-plan-ref", FrameID: "frame-plan-ref",
	}); err != nil {
		t.Fatal(err)
	}
	reference, found, err := store.GetCurrentTaskPlanReference(context.Background(), "frame-plan-ref")
	if err != nil || !found || reference.ArtifactID != "plan-ref" || reference.VersionID != planVersion.ID {
		t.Fatalf("reference=%#v found=%t err=%v", reference, found, err)
	}

	if _, _, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "plan-ref-ambiguous", ProjectID: "project-plan-ref", Name: "plan_other.json",
		ContentType: "application/json", Content: strings.NewReader(`{"title":"Other"}`),
		CreatedBy: "agent", RootFrameID: "frame-plan-ref", FrameID: "frame-plan-ref",
	}); err != nil {
		t.Fatal(err)
	}
	if reference, found, err := store.GetCurrentTaskPlanReference(context.Background(), "frame-plan-ref"); err != nil || found {
		t.Fatalf("ambiguous reference=%#v found=%t err=%v", reference, found, err)
	}
	if _, err := store.db.Exec(`UPDATE artifact_runtime_metadata SET superseded_by_artifact_id='plan-ref' WHERE artifact_id='plan-ref-ambiguous'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{
		FrameID: "frame-plan-ref", Type: "plan_discarded", Payload: map[string]any{"artifact_id": "plan-ref"},
	}); err != nil {
		t.Fatal(err)
	}
	if reference, found, err := store.GetCurrentTaskPlanReference(context.Background(), "frame-plan-ref"); err != nil || found {
		t.Fatalf("discarded reference=%#v found=%t err=%v", reference, found, err)
	}
}
