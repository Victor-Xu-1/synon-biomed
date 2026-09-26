package server

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSavedArtifactEvidenceStreamCitationParity(t *testing.T) {
	samples := []string{
		"", "plain text", "DOI:10.1234/abc\n", "DOI:10.1234/abc not found\nDOI:10.1234/abc verified\n",
		"doi,status\n10.1234/abc,not found\n", "pmid,pdb\n12345678,1ABC\n",
		"source,doi,status\n\"multiline\nresult\",10.1234/abc,not found\n",
		"doi,status\n10.1234/abc,valid\n\"bad quote\n",
		"| PMID | PDB |\n| ---- | --- |\n| 12345678 | 1ABC |\n",
		"PMID|PDB\n12345678|1ABC\n\nPMID|PDB\n87654321|2ABC\n",
		"title,extra\nDOI:10.1234/abc,none\n", "doi,status", "\ufeffpmid,pdb\n12345678,1ABC\n",
		"image data:image/png;base64,abcdefgh0123456 DOI:10.1234/abc\n",
		"data:bad\nimage/png;base64,10.1234/abc\n",
		"| pmid | pdb |\r\n|12345678|1ABC|\r\n",
	}
	for _, content := range samples {
		redacted, _ := redactSessionReviewerDataURIs(content)
		positive, negative := map[string]struct{}{}, map[string]struct{}{}
		addSessionRunnerCandidateReferences(positive, negative, redacted)
		actual, _, err := scanAgentSavedCitationCandidates(context.Background(), strings.NewReader(content), "", runnerCitationIdentityIndex{})
		if err != nil || !reflect.DeepEqual(actual.positive, positive) || !reflect.DeepEqual(actual.negative, negative) {
			t.Fatalf("content=%q actual=%#v positive=%#v negative=%#v err=%v", content, actual, positive, negative, err)
		}
	}
}

func TestSavedArtifactEvidenceStreamCancellationAndInvalidEncoding(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "evidence-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("text\n\xff")); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	if err := server.validateAgentSavedArtifactEvidenceStream(context.Background(), "report.md", file, nil, true); !errors.Is(err, errAgentSavedArtifactTextInvalid) {
		t.Fatalf("invalid UTF8: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.validateAgentSavedArtifactEvidenceStream(ctx, "report.md", file, nil, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatalf("reset: %d %v", offset, err)
	}
}

func TestSavedArtifactEvidenceScansBeyondFormerPreviewBound(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "evidence-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	block := strings.Repeat("ordinary result\n", 4096)
	for n := 0; n < 900; n++ {
		if _, err := io.WriteString(file, block); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Server{}).validateAgentSavedArtifactEvidenceForPathWithPolicy("report.md", file, nil, true); err != nil {
		t.Fatalf("ordinary large report: %v", err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "DOI:10.1234/unverified-tail\n"); err != nil {
		t.Fatal(err)
	}
	err = (&Server{}).validateAgentSavedArtifactEvidenceForPathWithPolicy("report.md", file, nil, true)
	var failure *agentSavedArtifactEvidenceError
	if !errors.As(err, &failure) || !strings.Contains(strings.Join(failure.References, "\n"), "unverified-tail") {
		t.Fatalf("tail evidence: %#v", err)
	}
	if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != 0 {
		t.Fatalf("cursor=%d error=%v", position, err)
	}
}
