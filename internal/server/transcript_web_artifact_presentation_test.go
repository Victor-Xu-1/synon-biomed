package server

import (
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptWebPresentationArtifactReferencesCollapsesExactGeneratedCopies(t *testing.T) {
	references := []transcriptstore.TranscriptWebMessageArtifactReference{
		{
			ArtifactID: "artifact-output", VersionID: "version-output",
			Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			Filename: "complex.pdb", ContentType: "chemical/x-pdb", SizeBytes: 128, Checksum: "abc123",
		},
		{
			ArtifactID: "artifact-copy", VersionID: "version-copy",
			Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			Filename: "complex.pdb", ContentType: "chemical/x-pdb", SizeBytes: 128, Checksum: "abc123",
		},
	}
	message := map[string]any{"content": map[string]any{
		"content": "[complex.pdb]({{artifact:version-copy}})",
	}}

	got := transcriptWebPresentationArtifactReferences(references, message)
	if len(got) != 1 || got[0].ArtifactID != "artifact-copy" || got[0].VersionID != "version-copy" {
		t.Fatalf("references=%#v", got)
	}
}

func TestTranscriptWebPresentationArtifactReferencesPreservesDistinctOrCitedFiles(t *testing.T) {
	references := []transcriptstore.TranscriptWebMessageArtifactReference{
		{
			ArtifactID: "artifact-produced", VersionID: "version-produced",
			Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			Filename: "result.csv", ContentType: "text/csv", Checksum: "first",
		},
		{
			ArtifactID: "artifact-distinct", VersionID: "version-distinct",
			Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			Filename: "result.csv", ContentType: "text/csv", Checksum: "second",
		},
		{
			ArtifactID: "artifact-cited", VersionID: "version-cited",
			Relation: transcriptstore.ArtifactRelationCited, Availability: transcriptstore.ArtifactAvailable,
			Filename: "result.csv", ContentType: "text/csv", Checksum: "first",
		},
	}

	got := transcriptWebPresentationArtifactReferences(references, map[string]any{})
	if len(got) != len(references) {
		t.Fatalf("references=%#v", got)
	}
}

func TestSessionRunnerDeliveryCollapsesExactGeneratedCopies(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	first := writeAgentSaveArtifactsFile(t, fixture.projectPath, "output/result.txt", "verified result\n")
	second := writeAgentSaveArtifactsFile(t, fixture.projectPath, "copy/result.txt", "verified result\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-duplicates", 1, first, second)
	input := map[string]any{
		"files": []any{"output/result.txt", "copy/result.txt"},
		"destination": map[string]any{
			"output/result.txt": "snapshot",
			"copy/result.txt":   "snapshot",
		},
		"language": "text", "human_description": "Saving generated structures",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-exact-duplicates", input),
		fixture.identity,
		"save-exact-duplicates",
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 2 {
		t.Fatalf("artifacts=%#v", artifacts)
	}
	refs := []transcriptstore.ArtifactReferenceInput{
		{
			ArtifactID: stringValue(artifacts[0]["artifact_id"]), VersionID: stringValue(artifacts[0]["version_id"]),
			Relation: transcriptstore.ArtifactRelationProduced,
		},
		{
			ArtifactID: stringValue(artifacts[1]["artifact_id"]), VersionID: stringValue(artifacts[1]["version_id"]),
			Relation: transcriptstore.ArtifactRelationProduced,
		},
	}
	preferred := map[string]struct{}{refs[1].VersionID: {}}
	got, err := fixture.server.deduplicateSessionRunnerProducedArtifactReferences(refs, preferred)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != refs[1] {
		t.Fatalf("references=%#v", got)
	}
}
