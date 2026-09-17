package server

import (
	"log"
	"regexp"
	"strconv"
	"strings"
)

func sessionRunnerObserveUnsupportedCitationAdvisories(references []string) {
	if len(references) == 0 {
		return
	}
	// Identifiers remain in the immutable result and save-time warning payload.
	// Log only the bounded count here so completion observability does not turn
	// a missing claim-source binding into a second scientific truth authority.
	log.Printf("runner_completion_advisory_unsupported_citations count=%d", len(references))
}

// sessionRunnerCompletionAdvisoryCrossArtifactFailure identifies a
// presentation-level discrepancy that should be retained for diagnostics but
// must not turn an otherwise complete task into an automatic correction loop.
// The validator remains authoritative for detecting the discrepancy; this
// policy only separates execution-blocking integrity failures from
// non-blocking quality feedback. It is deliberately domain-neutral.
func sessionRunnerCompletionAdvisoryCrossArtifactFailure(failure string) bool {
	failure = strings.ToLower(strings.TrimSpace(failure))
	for _, prefix := range []string{
		"numeric_table_mismatch:",
		"numeric_transposed_table_mismatch:",
		"table_row_identity_mismatch:",
		"artifact_table_contract_value_mismatch:",
		"artifact_table_contract_row_missing:",
		"artifact_table_contract_row_unexpected:",
		// Source labels and inferred coverage are diagnostics, not a proof
		// that a deliverable is structurally broken. They cannot impose a
		// per-category retrieval quota at publication time.
		"evidence_record_depth_missing:",
		"evidence_record_depth_required:",
		"evidence_record_class_missing:",
		"evidence_record_depth_unavailable:",
		"evidence_record_class_unavailable:",
	} {
		if strings.HasPrefix(failure, prefix) {
			return true
		}
	}
	return false
}

// sessionRunnerEvidenceGapCrossArtifactFailure identifies evidence coverage
// findings that describe row-level depth or provenance of a source ledger
// rather than a broken deliverable. Discovery rows can legitimately outnumber
// the small set of records read in depth, and a blocked provider may leave a
// requested source class unavailable. These findings remain observable; task
// completion is not a certification of scientific quality by a category count.
func sessionRunnerEvidenceGapCrossArtifactFailure(failure string) bool {
	failure = strings.ToLower(strings.TrimSpace(failure))
	for _, prefix := range []string{
		"evidence_record_depth_missing:",
		"evidence_record_depth_required:",
		"evidence_record_depth_unavailable:",
		"evidence_record_class_missing:",
		"evidence_record_class_unavailable:",
		"source_ledger_source_not_in_durable_receipts:",
		"source_ledger_missing_attested_excerpt:",
		"source_ledger_direct_quote_not_in_durable_receipts:",
		"source_claim_source_not_in_durable_receipts:",
		"source_claim_missing_attested_excerpt:",
		"source_claim_direct_quote_not_in_durable_receipts:",
	} {
		if strings.HasPrefix(failure, prefix) {
			return true
		}
	}
	return false
}

// sessionRunnerCorrectionDetailOnlyAdvisory recognizes a stale completion
// correction from the pre-policy path. It is safe to discard only when every
// blocking count is present and zero, no blocking marker is present, and all
// remaining findings are known presentation-level diagnostics. Unknown text
// remains fail-closed so a real artifact defect cannot be hidden by recovery.
func sessionRunnerCorrectionDetailOnlyAdvisory(detail string) bool {
	lower := strings.ToLower(strings.TrimSpace(detail))
	if !strings.HasPrefix(lower, "runner completion reference integrity failed") {
		return false
	}
	for _, key := range []string{
		"unresolved_artifacts", "malformed_artifact_references", "invalid_reference_artifacts",
		"invalid_scientific_artifacts", "invalid_research_artifacts", "missing_local_artifacts",
		"missing_required_deliverables",
	} {
		match := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(key) + `=([0-9]+)`).FindStringSubmatch(lower)
		if len(match) != 2 || strings.TrimLeft(match[1], "0") != "" {
			return false
		}
	}
	for _, marker := range []string{
		"missing_artifact_reference:", "machine_validation_", "malformed_artifact_placeholder:",
		"authoritative_artifact_reference_conflict:", "environment_claim_conflict:",
		"rank_score_pair_missing:",
		"artifact_publication_stale_after_later_work",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	// Older deployments emitted every unselected evidence-ledger row as a
	// blocking correction. Current validation keeps those rows observable but
	// advisory, including aggregate source-category findings. Resume must
	// apply the same policy or a task
	// rejected before the migration can loop forever on a finding that a fresh
	// attempt would no longer reject.
	if strings.Contains(lower, "evidence_") && !sessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepth(lower) {
		return false
	}
	separator := strings.Index(lower, "):")
	if separator < 0 {
		return false
	}
	body := strings.TrimSpace(lower[separator+2:])
	if body == "" {
		return true
	}
	for _, segment := range strings.Split(body, ";") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "unsupported identifiers") ||
			strings.HasPrefix(segment, "cross-artifact consistency failures") {
			continue
		}
		if sessionRunnerCompletionAdvisoryCrossArtifactFailure(segment) {
			continue
		}
		return false
	}
	return true
}

func sessionRunnerRecoveredCorrectionIsAdvisory(reasonCode, detail string) bool {
	return strings.TrimSpace(reasonCode) == "artifact_reference_correction_required" &&
		sessionRunnerCorrectionDetailOnlyAdvisory(detail)
}

