package server

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestResearchCitationIdentityDetectsKnownTitlePairedWithWrongDOI(t *testing.T) {
	evidence := []agentruntime.Message{{Role: "tool", Content: `{"ok":true,"result":{"sources":[
		{"title":"Alpha trial results","record":{"title":"Alpha trial results","citation_handle":"doi:10.1000/alpha","citation_text":"Alpha trial results. DOI:10.1000/alpha.","identifiers":[{"namespace":"doi","value":"10.1000/alpha"}]}},
		{"title":"Beta biomarker analysis","record":{"title":"Beta biomarker analysis","citation_handle":"doi:10.1000/beta","citation_text":"Beta biomarker analysis. DOI:10.1000/beta.","identifiers":[{"namespace":"doi","value":"10.1000/beta"}]}}
	]}}`}}
	index := runnerCitationIdentityIndexFromMessages(evidence)
	failures := runnerCitationIdentityFailures(
		"report.md", []byte("1. Beta biomarker analysis. DOI:10.1000/alpha.\n"), index,
	)
	want := []string{"citation_identity_conflict:report.md doi=10.1000/alpha expected_title=Alpha trial results observed_title=Beta biomarker analysis"}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestResearchCitationIdentityFlowsIntoNonBlockingSaveAdvisory(t *testing.T) {
	evidence := []agentruntime.Message{{Role: "tool", Content: `{"ok":true,"result":{"sources":[
		{"record":{"title":"Alpha trial results","identifiers":[{"namespace":"doi","value":"10.1000/alpha"}]}},
		{"record":{"title":"Beta biomarker analysis","identifiers":[{"namespace":"doi","value":"10.1000/beta"}]}}
	]}}`}}
	snapshot, err := os.CreateTemp(t.TempDir(), "report-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := snapshot.WriteString("Beta biomarker analysis. DOI:10.1000/alpha.\n"); err != nil {
		t.Fatal(err)
	}
	validationErr := (&Server{}).validateAgentSavedArtifactEvidenceForPathWithPolicy("report.md", snapshot, evidence, true)
	var evidenceErr *agentSavedArtifactEvidenceError
	if !errors.As(validationErr, &evidenceErr) || len(evidenceErr.References) == 0 ||
		!strings.Contains(strings.Join(evidenceErr.References, "\n"), "citation_identity_conflict:report.md") {
		t.Fatalf("citation identity validation error=%#v", validationErr)
	}
	warning := agentSaveArtifactFailure("report.md", validationErr)
	annotateAgentSavedArtifactEvidenceAdvisory(warning)
	if warning["evidence_status"] != "citation_identity_conflict" || boolValue(warning["blocking"], true) {
		t.Fatalf("save advisory=%#v", warning)
	}
}

func TestResearchCitationIdentityAllowsCorrectPairAndDoesNotJudgeUnknownParaphrase(t *testing.T) {
	evidence := []agentruntime.Message{{Role: "tool", Content: `{"ok":true,"result":{"doi":"10.1000/alpha","title":"Alpha trial results"}}`}}
	index := runnerCitationIdentityIndexFromMessages(evidence)
	for name, content := range map[string]string{
		"correct":            "Alpha trial results. DOI:10.1000/alpha.",
		"unknown paraphrase": "A translated summary. DOI:10.1000/alpha.",
	} {
		t.Run(name, func(t *testing.T) {
			if failures := runnerCitationIdentityFailures("report.md", []byte(content), index); len(failures) != 0 {
				t.Fatalf("non-conflicting citation was judged semantically: %#v", failures)
			}
		})
	}
}
