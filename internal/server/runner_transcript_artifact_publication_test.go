package server

import (
	"errors"
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestAssistantMessageArtifactReferencesAreScopedToCurrentTurn(t *testing.T) {
	snapshot := transcriptstore.ArtifactCommitSnapshot{
		ThroughAttempt: 5,
		References: []transcriptstore.ArtifactReference{
			{
				RunnerAttempt: 4, ArtifactID: "artifact-prior", VersionID: "version-prior",
				Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			},
			{
				RunnerAttempt: 5, ArtifactID: "artifact-current", VersionID: "version-current",
				Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactAvailable,
			},
			{
				RunnerAttempt: 5, ArtifactID: "artifact-deleted", VersionID: "version-deleted",
				Relation: transcriptstore.ArtifactRelationProduced, Availability: transcriptstore.ArtifactDeleted,
			},
		},
	}

	currentOnly, err := assistantMessageArtifactReferences(snapshot, 5, []byte(`{"text":"31 × 37 = 1147."}`))
	if err != nil {
		t.Fatal(err)
	}
	wantCurrent := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: "artifact-current", VersionID: "version-current", Relation: transcriptstore.ArtifactRelationProduced,
	}}
	if !reflect.DeepEqual(currentOnly, wantCurrent) {
		t.Fatalf("current-turn references=%#v want=%#v", currentOnly, wantCurrent)
	}

	withExplicitPrior, err := assistantMessageArtifactReferences(
		snapshot,
		5,
		[]byte(`{"text":"Use [the earlier report]({{artifact:version-prior}})."}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantExplicit := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: "artifact-prior", VersionID: "version-prior", Relation: transcriptstore.ArtifactRelationCited},
		{ArtifactID: "artifact-current", VersionID: "version-current", Relation: transcriptstore.ArtifactRelationProduced},
	}
	if !reflect.DeepEqual(withExplicitPrior, wantExplicit) {
		t.Fatalf("explicit prior references=%#v want=%#v", withExplicitPrior, wantExplicit)
	}

	if _, err := assistantMessageArtifactReferences(snapshot, 4, []byte(`{"text":"wrong fence"}`)); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("mismatched snapshot fence error=%v", err)
	}
}
