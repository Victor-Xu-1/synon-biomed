package server

import "testing"

func TestResearchQualityAdvisoryFingerprintIsStableAndClassifiesCitationConflict(t *testing.T) {
	first := map[string]any{
		"code": "unsupported_evidence_references",
		"unsupported_evidence_references": []string{
			"source_ledger_source_not_in_durable_receipts:evidence.csv row=2 source=doi:10.1000/a",
			"citation_identity_conflict:report.md doi=10.1000/a expected_title=A observed_title=B",
		},
	}
	second := map[string]any{
		"code": "unsupported_evidence_references",
		"unsupported_evidence_references": []string{
			"citation_identity_conflict:report.md doi=10.1000/a expected_title=A observed_title=B",
			"source_ledger_source_not_in_durable_receipts:evidence.csv row=2 source=doi:10.1000/a",
		},
	}
	annotateAgentSavedArtifactEvidenceAdvisory(first)
	annotateAgentSavedArtifactEvidenceAdvisory(second)
	if first["quality_snapshot_id"] == "" || first["quality_snapshot_id"] != second["quality_snapshot_id"] {
		t.Fatalf("unstable advisory fingerprints: %#v %#v", first, second)
	}
	if first["evidence_status"] != "citation_identity_conflict" ||
		first["message"] != "A cited identifier is paired with the exact known title of a different retrieved record. The saved artifact is retained; review this citation identity before final use." {
		t.Fatalf("citation conflict was relabeled as an ordinary missing binding: %#v", first)
	}
	if boolValue(first["blocking"], true) || !boolValue(first["quality_advisory"], false) {
		t.Fatalf("quality advisory changed save semantics: %#v", first)
	}
}

func TestResearchQualityAdvisoryKeepsValidationOutageNonBlockingAndDistinct(t *testing.T) {
	warning := map[string]any{"code": "evidence_validation_unavailable", "path": "report.md"}
	annotateAgentSavedArtifactEvidenceAdvisory(warning)
	if warning["code"] != "evidence_binding_advisory" || warning["legacy_code"] != "evidence_validation_unavailable" ||
		warning["evidence_status"] != "validation_unavailable" || warning["quality_snapshot_id"] == "" ||
		boolValue(warning["blocking"], true) || !boolValue(warning["completion_check_required"], false) {
		t.Fatalf("validation outage advisory=%#v", warning)
	}
}
