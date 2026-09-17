package server

import (
	"reflect"
	"strings"
	"testing"
)

func TestSessionRunnerFatalCrossArtifactFailuresKeepsPresentationMismatchesAdvisoryWhenReviewIsOff(t *testing.T) {
	run := &sessionRunnerChatRun{VerificationExplicitlyDisabled: true}
	failures := []string{
		"numeric_table_mismatch:report.md<->results.csv key=A",
		"numeric_transposed_table_mismatch:results.csv<->report.md entity=A",
		"table_row_identity_mismatch:report.md<->results.csv key_column=id",
		"missing_artifact_reference:missing.csv in report.md",
		"machine_validation_missing_passing_check:validation.json",
	}
	want := []string{
		"missing_artifact_reference:missing.csv in report.md",
		"machine_validation_missing_passing_check:validation.json",
	}
	if got := sessionRunnerFatalCrossArtifactFailures(run, failures); !reflect.DeepEqual(got, want) {
		t.Fatalf("fatal failures=%#v want=%#v", got, want)
	}
}

func TestSessionRunnerFatalCrossArtifactFailuresKeepsLegacyRunsStrict(t *testing.T) {
	failures := []string{"numeric_table_mismatch:report.md<->results.csv key=A"}
	if got := sessionRunnerFatalCrossArtifactFailures(&sessionRunnerChatRun{}, failures); !reflect.DeepEqual(got, failures) {
		t.Fatalf("legacy fatal failures=%#v want=%#v", got, failures)
	}
}

func TestSessionRunnerFatalCrossArtifactFailuresKeepsSourceCoverageAdvisory(t *testing.T) {
	failures := []string{
		"evidence_record_depth_missing:sources.csv row=4 source_type=publication identifier=doi:10.1000/example",
		"evidence_record_depth_required:sources.csv source_type=publication reason=at_least_one_substantive_record_read",
	}
	if got := sessionRunnerFatalCrossArtifactFailures(&sessionRunnerChatRun{}, failures); len(got) != 0 {
		t.Fatalf("source-category heuristic blocked completion: %#v", got)
	}
}

func TestSessionRunnerFatalCrossArtifactFailuresKeepsLedgerProvenanceGapsAdvisory(t *testing.T) {
	failures := []string{
		"source_ledger_source_not_in_durable_receipts:sources.csv row=3 source=doi:10.1000/example",
		"source_claim_missing_attested_excerpt:source_evidence.json source=s1 claim=c1",
		"evidence_record_class_missing:source_type=trial reason=requested_source_class_not_represented",
	}
	if got := sessionRunnerFatalCrossArtifactFailures(&sessionRunnerChatRun{}, failures); len(got) != 0 {
		t.Fatalf("source-category heuristic blocked completion: %#v", got)
	}
}

func TestSessionRunnerFatalCrossArtifactFailuresTreatsExhaustedSourcesAsAdvisory(t *testing.T) {
	failures := []string{
		"evidence_record_depth_unavailable:sources.csv source_type=publication reason=all_advertised_source_routes_exhausted",
		"evidence_record_class_unavailable:source_type=patent reason=all_advertised_source_routes_exhausted",
	}
	if got := sessionRunnerFatalCrossArtifactFailures(&sessionRunnerChatRun{}, failures); len(got) != 0 {
		t.Fatalf("exhausted source limitations remained fatal: %#v", got)
	}
}

func TestSessionRunnerCitationIntegrityFollowsExplicitVerifierDisablement(t *testing.T) {
	if !sessionRunnerCitationIntegrityRequired(nil) {
		t.Fatal("nil run must retain strict legacy behavior")
	}
	if sessionRunnerCitationIntegrityRequired(&sessionRunnerChatRun{VerificationExplicitlyDisabled: true}) {
		t.Fatal("explicitly disabled verifier unexpectedly enabled citation gate")
	}
	if !sessionRunnerCitationIntegrityRequired(&sessionRunnerChatRun{}) {
		t.Fatal("unresolved policy should retain strict behavior")
	}
}

func TestSessionRunnerCorrectionDetailOnlyAdvisoryRequiresZeroBlockingCounts(t *testing.T) {
	detail := "runner completion reference integrity failed (unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=5 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=7 invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): unsupported identifiers doi:10.1000/example; cross-artifact consistency failures numeric_table_mismatch:report.csv row=2"
	if !sessionRunnerCorrectionDetailOnlyAdvisory(detail) {
		t.Fatal("stale presentation-only correction was not recognized")
	}
	if sessionRunnerCorrectionDetailOnlyAdvisory(strings.Replace(detail, "invalid_scientific_artifacts=0", "invalid_scientific_artifacts=1", 1)) {
		t.Fatal("blocking correction was incorrectly treated as stale advisory")
	}
	if !sessionRunnerCorrectionDetailOnlyAdvisory(strings.Replace(detail, "numeric_table_mismatch:", "evidence_record_depth_missing:", 1)) {
		t.Fatal("stale per-row evidence-depth advisory was not recognized")
	}
	if !sessionRunnerCorrectionDetailOnlyAdvisory(strings.Replace(detail, "numeric_table_mismatch:", "evidence_record_depth_required:", 1)) {
		t.Fatal("old source-category requirement would restart the same correction loop")
	}
}

func TestSessionRunnerRecoveredUnsupportedCitationCorrectionIsAdvisory(t *testing.T) {
	detail := "runner completion reference integrity failed (unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=1 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=0 invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): unsupported identifiers doi:10.1000/example"
	if !sessionRunnerRecoveredCorrectionIsAdvisory("artifact_reference_correction_required", detail) {
		t.Fatal("legacy unsupported-citation correction was not retired under the advisory policy")
	}
	if sessionRunnerRecoveredCorrectionIsAdvisory("artifact_reference_correction_required", strings.Replace(detail, "invalid_scientific_artifacts=0", "invalid_scientific_artifacts=1", 1)) {
		t.Fatal("a structural blocker was retired as an evidence advisory")
	}
}

func TestSessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepthRejectsMixedEvidenceFindings(t *testing.T) {
	if !sessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepth(
		"evidence_record_depth_missing:sources.csv row=2, evidence_record_depth_missing:sources.csv row=3",
	) {
		t.Fatal("per-row depth findings were not recognized as advisory")
	}
	if !sessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepth(
		"evidence_record_depth_missing:sources.csv row=2, evidence_record_class_missing:trial",
	) {
		t.Fatal("source coverage diagnostics were not retained as advisory")
	}
	if !sessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepth(
		"evidence_record_depth_unavailable:sources.csv source_type=publication, evidence_record_class_unavailable:source_type=patent",
	) {
		t.Fatal("exhausted source limitations were not recognized as advisory")
	}
}

func TestSessionRunnerRemoveRecoveredCorrectionContextPreservesTranscript(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "system", Content: "base"},
		{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker + ": stale"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "result"},
	}
	got := sessionRunnerRemoveRecoveredCorrectionContext(messages)
	if len(got) != 3 || got[0].Content != "base" || got[1].Role != "user" || got[2].Role != "assistant" {
		t.Fatalf("filtered transcript=%#v", got)
	}
}
