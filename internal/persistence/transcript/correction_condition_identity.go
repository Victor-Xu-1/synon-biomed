package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Fingerprint excludes diagnostic ordering/duplicates and a review's summary
// when its typed issues exist. Exact targets, claims, versions, evidence and
// message coordinates remain semantic. Only outer whitespace is irrelevant;
// internal whitespace/case in paths, quotes and scientific identifiers is not.
// Text-only conditions have no asserted typed semantics and retain exact text.
func (condition RunnerCorrectionCondition) Fingerprint() string {
	semantic := condition
	if condition.Reference != nil {
		reference := *condition.Reference
		reference.UnresolvedArtifactReferences = conditionStringSet(reference.UnresolvedArtifactReferences)
		reference.MalformedArtifactReferences = conditionStringSet(reference.MalformedArtifactReferences)
		reference.UnsupportedCitations = conditionStringSet(reference.UnsupportedCitations)
		reference.InvalidReferenceArtifacts = conditionStringSet(reference.InvalidReferenceArtifacts)
		reference.InvalidScientificArtifacts = conditionStringSet(reference.InvalidScientificArtifacts)
		reference.CrossArtifactFailures = conditionStringSet(reference.CrossArtifactFailures)
		reference.InvalidResearchArtifacts = conditionStringSet(reference.InvalidResearchArtifacts)
		reference.MissingLocalArtifacts = conditionStringSet(reference.MissingLocalArtifacts)
		reference.MissingRequiredDeliverables = conditionStringSet(reference.MissingRequiredDeliverables)
		semantic.Reference = &reference
	}
	if condition.Review != nil {
		review := RunnerReviewCondition{Summary: strings.TrimSpace(condition.Review.Summary)}
		if len(condition.Review.Issues) > 0 {
			review.Summary = ""
		}
		normalizedIssues := make([]RunnerReviewConditionIssue, 0, len(condition.Review.Issues))
		for _, issue := range condition.Review.Issues {
			issue.Claim = strings.TrimSpace(issue.Claim)
			issue.Verdict = strings.ToLower(strings.TrimSpace(issue.Verdict))
			issue.Severity = strings.ToLower(strings.TrimSpace(issue.Severity))
			issue.Evidence = strings.TrimSpace(issue.Evidence)
			issue.EvidenceQuote = strings.TrimSpace(issue.EvidenceQuote)
			issue.ArtifactVersionID = strings.TrimSpace(issue.ArtifactVersionID)
			issue.EvidenceRefs = conditionStringSet(issue.EvidenceRefs)
			normalizedIssues = append(normalizedIssues, issue)
		}
		review.Issues = conditionRecordSet(normalizedIssues)
		semantic.Review = &review
	}
	if condition.Plan != nil {
		plan := *condition.Plan
		plan.Steps = conditionRecordSet(plan.Steps)
		semantic.Plan = &plan
	}
	if condition.Visual != nil {
		visual := *condition.Visual
		visual.Artifacts = conditionRecordSet(visual.Artifacts)
		visual.UnboundNames = conditionStringSet(visual.UnboundNames)
		semantic.Visual = &visual
	}
	if condition.Text != nil {
		semantic.Text = &RunnerTextCondition{Detail: strings.TrimSpace(condition.Text.Detail)}
	}
	encoded, _ := json.Marshal(semantic)
	return correctionContentDigest(encoded)
}

// ContentID binds a read to the exact persisted condition, including its
// presentation fields and original diagnostic ordering.
func (condition RunnerCorrectionCondition) ContentID() string {
	encoded, _ := json.Marshal(condition)
	return correctionContentDigest(encoded)
}

func conditionStringSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[strings.TrimSpace(value)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// These closed records contain only JSON-supported scalars/slices. Their
// marshaling cannot fail or invoke an external/custom serializer.
func conditionRecordSet[T RunnerReviewConditionIssue | RunnerPlanConditionStep | RunnerVisualConditionArtifact](values []T) []T {
	set := make(map[string]T, len(values))
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		set[string(encoded)] = value
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]T, 0, len(keys))
	for _, key := range keys {
		result = append(result, set[key])
	}
	return result
}

func correctionContentDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
