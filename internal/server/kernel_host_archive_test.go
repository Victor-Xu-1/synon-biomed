package server

import (
	"context"
	"reflect"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestReviewerArchiveHostMethodsAreRootBoundAndReadOnly(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-save", Type: "message", Payload: map[string]any{
			"role": "assistant", "text": "selected score -7.2 from the durable result",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateCompactionArchive(workspace.CreateCompactionArchiveInput{
		ID: "review-archive", FrameID: "frame-save", MessageCount: 1,
		Summary: "Earlier decision selected compound A.", Messages: []map[string]any{{"role": "assistant", "text": "compound A"}},
	}); err != nil {
		t.Fatal(err)
	}
	scope := &sessionReviewerEvidenceScope{sessionID: "frame-save"}
	ctx := withSessionReviewerEvidenceScope(context.Background(), scope)
	search, err := fixture.server.handleKernelArchiveHostCall(
		ctx, fixture.identity.access, "host.archive.search", []any{"score -7.2"}, map[string]any{"limit": float64(4)},
	)
	if err != nil || numberValue(mapValue(search)["count"]) != 1 {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	page, err := fixture.server.handleKernelArchiveHostCall(
		ctx, fixture.identity.access, "host.archive.page", []any{float64(0)}, map[string]any{"offset": float64(0), "limit": float64(10)},
	)
	if err != nil || len(anySliceValue(mapValue(page)["messages"])) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	policy := fixture.server.agentKernelHostCallPolicy(ctx, fixture.identity.access, fixture.projectPath, []string{"repl", "read_file"}, false)
	want := []string{"host.archive.search", "host.archive.page", "host.artifact_path"}
	if !reflect.DeepEqual(policy.AllowedMethods, want) {
		t.Fatalf("reviewer host methods=%#v want=%#v", policy.AllowedMethods, want)
	}
}
