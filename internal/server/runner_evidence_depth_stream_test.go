package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerEvidenceDepthStreamsBeyondFormerFileBudget(t *testing.T) {
	for _, shape := range []struct{ name, header, row, tail string }{
		{"typed", "source_type,identifier,notes\n", "trial,,", "trial,NCT01234567,tail\n"},
		{"generic", "identifier,notes\n", ",", "NCT01234567,tail\n"},
		{"wide", "clinical_trial_ids,source_url,notes\n", ",,", "NCT01234567,,tail\n"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			const rows = 8193
			content := shape.header + strings.Repeat(shape.row+strings.Repeat("x", 2048)+"\n", rows) + shape.tail
			if len(content) <= 16<<20 {
				t.Fatal("fixture must exceed former scan budget")
			}
			store := openRunnerArtifactCompletionStore(t)
			artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
				ArtifactID: "depth-stream", ProjectID: "project-a", Name: "evidence.csv", Kind: "text/csv", Content: []byte(content), CreatedBy: "runner",
			})
			if err != nil {
				t.Fatal(err)
			}
			commits := []transcriptstore.ArtifactReferenceInput{{ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced}}
			got, err := (&Server{workspaceStore: store}).validateSessionRunnerEvidenceRecordDepth(context.Background(), "project-a", commits, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got, "\n")
			if len(got) != 2 || !strings.Contains(joined, fmt.Sprintf("evidence.csv row=%d", rows+2)) || !strings.Contains(joined, "evidence_record_depth_required:trial source_type=trial identifier=NCT01234567") {
				t.Fatalf("late evidence row was skipped or its record index lost: %v", got)
			}
		})
	}
}

func TestRunnerEvidenceDepthStreamBoundaries(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.trials["NCT01234567"] = struct{}{}
	input := "\ufeffsource_type\tidentifier\tnotes\ntrial\tNCT01234567\t\"first\nline\"\ntrial\tNCT00000001\ttail\n"
	got, err := scanRunnerEvidenceDepthLedger(context.Background(), strings.NewReader(input), "evidence.tsv", index)
	if err != nil || len(got.failures) != 1 || !strings.Contains(got.failures[0], "row=3") || !got.present["trial"] || !got.verified["trial"] || got.representative["trial"] != "NCT00000001" {
		t.Fatalf("BOM/TSV/row indices/verified class/minimum representative: %+v err=%v", got, err)
	}
	malformed := "source_type,identifier\ntrial,NCT01234567\ntrial,\"unterminated"
	got, err = scanRunnerEvidenceDepthLedger(context.Background(), strings.NewReader(malformed), "evidence.csv", index)
	if err != nil || len(got.failures) != 1 || got.failures[0] != "source_evidence_invalid_delimited:evidence.csv" || len(got.verified) != 0 {
		t.Fatalf("malformed tail must require repair, not claim verification: %+v err=%v", got, err)
	}
	sentinel := errors.New("controlled read failure")
	_, err = scanRunnerEvidenceDepthLedger(context.Background(), io.MultiReader(strings.NewReader("source_type,identifier\ntrial,NCT01234567\n"), sourceEvidenceFailReader{sentinel}), "evidence.csv", index)
	if !errors.Is(err, sentinel) {
		t.Fatalf("lost storage error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = scanRunnerEvidenceDepthLedger(ctx, strings.NewReader(input), "evidence.tsv", index)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	// A non-evidence result table is classified before scanning its body.
	got, err = scanRunnerEvidenceDepthLedger(context.Background(), io.MultiReader(strings.NewReader("a,b\n"), sourceEvidenceFailReader{sentinel}), "results.csv", index)
	if err != nil || len(got.failures) != 0 {
		t.Fatalf("ordinary table scanned as evidence: %+v err=%v", got, err)
	}
}
