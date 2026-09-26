package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const RunnerCorrectionConditionSchema = "synon.runner_correction_condition.v1"

// RunnerCorrectionCondition is the complete host-produced obligation. Detail in
// RunnerInterruptionCause is only a bounded display; it is never this identity.
// Exactly one typed body is present. Original ordering and evidence are retained
// in storage; the separately defined semantic fingerprint normalizes only sets.
type RunnerCorrectionCondition struct {
	Schema     string                    `json:"schema"`
	ReasonCode string                    `json:"reason_code"`
	Reference  *RunnerReferenceCondition `json:"reference,omitempty"`
	Review     *RunnerReviewCondition    `json:"review,omitempty"`
	Plan       *RunnerPlanCondition      `json:"plan,omitempty"`
	Visual     *RunnerVisualCondition    `json:"visual,omitempty"`
	Text       *RunnerTextCondition      `json:"text,omitempty"`
}

type RunnerReferenceCondition struct {
	UnresolvedArtifacts          int      `json:"unresolved_artifacts"`
	UnresolvedArtifactReferences []string `json:"unresolved_artifact_references"`
	MalformedArtifactReferences  []string `json:"malformed_artifact_references"`
	UnsupportedCitations         []string `json:"unsupported_citations"`
	InvalidReferenceArtifacts    []string `json:"invalid_reference_artifacts"`
	InvalidScientificArtifacts   []string `json:"invalid_scientific_artifacts"`
	CrossArtifactFailures        []string `json:"cross_artifact_failures"`
	InvalidResearchArtifacts     []string `json:"invalid_research_artifacts"`
	MissingLocalArtifacts        []string `json:"missing_local_artifacts"`
	MissingRequiredDeliverables  []string `json:"missing_required_deliverables"`
}

type RunnerReviewCondition struct {
	Summary string                       `json:"summary"`
	Issues  []RunnerReviewConditionIssue `json:"issues"`
}

// EvidenceRefs and EvidenceQuote are deliberately serialized here. Their
// omission from a provider's review presentation cannot erase repair evidence.
type RunnerReviewConditionIssue struct {
	MessageIndex      int      `json:"message_index"`
	Claim             string   `json:"claim"`
	Verdict           string   `json:"verdict"`
	Severity          string   `json:"severity"`
	Evidence          string   `json:"evidence"`
	ArtifactVersionID string   `json:"artifact_version_id"`
	EvidenceRefs      []string `json:"evidence_refs"`
	EvidenceQuote     string   `json:"evidence_quote"`
}

type RunnerPlanCondition struct {
	ArtifactID string                    `json:"artifact_id"`
	VersionID  string                    `json:"version_id"`
	Steps      []RunnerPlanConditionStep `json:"steps"`
}
type RunnerPlanConditionStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type RunnerVisualCondition struct {
	Artifacts    []RunnerVisualConditionArtifact `json:"artifacts"`
	UnboundNames []string                        `json:"unbound_names"`
}
type RunnerVisualConditionArtifact struct {
	Name      string `json:"name"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
	Failure   string `json:"failure"`
}
type RunnerTextCondition struct {
	Detail string `json:"detail"`
}

func EncodeRunnerCorrectionCondition(condition RunnerCorrectionCondition) ([]byte, error) {
	if err := condition.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(condition)
}

func DecodeRunnerCorrectionCondition(raw []byte) (RunnerCorrectionCondition, error) {
	var condition RunnerCorrectionCondition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&condition); err != nil {
		return condition, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return condition, errors.New("correction condition has trailing data")
	}
	return condition, condition.Validate()
}

func (condition RunnerCorrectionCondition) Validate() error {
	invalid := errors.New("runner correction condition is invalid")
	if condition.Schema != RunnerCorrectionConditionSchema || !validReasonCode(condition.ReasonCode) {
		return invalid
	}
	bodies := 0
	if condition.Reference != nil {
		bodies++
		if condition.ReasonCode != "artifact_reference_correction_required" || condition.Reference.UnresolvedArtifacts < 0 {
			return invalid
		}
		for _, group := range condition.Reference.groups() {
			if !validConditionStrings(group) {
				return invalid
			}
		}
	}
	if condition.Review != nil {
		bodies++
		if condition.ReasonCode != "completion_review_correction_required" || !validConditionStrings([]string{condition.Review.Summary}) {
			return invalid
		}
		for _, issue := range condition.Review.Issues {
			if issue.MessageIndex < 0 || strings.TrimSpace(issue.Claim) == "" || !validConditionStrings([]string{issue.Claim, issue.Verdict, issue.Severity, issue.Evidence, issue.ArtifactVersionID, issue.EvidenceQuote}) || !validConditionStrings(issue.EvidenceRefs) {
				return invalid
			}
		}
	}
	if condition.Plan != nil {
		bodies++
		if condition.ReasonCode != "plan_step_status_required" || !validConditionStrings([]string{condition.Plan.ArtifactID, condition.Plan.VersionID}) {
			return invalid
		}
		for _, step := range condition.Plan.Steps {
			if !validConditionStrings([]string{step.ID, step.Title}) {
				return invalid
			}
		}
	}
	if condition.Visual != nil {
		bodies++
		if condition.ReasonCode != "visual_artifact_validation_required" || !validConditionStrings(condition.Visual.UnboundNames) {
			return invalid
		}
		for _, artifact := range condition.Visual.Artifacts {
			if !validConditionStrings([]string{artifact.Name, artifact.VersionID, artifact.SHA256, artifact.Failure}) {
				return invalid
			}
		}
	}
	if condition.Text != nil {
		bodies++
		switch condition.ReasonCode {
		case "artifact_reference_correction_required", "completion_review_correction_required", "plan_step_status_required", "visual_artifact_validation_required":
			return invalid
		}
		if strings.TrimSpace(condition.Text.Detail) == "" || !validConditionStrings([]string{condition.Text.Detail}) {
			return invalid
		}
	}
	if bodies != 1 {
		return invalid
	}
	return nil
}

func validConditionStrings(values []string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

func (reference RunnerReferenceCondition) groups() [][]string {
	return [][]string{reference.UnresolvedArtifactReferences, reference.MalformedArtifactReferences,
		reference.UnsupportedCitations, reference.InvalidReferenceArtifacts, reference.InvalidScientificArtifacts,
		reference.CrossArtifactFailures, reference.InvalidResearchArtifacts, reference.MissingLocalArtifacts,
		reference.MissingRequiredDeliverables}
}