// sessionRunnerCorrectionOnlyHasPublicationFreshnessFailures recognizes the
// exact old-contract correction that became obsolete when narrative freshness
// was scoped to one user execution unit. It fails closed on unknown text or any
// other blocker so an upgrade cannot erase an unresolved artifact defect.
func sessionRunnerCorrectionOnlyHasPublicationFreshnessFailures(detail string) bool {
	lower := strings.ToLower(strings.TrimSpace(detail))
	if !strings.HasPrefix(lower, "runner completion reference integrity failed") {
		return false
	}
	for _, key := range []string{
		"unresolved_artifacts", "malformed_artifact_references", "unsupported_citations",
		"invalid_reference_artifacts", "invalid_scientific_artifacts", "invalid_research_artifacts",
		"missing_local_artifacts", "missing_required_deliverables",
	} {
		match := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(key) + `=([0-9]+)`).FindStringSubmatch(lower)
		if len(match) != 2 || strings.TrimLeft(match[1], "0") != "" {
			return false
		}
	}
	crossMatch := regexp.MustCompile(`(?i)cross_artifact_failures=([0-9]+)`).FindStringSubmatch(lower)
	if len(crossMatch) != 2 {
		return false
	}
	crossCount, err := strconv.Atoi(crossMatch[1])
	if err != nil || crossCount <= 0 || crossCount > 32 {
		return false
	}
	const diagnosticPrefix = "cross-artifact consistency failures "
	start := strings.Index(lower, diagnosticPrefix)
	if start < 0 {
		return false
	}
	body := strings.TrimSpace(lower[start+len(diagnosticPrefix):])
	marker := sessionRunnerArtifactPublicationStaleMarker + ":"
	publicationFailure := regexp.QuoteMeta(marker) + `[^,;\r\n]+ later_tool=[a-z0-9_.:-]+`
	allPublicationFailures := regexp.MustCompile(`^` + publicationFailure + `(?:, ` + publicationFailure + `)*$`)
	return strings.Count(body, marker) == crossCount && allPublicationFailures.MatchString(body)
}

func sessionRunnerCorrectionOnlyHasAdvisoryEvidenceDepth(detail string) bool {
	detail = strings.ToLower(strings.TrimSpace(detail))
	prefixes := []string{
		"evidence_record_depth_missing:",
		"evidence_record_depth_required:",
		"evidence_record_class_missing:",
		"evidence_record_depth_unavailable:",
		"evidence_record_class_unavailable:",
		"source_ledger_source_not_in_durable_receipts:",
		"source_ledger_missing_attested_excerpt:",
		"source_ledger_direct_quote_not_in_durable_receipts:",
		"source_claim_source_not_in_durable_receipts:",
		"source_claim_missing_attested_excerpt:",
		"source_claim_direct_quote_not_in_durable_receipts:",
	}
	found := false
	for searchFrom := 0; searchFrom < len(detail); {
		relative := -1
		for _, prefix := range prefixes {
			if candidate := strings.Index(detail[searchFrom:], prefix); candidate >= 0 && (relative < 0 || candidate < relative) {
				relative = candidate
			}
		}
		if relative < 0 {
			break
		}
		index := searchFrom + relative
		matched := ""
		for _, prefix := range prefixes {
			if strings.HasPrefix(detail[index:], prefix) {
				matched = prefix
				break
			}
		}
		if matched == "" {
			return false
		}
		found = true
		searchFrom = index + len(matched)
	}
	return found
}

// sessionRunnerRemoveRecoveredCorrectionContext removes only the synthetic
// prior-unit correction message. The durable transcript and all user/model
// messages remain untouched, so clearing a stale advisory cannot erase work.
func sessionRunnerRemoveRecoveredCorrectionContext(messages []chatCompletionMessage) []chatCompletionMessage {
	filtered := make([]chatCompletionMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

// sessionRunnerFatalCrossArtifactFailures applies the completion policy to
// the current run. When verification is explicitly disabled, presentation
// mismatches are observable diagnostics and do not block publication. All
// other failures, and all unresolved/contract/scientific checks outside this
// helper, remain fail-closed. A nil or legacy run intentionally keeps the
// stricter behavior so test and migration contexts cannot silently weaken
// their authority boundary.
func sessionRunnerFatalCrossArtifactFailures(run *sessionRunnerChatRun, failures []string) []string {
	if len(failures) == 0 {
		return nil
	}
	fatal := make([]string, 0, len(failures))
	advisory := make([]string, 0)
	for _, failure := range failures {
		isAdvisory := sessionRunnerCompletionAdvisoryCrossArtifactFailure(failure)
		evidenceGap := sessionRunnerEvidenceGapCrossArtifactFailure(failure)
		lowerFailure := strings.ToLower(strings.TrimSpace(failure))
		perRowDepthAdvisory := strings.HasPrefix(lowerFailure, "evidence_record_depth_missing:")
		sourceUnavailableAdvisory := strings.HasPrefix(lowerFailure, "evidence_record_depth_unavailable:") ||
			strings.HasPrefix(lowerFailure, "evidence_record_class_unavailable:")
		if run != nil &&
			(evidenceGap || (isAdvisory && (perRowDepthAdvisory || sourceUnavailableAdvisory || run.VerificationExplicitlyDisabled))) {
			advisory = append(advisory, failure)
			continue
		}
		fatal = append(fatal, failure)
	}
	if len(advisory) > 0 {
		// Keep the discrepancy visible to operators without creating another
		// transcript event or exposing internal validation language to users.
		log.Printf("runner_completion_advisory_cross_artifact_failures count=%d", len(advisory))
	}
	return fatal
}
