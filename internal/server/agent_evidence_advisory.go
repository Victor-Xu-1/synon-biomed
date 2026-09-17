package server

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// annotateAgentSavedArtifactEvidenceAdvisory separates observed execution
// facts from scientific interpretation. The validator can report that the
// current logical task did not bind a claim to a retrieved record, but it does
// not erase the authored source identity or declare that source false.
func annotateAgentSavedArtifactEvidenceAdvisory(warning map[string]any) {
	if len(warning) == 0 {
		return
	}
	legacyCode := strings.TrimSpace(stringValue(warning["code"]))
	warning["legacy_code"] = legacyCode
	warning["code"] = "evidence_binding_advisory"
	warning["severity"] = "warning"
	warning["blocking"] = false
	warning["quality_advisory"] = true
	references := agentEvidenceAdvisoryReferences(warning["unsupported_evidence_references"])
	if len(references) > 0 {
		warning["quality_findings"] = references
	}
	fingerprintValues := append([]string{legacyCode, strings.TrimSpace(stringValue(warning["path"]))}, references...)
	digest := sha256.Sum256([]byte("synon.research-quality-advisory.v1\n" + strings.Join(fingerprintValues, "\n")))
	warning["quality_snapshot_id"] = hex.EncodeToString(digest[:])
	conflicts, unbound := []string{}, []string{}
	for _, reference := range references {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(reference)), "citation_identity_conflict:") {
			conflicts = append(conflicts, reference)
		} else {
			unbound = append(unbound, reference)
		}
	}
	grading := map[string]any{
		"source_identity":     "retained_as_authored",
		"source_authenticity": "not_invalidated",
		"retrieval_depth":     "not_established_for_this_binding",
		"source_tier":         "not_assigned",
		"semantic_support":    "not_established_for_this_claim",
		"basis":               "current_logical_task_durable_receipts",
	}
	if legacyCode == "evidence_validation_unavailable" {
		warning["evidence_status"] = "validation_unavailable"
		warning["completion_check_required"] = true
		warning["message"] = "The artifact was saved, but the durable evidence validator was unavailable. Existing sources remain retained; terminal validation must retry this check before publication."
		grading["source_identity"] = "retained_not_revalidated"
		grading["retrieval_depth"] = "temporarily_unavailable"
		grading["semantic_support"] = "not_assessed"
	} else if len(conflicts) > 0 {
		warning["evidence_status"] = "citation_identity_conflict"
		warning["citation_identity_conflicts"] = conflicts
		warning["message"] = "A cited identifier is paired with the exact known title of a different retrieved record. The saved artifact is retained; review this citation identity before final use."
		grading["source_identity"] = "conflicting_record_identity"
		grading["source_authenticity"] = "records_retained_not_invalidated"
		grading["retrieval_depth"] = "record_metadata_observed"
		grading["semantic_support"] = "not_assessed_until_identity_is_resolved"
	} else {
		warning["evidence_status"] = "claim_source_binding_not_established"
		warning["message"] = "The source identity is retained, but this logical task does not establish the cited claim-to-source binding or retrieval depth. This advisory does not mean the source is false."
	}
	if len(unbound) > 0 {
		warning["unbound_evidence_references"] = unbound
	}
	warning["evidence_grading"] = grading
}

func agentEvidenceAdvisoryReferences(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case []string:
		result = append(result, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	}
	result = uniqueSortedFolded(result)
	sort.Strings(result)
	return result
}
