package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestConversationArtifactReferenceBatchReturnsOnlyExactOwnedVersions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(CreateCompatibilityProjectInput{
		ID: "project-batch", UserID: "owner-batch", Name: "Batch project",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedCompatibilityProjectArtifacts(t, store, "project-batch", "root-batch", 10_000)

	refs := []CompatibilityArtifactVersionReference{
		{ArtifactID: "artifact-000000", VersionID: "version-000000"},
		{ArtifactID: "artifact-005000", VersionID: "version-005000"},
		{ArtifactID: "artifact-009999", VersionID: "version-009999"},
	}
	artifacts, err := store.ListCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "owner-batch", "project-batch", "root-batch", refs,
	)
	if err != nil {
		t.Fatalf("resolve exact versions: %v", err)
	}
	if len(artifacts) != len(refs) {
		t.Fatalf("artifacts=%d want=%d", len(artifacts), len(refs))
	}
	for index, artifact := range artifacts {
		if artifact.ID != refs[index].ArtifactID || artifact.VersionID != refs[index].VersionID {
			t.Fatalf("artifact[%d]=%s/%s", index, artifact.ID, artifact.VersionID)
		}
		if artifact.RetentionMode != "snapshot" {
			t.Fatalf("artifact[%d] retention=%q", index, artifact.RetentionMode)
		}
	}
	byVersionIDs, err := store.ListCompatibilityConversationArtifactVersionsByVersionIDs(
		context.Background(), "owner-batch", "project-batch", "root-batch",
		[]string{"version-009999", "version-000000", "version-009999"},
	)
	if err != nil {
		t.Fatalf("resolve exact version ids: %v", err)
	}
	if len(byVersionIDs) != 2 ||
		byVersionIDs[0].ID != "artifact-009999" || byVersionIDs[0].VersionID != "version-009999" ||
		byVersionIDs[1].ID != "artifact-000000" || byVersionIDs[1].VersionID != "version-000000" {
		t.Fatalf("version id artifacts=%#v", byVersionIDs)
	}

	if _, err := store.ListCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "foreign-owner", "project-batch", "root-batch", refs[:1],
	); err == nil {
		t.Fatal("foreign owner resolved artifact metadata")
	}
	if _, err := store.ListCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "owner-batch", "project-batch", "foreign-root", refs[:1],
	); err == nil {
		t.Fatal("foreign conversation resolved artifact metadata")
	}
	if _, err := store.ListCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "owner-batch", "project-batch", "root-batch",
		[]CompatibilityArtifactVersionReference{{ArtifactID: "artifact-000000", VersionID: "version-000001"}},
	); err == nil {
		t.Fatal("mismatched artifact/version pair resolved")
	}
	if _, err := store.ListCompatibilityConversationArtifactVersionsByVersionIDs(
		context.Background(), "foreign-owner", "project-batch", "root-batch", []string{"version-000000"},
	); err == nil {
		t.Fatal("foreign owner resolved artifact version id")
	}
	if _, err := store.ListCompatibilityConversationArtifactVersionsByVersionIDs(
		context.Background(), "owner-batch", "project-batch", "foreign-root", []string{"version-000000"},
	); err == nil {
		t.Fatal("foreign conversation resolved artifact version id")
	}
	if _, err := store.ListCompatibilityConversationArtifactVersionsByVersionIDs(
		context.Background(), "owner-batch", "project-batch", "root-batch", []string{"version-missing"},
	); err == nil {
		t.Fatal("missing artifact version id resolved")
	}
	available, err := store.ListAvailableCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "owner-batch", "project-batch", "root-batch",
		[]CompatibilityArtifactVersionReference{
			refs[0],
			{ArtifactID: "artifact-missing", VersionID: "version-missing"},
			{ArtifactID: "artifact-005000", VersionID: "version-005000"},
		},
	)
	if err != nil || len(available) != 2 || available[0].VersionID != refs[0].VersionID ||
		available[1].VersionID != "version-005000" {
		t.Fatalf("available reference subset=%#v err=%v", available, err)
	}
	foreignAvailable, err := store.ListAvailableCompatibilityConversationArtifactVersionsByReferences(
		context.Background(), "foreign-owner", "project-batch", "root-batch", refs[:1],
	)
	if err != nil || len(foreignAvailable) != 0 {
		t.Fatalf("foreign available subset=%#v err=%v", foreignAvailable, err)
	}
	versionAvailable, err := store.ListAvailableCompatibilityConversationArtifactVersionsByVersionIDs(
		context.Background(), "owner-batch", "project-batch", "root-batch",
		[]string{"version-missing", "version-009999"},
	)
	if err != nil || len(versionAvailable) != 1 || versionAvailable[0].VersionID != "version-009999" {
		t.Fatalf("available version subset=%#v err=%v", versionAvailable, err)
	}
}
