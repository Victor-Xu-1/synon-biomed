package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// Legacy checkpoint fixture only. Production classifiers consume complete
// condition projections directly and never manufacture bounded replay entries.
func runnerCorrectionEntriesForClassification(reason, detail string) []eventjournal.Entry {
	return []eventjournal.Entry{{Message: eventjournal.Message{"type": "runner_checkpoint", "status": "interrupted", "reason_code": reason, "resume_detail": detail}}}
}

func TestCorrectionIdentityBeyondDisplayBudget(t *testing.T) {
	prefix := strings.Repeat("segment/", 40)
	refs := make([]string, 32)
	for index := range refs {
		refs[index] = fmt.Sprintf("source-%02d", index)
	}
	visualNames := []string{"a.png", "b.png", "c.png", "d.png", "e.png", "f.png", "g.png", "h.png"}
	for _, test := range []struct {
		name string
		a, b sessionRunnerBoundedCorrection
	}{
		{"identifier suffix", &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{prefix + "first.dat"}}, &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{prefix + "second.dat"}}},
		{"omitted identifier", &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: append(append([]string(nil), refs...), "zz-first.dat")}, &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: append(append([]string(nil), refs...), "zz-second.dat")}},
		{"review suffix", sessionRunnerCompletionReviewCorrection{Issues: []sessionRunnerReviewIssue{{Severity: "major", Claim: strings.Repeat("finding ", 800) + "first"}}}, sessionRunnerCompletionReviewCorrection{Issues: []sessionRunnerReviewIssue{{Severity: "major", Claim: strings.Repeat("finding ", 800) + "second"}}}},
		{"ninth visual artifact", &sessionRunnerVisualArtifactValidationRequired{Artifacts: append(append([]string(nil), visualNames...), "z-first.png")}, &sessionRunnerVisualArtifactValidationRequired{Artifacts: append(append([]string(nil), visualNames...), "z-second.png")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := test.a.runnerCorrection(), test.b.runnerCorrection()
			if a.Detail != b.Detail {
				t.Fatal("fixture must collide in the display projection")
			}
			if runnerCorrectionFingerprint(a) == runnerCorrectionFingerprint(b) {
				t.Fatal("display clipping merged distinct complete conditions")
			}
		})
	}
}

func TestCorrectionDisplayCollisionDoesNotInheritBudget(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	prefix := strings.Repeat("segment/", 40)
	first := &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{prefix + "first.dat"}}
	different := &sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{prefix + "second.dat"}}
	cause := first.runnerCorrection()
	payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, fmt.Sprintf("collision-%d", index), map[string]any{"status": "interrupted", "reason_code": cause.ReasonCode, "resume_detail": cause.Detail, transcriptstore.RunnerInterruptionCauseField: payload})
	}
	entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	var chatErr error = different
	result := &SessionRunnerCycleResult{SessionID: fixture.run.SessionID, Attempt: fixture.run.Attempt}
	handled, err := fixture.server.handleSessionRunnerChatInterruption(context.Background(), fixture.options, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, fixture.run.Transcript, fixture.run, entries, &chatErr, nil)
	if err != nil || !handled {
		t.Fatalf("handled=%t error=%v", handled, err)
	}
	if result.InterruptionReasonCode == sessionRunnerCorrectionNoProgressExhaustedReasonCode || !result.InterruptionAutoResume {
		t.Fatal("different complete condition inherited an exhausted display budget")
	}
}

