package server

import (
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSelectSessionRunnerActiveArtifactReferencesDropsSupersededContinuationOutputs(t *testing.T) {
	ref := func(artifactID, versionID string) transcriptstore.ArtifactReferenceInput {
		return transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifactID, VersionID: versionID, Relation: transcriptstore.ArtifactRelationProduced,
		}
	}
	oldTable := ref("old-table", "old-table-v1")
	unchangedReport := ref("report", "report-v3")
	newTable := ref("new-table", "new-table-v2")
	commits := []transcriptstore.ArtifactReferenceInput{oldTable, unchangedReport, newTable}

	got := selectSessionRunnerActiveArtifactReferences(
		commits,
		map[string]struct{}{newTable.VersionID: {}},
		map[string]struct{}{unchangedReport.VersionID: {}},
		true,
		true,
	)
	want := []transcriptstore.ArtifactReferenceInput{unchangedReport, newTable}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("active refs=%#v, want %#v", got, want)
	}

	if independent := selectSessionRunnerActiveArtifactReferences(commits, nil, nil, false, false); len(independent) != 0 {
		t.Fatalf("independent no-output turn inherited refs=%#v", independent)
	}

	if continuation := selectSessionRunnerActiveArtifactReferences(commits, nil, nil, false, true); !reflect.DeepEqual(continuation, commits) {
		t.Fatalf("continuation no-signal refs=%#v, want full logical-task ledger", continuation)
	}
}
