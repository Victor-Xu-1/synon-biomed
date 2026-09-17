package sourcecitation

import (
	"strings"
	"testing"
)

func TestBuildKeepsIdentifierAndMetadataIdentityTogether(t *testing.T) {
	citation := Build(Input{
		DOI: "10.1000/PRIMARY", PMID: "123", Authors: "A Author", Title: "Primary article",
		Journal: "Evidence Journal", Published: "2026-05-18",
	})
	if citation.Handle != "doi:10.1000/primary" ||
		citation.Text != "A Author. Primary article. Evidence Journal. 2026-05-18. DOI:10.1000/primary." {
		t.Fatalf("citation=%#v", citation)
	}
}

func TestBuildDoesNotCanonicalizeInvalidIdentifierAndBoundsPresentation(t *testing.T) {
	citation := Build(Input{DOI: "not-a-doi", Title: strings.Repeat("Long title ", 1000)})
	if citation.Handle != "" || strings.Contains(citation.Text, "not-a-doi") {
		t.Fatalf("invalid source identifier became a citation identity: %#v", citation)
	}
	if len([]rune(citation.Text)) > MaxCitationTextRunes {
		t.Fatalf("citation presentation is unbounded: %d", len([]rune(citation.Text)))
	}
}