func TestCorrectionRepairConsumersRetainEveryFinding(t *testing.T) {
	for _, test := range []struct {
		name       string
		count, row int
		prefix     string
	}{
		{"beyond preview and old projection count", 96, 3, ""},
		{"large valid row coordinate", 1, 1000001, ""},
		{"long exact artifact identity", 1, 3, strings.Repeat("segment/", 150)},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := &sessionRunnerReferenceIntegrityError{}
			for index := 0; index < test.count; index++ {
				failure.CrossArtifactFailures = append(failure.CrossArtifactFailures, fmt.Sprintf("evidence_source_locator_missing:%sEvidence-%03d.csv row=%d source_type=publication", test.prefix, index, test.row))
			}
			cause := failure.runnerCorrection()
			correction := recoveredRunnerCorrection{ReasonCode: cause.ReasonCode, Detail: cause.Detail, Condition: cause.Condition}
			run := &sessionRunnerChatRun{}
			run.restoreCorrection(&correction)
			requirements := runnerArtifactRepairRequirements(run.CorrectionDetail)
			if len(requirements) != test.count {
				t.Fatalf("repair findings=%d want=%d", len(requirements), test.count)
			}
			if stringValue(requirements[0]["artifact_path"]) != test.prefix+"Evidence-000.csv" || int(numberValue(requirements[0]["row_number"])) != test.row {
				t.Fatal("repair target changed")
			}
		})
	}
}

func TestCorrectionTypedEvidenceControlsObligationIdentity(t *testing.T) {
	for _, change := range []string{"summary", "quote", "reference"} {
		t.Run(change, func(t *testing.T) {
			fixture := newNoProgressReceiptFixture(t)
			first := sessionRunnerCompletionReviewCorrection{Summary: "original presentation", Issues: []sessionRunnerReviewIssue{{Claim: "same claim", Severity: "major", EvidenceQuote: "exact evidence A", EvidenceRefs: []string{"receipt-A"}}}}
			cause := first.runnerCorrection()
			payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 2; index++ {
				appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, fmt.Sprintf("typed-budget-%d", index), map[string]any{"status": "interrupted", "reason_code": cause.ReasonCode, "resume_detail": cause.Detail, transcriptstore.RunnerInterruptionCauseField: payload})
			}
			next := first
			next.Issues = append([]sessionRunnerReviewIssue(nil), first.Issues...)
			switch change {
			case "summary":
				next.Summary = "cosmetic wording changed"
			case "quote":
				next.Issues[0].EvidenceQuote = "exact evidence B"
			case "reference":
				next.Issues[0].EvidenceRefs = []string{"receipt-B"}
			}
			entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 20, 20)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if change == "summary" {
				want = 2
			}
			if count := runnerRepeatedCorrectionInterruptionCount(entries, next.runnerCorrection()); count != want {
				t.Fatalf("count=%d want=%d", count, want)
			}
			var chatErr error = next
			result := &SessionRunnerCycleResult{SessionID: fixture.run.SessionID, Attempt: fixture.run.Attempt}
			if handled, err := fixture.server.handleSessionRunnerChatInterruption(context.Background(), fixture.options, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, fixture.run.Transcript, fixture.run, entries, &chatErr, nil); err != nil || !handled {
				t.Fatalf("handled=%t error=%v", handled, err)
			}
			if result.InterruptionReasonCode != cause.ReasonCode || !result.InterruptionAutoResume {
				t.Fatalf("obligation count became a terminal budget: %#v", result)
			}
		})
	}
}

func TestCorrectionLegacyPreviewNeverInventsCompleteEvidence(t *testing.T) {
	cause := (&sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{strings.Repeat("path/", 90) + "tail"}}).runnerCorrection()
	entries := runnerCorrectionEntriesForClassification(cause.ReasonCode, cause.Detail)
	legacy, found := latestRunnerCorrection(entries)
	if !found || legacy.Condition != nil {
		t.Fatal("legacy preview invented typed fields")
	}
	if count := runnerRepeatedCorrectionInterruptionCount(entries, cause); count != 0 {
		t.Fatal("legacy clipped evidence was guessed equal to a complete condition")
	}
	if count := runnerRepeatedCorrectionInterruptionCount(entries, transcriptstore.RunnerInterruptionCause{ReasonCode: cause.ReasonCode, Detail: cause.Detail}); count != 1 {
		t.Fatal("legacy budget was lost")
	}
	for _, entry := range []eventjournal.Entry{
		{Message: eventjournal.Message{"type": "message", "role": "assistant", "interruption_cause": cause}},
		{RuntimeProjection: cause, Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": "model_protocol_error"}},
	} {
		if _, found := latestRunnerCorrection([]eventjournal.Entry{entry}); found {
			t.Fatal("untrusted projection became a condition")
		}
	}
}
