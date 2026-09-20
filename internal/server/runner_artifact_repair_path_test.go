package server

import (
	"context"
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestCrossArtifactRepairTargetsSavedPathNotDuplicateBasename(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "outputs/evidence.csv",
		"claim,source_type,source\nobservation,paper,publication\n")
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "evidence.csv",
		"claim,source_type,source\nobservation,paper,https://example.test/source\n")
	input := map[string]any{
		"files":    []any{"outputs/evidence.csv", "evidence.csv"},
		"language": "text", "human_description": "Save source tables",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-paths", input), fixture.identity, "save-paths", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{}
	for _, artifact := range agentSaveArtifactResults(t, result) {
		commits = append(commits, transcriptstore.ArtifactReferenceInput{
			ArtifactID: stringValue(artifact["artifact_id"]), VersionID: stringValue(artifact["version_id"]),
			Relation: transcriptstore.ArtifactRelationProduced,
		})
	}
	if len(commits) != 2 || commits[0].ArtifactID == commits[1].ArtifactID {
		t.Fatalf("distinct source paths lost identity: %#v", commits)
	}
	want := []string{"evidence_source_locator_missing:outputs/evidence.csv row=2 source_type=paper"}
	failures, err := fixture.server.validateSessionRunnerCrossArtifactConsistency(
		context.Background(), fixture.stream.ProjectID, commits,
	)
	if err != nil || !reflect.DeepEqual(failures, want) {
		t.Fatalf("repair target=%#v err=%v, want=%#v", failures, err, want)
	}
	repairs := runnerArtifactRepairRequirements(failures[0])
	if len(repairs) != 1 || repairs[0]["artifact_path"] != "outputs/evidence.csv" {
		t.Fatalf("repair contract lost saved path: %#v", repairs)
	}
	// Repairing and saving that precise path replaces the failing version;
	// the unrelated same-named file must neither mask nor inherit its failure.
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "outputs/evidence.csv",
		"claim,source_type,source\nobservation,paper,https://example.test/source\n")
	input["files"] = []any{"outputs/evidence.csv"}
	repaired, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-repaired-path", input), fixture.identity, "save-repaired-path", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifact := agentSaveArtifactResults(t, repaired)[0]
	commits = append(commits, transcriptstore.ArtifactReferenceInput{
		ArtifactID: stringValue(artifact["artifact_id"]), VersionID: stringValue(artifact["version_id"]),
		Relation: transcriptstore.ArtifactRelationProduced,
	})
	failures, err = fixture.server.validateSessionRunnerCrossArtifactConsistency(
		context.Background(), fixture.stream.ProjectID, commits,
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("repaired saved path still rejected: %#v err=%v", failures, err)
	}
}
