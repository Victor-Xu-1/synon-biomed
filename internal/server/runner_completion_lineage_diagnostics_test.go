package server

import (
	"strings"
	"testing"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestUnresolvedSessionRunnerArtifactReferencesReturnsStableActionableReferences(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-current", ProjectID: "project-a", Name: "report.md",
		Kind: "markdown", Content: []byte("report"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	missing := "00000000-0000-0000-0000-000000000001"
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	content := strings.Join([]string{
		"{{artifact:" + missing + "}}",
		"{{artifact:" + missing + "}}",
		"{{artifact:" + version.ID + "}}",
	}, " ")
	references, err := (&Server{workspaceStore: store}).unresolvedSessionRunnerArtifactReferences(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}, run, commits, content,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0] != missing {
		t.Fatalf("unresolved references=%#v, want one stable missing reference", references)
	}
}

func TestSessionRunnerReferenceIntegrityErrorIncludesBoundedUnresolvedReferences(t *testing.T) {
	err := (&sessionRunnerReferenceIntegrityError{
		UnresolvedArtifacts:          1,
		UnresolvedArtifactReferences: []string{"00000000-0000-0000-0000-000000000001"},
	}).Error()
	if !strings.Contains(err, "unresolved_artifacts=1") ||
		!strings.Contains(err, "unresolved artifact references") ||
		!strings.Contains(err, "00000000-0000-0000-0000-000000000001") {
		t.Fatalf("integrity error lost actionable reference: %q", err)
	}
}

func TestNormalizeSessionRunnerArtifactReferencesUsesCurrentVersionForArtifactIdentity(t *testing.T) {
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: "report", VersionID: "00000000-0000-0000-0000-000000000001", Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: "report", VersionID: "00000000-0000-0000-0000-000000000002", Relation: transcriptstore.ArtifactRelationProduced},
	}
	content := "[报告]({{artifact:report}}) {{artifact:art_report}} {{artifact:unknown}}"
	got, changed := normalizeSessionRunnerArtifactReferencesToCurrentVersions(content, commits)
	want := "[报告]({{artifact:00000000-0000-0000-0000-000000000002}}) {{artifact:00000000-0000-0000-0000-000000000002}} {{artifact:unknown}}"
	if !changed || got != want {
		t.Fatalf("canonical artifact references=%q changed=%t want=%q", got, changed, want)
	}
}
