package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCrossArtifactStreamingChecksLargeDocumentTail(t *testing.T) {
	content := strings.Repeat("ordinary report\n", (16<<20)/16+1) + "outputs/absent.csv\n"
	got := validateLargeCrossArtifactFixture(t, "report.md", content, "")
	if len(got) != 1 || got[0] != "missing_artifact_reference:absent.csv in report.md" {
		t.Fatalf("large reference tail was skipped: %v", got)
	}
}

func TestCrossArtifactStreamingChecksLargeTablePair(t *testing.T) {
	var content strings.Builder
	content.WriteString("entity,value,notes\n")
	for row := 0; row < 8200; row++ {
		fmt.Fprintf(&content, "entity%d,%d,%s\n", row, row, strings.Repeat("x", 2048))
	}
	got := validateLargeCrossArtifactFixture(t, "measurements.csv", content.String(), "|entity|value|\n|---|---|\n|entity8199|999|\n")
	if len(got) != 1 || !strings.Contains(got[0], "numeric_table_mismatch:measurements.csv<->final_response.md key=entity8199 column=value values=8199|999") {
		t.Fatalf("large table comparison was skipped: %v", got)
	}
}

func TestCrossArtifactStreamingChecksLargeRankTable(t *testing.T) {
	var content strings.Builder
	content.WriteString("entity,score_neutral,rank_neutral,notes\n")
	for row := 0; row < 8200; row++ {
		rank := row + 1
		if row == 8199 {
			rank = 1
		}
		fmt.Fprintf(&content, "entity%d,%d,%d,%s\n", row, 8200-row, rank, strings.Repeat("x", 2048))
	}
	got := validateLargeCrossArtifactFixture(t, "rankings.csv", content.String(), "")
	if len(got) != 1 || !strings.Contains(got[0], "rank_score_mismatch:rankings.csv key=entity8199") || !strings.Contains(got[0], "expected_rank=8200") {
		t.Fatalf("large rank consistency was skipped: %v", got)
	}
}

func TestCrossArtifactStreamingChecksLargeValidationDocument(t *testing.T) {
	content := `{"metadata":[` + strings.Repeat(`"`+strings.Repeat("x", 2048)+`",`, 8200) + `"last"],"passed":true,"status":"failed"}`
	got := validateLargeCrossArtifactFixture(t, "validation.json", content, "")
	if len(got) != 1 || got[0] != "machine_validation_failed_status:validation.json path=status value=failed" {
		t.Fatalf("large machine validation was skipped: %v", got)
	}
}

func TestCrossArtifactStreamingChecksLargeProvenanceLedger(t *testing.T) {
	row := "source,literature,claim,https://example.test/source," + strings.Repeat("x", 2048) + "\n"
	content := "identifier,source_type,claim,source_url,notes\n" + strings.Repeat(row, 8200) + "tail,literature,claim,,missing\n"
	got := validateLargeCrossArtifactFixture(t, "sources.csv", content, "")
	if len(got) != 1 || got[0] != "evidence_source_locator_missing:sources.csv row=8202 source_type=literature" {
		t.Fatalf("large provenance tail was skipped: %v", got)
	}
}

func validateLargeCrossArtifactFixture(t *testing.T, name, content, final string) []string {
	t.Helper()
	if len(content) <= 16<<20 {
		t.Fatal("fixture must exceed former scan size")
	}
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersionFromReader(context.Background(), workspace.SaveArtifactVersionReaderInput{ArtifactID: "cross-stream", ProjectID: "project-a", Name: name, Kind: "text/plain", Content: strings.NewReader(content), MaxBytes: int64(len(content)), CreatedBy: "runner"})
	if err != nil {
		t.Fatal(err)
	}
	if version.StoragePath == "" {
		t.Fatal("large fixture must exercise the canonical blob reader")
	}
	got, err := (&Server{workspaceStore: store}).validateSessionRunnerCrossArtifactConsistency(context.Background(), "project-a", []transcriptstore.ArtifactReferenceInput{{ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced}}, final)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
