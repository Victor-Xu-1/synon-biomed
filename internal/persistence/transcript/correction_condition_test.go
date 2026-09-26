package transcript

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCorrectionConditionIdentityAndExactEvidence(t *testing.T) {
	base := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "completion_review_correction_required",
		Review: &RunnerReviewCondition{Summary: "presentation one", Issues: []RunnerReviewConditionIssue{{
			MessageIndex: 7, Claim: "claim", Verdict: "revise", Severity: "major", Evidence: "field=value",
			EvidenceQuote: "exact quoted result", EvidenceRefs: []string{"receipt-b", "receipt-a"}, ArtifactVersionID: "version-1",
		}}}}
	raw, err := EncodeRunnerCorrectionCondition(base)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRunnerCorrectionCondition(raw)
	if err != nil || !reflect.DeepEqual(base, decoded) {
		t.Fatalf("exact evidence roundtrip: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*RunnerReviewConditionIssue)
	}{
		{"claim", func(issue *RunnerReviewConditionIssue) { issue.Claim += " changed" }},
		{"quote", func(issue *RunnerReviewConditionIssue) { issue.EvidenceQuote += " changed" }},
		{"reference", func(issue *RunnerReviewConditionIssue) { issue.EvidenceRefs = []string{"other-receipt"} }},
		{"version", func(issue *RunnerReviewConditionIssue) { issue.ArtifactVersionID = "version-2" }},
		{"coordinate", func(issue *RunnerReviewConditionIssue) { issue.MessageIndex++ }},
		{"evidence", func(issue *RunnerReviewConditionIssue) { issue.Evidence = "field=other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, _ := DecodeRunnerCorrectionCondition(raw)
			test.change(&changed.Review.Issues[0])
			if changed.Fingerprint() == base.Fingerprint() {
				t.Fatal("changed semantic field lost from identity")
			}
		})
	}
	cosmetic, _ := DecodeRunnerCorrectionCondition(raw)
	cosmetic.Review.Summary = "presentation two"
	cosmetic.Review.Issues[0].Severity = " MAJOR "
	cosmetic.Review.Issues[0].EvidenceRefs = []string{"receipt-a", "receipt-b", "receipt-a"}
	cosmetic.Review.Issues = append(cosmetic.Review.Issues, cosmetic.Review.Issues[0])
	if cosmetic.Fingerprint() != base.Fingerprint() {
		t.Fatal("presentation or set ordering changed semantic identity")
	}
	if cosmetic.ContentID() == base.ContentID() {
		t.Fatal("read ID failed to bind exact condition evidence")
	}
	if string(raw) == "" || !strings.Contains(string(raw), "exact quoted result") {
		t.Fatal("review quote not serialized")
	}
}

func TestCorrectionConditionReferenceSetsAndLongSuffix(t *testing.T) {
	base := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "artifact_reference_correction_required",
		Reference: &RunnerReferenceCondition{MissingLocalArtifacts: []string{"b", "a"}}}
	cosmetic := base
	ref := *base.Reference
	ref.MissingLocalArtifacts = []string{"a", "b", "a"}
	cosmetic.Reference = &ref
	if cosmetic.Fingerprint() != base.Fingerprint() {
		t.Fatal("unordered diagnostic set became positional")
	}
	base.Reference.MissingLocalArtifacts = []string{strings.Repeat("path/", 2000) + "first"}
	cosmetic.Reference.MissingLocalArtifacts = []string{strings.Repeat("path/", 2000) + "second"}
	if cosmetic.Fingerprint() == base.Fingerprint() {
		t.Fatal("full path suffix was clipped")
	}
	base.Reference.MissingLocalArtifacts = []string{"A  B"}
	cosmetic.Reference.MissingLocalArtifacts = []string{"A B"}
	if cosmetic.Fingerprint() == base.Fingerprint() {
		t.Fatal("meaningful internal path whitespace collapsed")
	}
}

func TestCorrectionConditionPlanAndVisualBindVersions(t *testing.T) {
	plan := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "plan_step_status_required", Plan: &RunnerPlanCondition{ArtifactID: "plan", VersionID: "plan-v1", Steps: []RunnerPlanConditionStep{{ID: "a", Title: "First"}, {ID: "b", Title: "Second"}}}}
	changed := plan
	body := *plan.Plan
	changed.Plan = &body
	changed.Plan.VersionID = "plan-v2"
	if changed.Fingerprint() == plan.Fingerprint() {
		t.Fatal("plan version did not bind its unfinished steps")
	}
	changed.Plan.VersionID = "plan-v1"
	changed.Plan.Steps = []RunnerPlanConditionStep{plan.Plan.Steps[1], plan.Plan.Steps[0]}
	if changed.Fingerprint() != plan.Fingerprint() {
		t.Fatal("finding order changed plan obligation")
	}
	visual := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "visual_artifact_validation_required", Visual: &RunnerVisualCondition{Artifacts: []RunnerVisualConditionArtifact{{Name: "figure.png", VersionID: "image-v1", SHA256: strings.Repeat("a", 64), Failure: "missing_validation"}}}}
	for _, change := range []string{"version", "digest", "failure"} {
		raw, _ := EncodeRunnerCorrectionCondition(visual)
		different, _ := DecodeRunnerCorrectionCondition(raw)
		switch change {
		case "version":
			different.Visual.Artifacts[0].VersionID = "image-v2"
		case "digest":
			different.Visual.Artifacts[0].SHA256 = strings.Repeat("b", 64)
		case "failure":
			different.Visual.Artifacts[0].Failure = "invalid_digest"
		}
		if different.Fingerprint() == visual.Fingerprint() {
			t.Fatalf("visual %s did not change identity", change)
		}
	}
}

func TestCorrectionConditionRejectsMalformedContract(t *testing.T) {
	base := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "artifact_reference_correction_required", Reference: &RunnerReferenceCondition{}}
	raw, _ := EncodeRunnerCorrectionCondition(base)
	for _, test := range []struct {
		name   string
		change func(*RunnerCorrectionCondition)
	}{
		{"schema", func(c *RunnerCorrectionCondition) { c.Schema = "unknown" }},
		{"reason", func(c *RunnerCorrectionCondition) { c.ReasonCode = "another_reason" }},
		{"negative count", func(c *RunnerCorrectionCondition) { c.Reference.UnresolvedArtifacts = -1 }},
		{"multiple bodies", func(c *RunnerCorrectionCondition) { c.Text = &RunnerTextCondition{Detail: "other"} }},
		{"missing body", func(c *RunnerCorrectionCondition) { c.Reference = nil }},
		{"invalid utf8", func(c *RunnerCorrectionCondition) { c.Reference.MissingLocalArtifacts = []string{string([]byte{0xff})} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := DecodeRunnerCorrectionCondition(raw)
			test.change(&c)
			if _, err := EncodeRunnerCorrectionCondition(c); err == nil {
				t.Fatal("invalid contract encoded")
			}
		})
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = true
	unknown, _ := json.Marshal(object)
	for _, invalid := range [][]byte{unknown, append(append([]byte(nil), raw...), []byte(" {}")...), []byte(`{"schema":"synon.runner_correction_condition.v1"}`)} {
		if _, err := DecodeRunnerCorrectionCondition(invalid); err == nil {
			t.Fatal("unknown, incomplete or trailing condition accepted")
		}
	}
}
