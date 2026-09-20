package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCorrectionConditionAtomicChunksAndReopen(t *testing.T) {
	ctx := context.Background()
	repo, db, dsn := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "condition-stream", "owner-a")
	claimed, err := repo.ClaimRunner(ctx, ClaimRunnerInput{StreamUID: "condition-stream", OwnerID: "owner-a", RunnerID: "condition-runner", TTL: time.Minute, ResumeSource: ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim: %v", err)
	}
	condition := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "completion_review_correction_required", Review: &RunnerReviewCondition{
		Summary: "display", Issues: []RunnerReviewConditionIssue{{MessageIndex: 2, Claim: strings.Repeat("large finding\n", 100000), EvidenceQuote: "quoted tail 界", EvidenceRefs: []string{"exact-receipt"}}},
	}}
	raw, err := EncodeRunnerCorrectionCondition(condition)
	if err != nil || len(raw) <= MaxEventPayloadBytes {
		t.Fatalf("large fixture bytes=%d error=%v", len(raw), err)
	}
	cause := RunnerInterruptionCause{ReasonCode: condition.ReasonCode, Detail: "bounded diagnostic", Condition: &condition}
	input := InterruptRunnerInput{Claim: claimed.Claim, ClientMessageID: "condition-final", ReasonCode: condition.ReasonCode, ResumeDetail: cause.Detail, Cause: &cause, AutoResume: true}
	if _, _, _, err := repo.AppendRunnerCheckpoint(ctx, AppendRunnerCheckpointInput{Claim: claimed.Claim, ClientMessageID: "before-condition", Phase: RunnerPhaseExecuting, PayloadJSON: []byte(`{"marker":"retain-before-condition"}`)}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var value int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='condition-stream'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := count()
	if _, err := db.Exec(`CREATE TRIGGER reject_condition_commit BEFORE INSERT ON transcript_events WHEN NEW.client_message_id='condition-final' BEGIN SELECT RAISE(ABORT,'injected condition commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InterruptRunner(ctx, input); err == nil {
		t.Fatal("fault injection did not fail")
	}
	if after := count(); after != before {
		t.Fatalf("chunk transaction partially committed: before=%d after=%d", before, after)
	}
	if _, err := repo.ValidateLiveRunnerClaim(ctx, claimed.Claim); err != nil {
		t.Fatalf("failed transaction released lease: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_condition_commit`); err != nil {
		t.Fatal(err)
	}
	foreign := input
	foreign.Claim.OwnerID = "owner-b"
	if _, err := repo.InterruptRunner(ctx, foreign); err == nil || count() != before {
		t.Fatal("foreign owner wrote chunks")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.InterruptRunner(canceled, input); !errors.Is(err, context.Canceled) || count() != before {
		t.Fatalf("canceled write: %v", err)
	}
	result, err := repo.InterruptRunner(ctx, input)
	if err != nil || !result.Created || !result.Checkpoint.Resumable {
		t.Fatalf("large interruption: %v", err)
	}
	after := count()
	if after-before < 3 {
		t.Fatal("large condition was not event-chunked")
	}
	modelWindow, err := repo.ListRunnerReplay(ctx, ListRunnerReplayInput{StreamUID: "condition-stream", OwnerID: "owner-a", MessageLimit: 10, CheckpointLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	keptPrevious := false
	for _, event := range modelWindow {
		if strings.Contains(string(event.ResolvedPayloadJSON), RunnerCorrectionConditionChunkField) {
			t.Fatal("storage chunks consumed model history")
		}
		keptPrevious = keptPrevious || strings.Contains(string(event.ResolvedPayloadJSON), "retain-before-condition")
	}
	if !keptPrevious {
		t.Fatal("large condition evicted earlier semantic checkpoint from model seed")
	}
	again, err := repo.InterruptRunner(ctx, input)
	if err != nil || again.Created || again.Event.EventID != result.Event.EventID || count() != after {
		t.Fatalf("non-idempotent retry: %v", err)
	}
	changed := condition
	changedReview := *condition.Review
	changedReview.Summary = "another display"
	changed.Review = &changedReview
	changedInput := input
	changedCause := cause
	changedCause.Condition = &changed
	changedInput.Cause = &changedCause
	if _, err := repo.InterruptRunner(ctx, changedInput); !errors.Is(err, ErrEventConflict) || count() != after {
		t.Fatalf("conflicting retry changed condition: %v", err)
	}
	if _, err := repo.ValidateLiveRunnerClaim(ctx, claimed.Claim); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("released lease still live: %v", err)
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	projected, err := reopened.ListProjectedCoordinateEvents(ctx, ListProjectedEventsInput{StreamUID: "condition-stream", OwnerID: "owner-a", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var assembler RunnerCorrectionAssembler
	var restored RunnerInterruptionCause
	chunks := 0
	for _, event := range projected {
		if len(event.ResolvedPayloadJSON) > MaxEventPayloadBytes {
			t.Fatal("event budget bypassed")
		}
		if event.Event.Type != "runner_checkpoint" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		if chunk, err := assembler.Observe(payload); err != nil {
			t.Fatal(err)
		} else if chunk {
			chunks++
			continue
		}
		parsed, present, err := ParseRunnerInterruptionCause(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			continue
		}
		if parsed.ConditionRef == nil {
			t.Fatal("large checkpoint has no canonical reference")
		}
		restored, err = assembler.Resolve(parsed)
		if err != nil {
			t.Fatal(err)
		}
	}
	if chunks < 2 || restored.Condition == nil || !reflect.DeepEqual(*restored.Condition, condition) {
		t.Fatal("reopen lost complete typed evidence")
	}
	if restored.Condition.Fingerprint() != condition.Fingerprint() || restored.Condition.ContentID() != condition.ContentID() {
		t.Fatal("reopen changed condition identity")
	}
	t.Logf("condition_bytes=%d chunks=%d event_budget=%d", len(raw), chunks, MaxEventPayloadBytes)
}

func TestCorrectionConditionChunksRejectIncompleteAndCorruptEvidence(t *testing.T) {
	condition := RunnerCorrectionCondition{Schema: RunnerCorrectionConditionSchema, ReasonCode: "plan_step_status_required", Plan: &RunnerPlanCondition{Steps: []RunnerPlanConditionStep{{ID: "step", Title: strings.Repeat("step", 50000)}}}}
	cause := RunnerInterruptionCause{ReasonCode: condition.ReasonCode, Detail: "display", Condition: &condition}
	stored, raw, err := prepareRunnerCorrectionStorage("chunk-negative", &cause)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConditionRef == nil {
		t.Fatal("fixture not chunked")
	}
	for _, variant := range []string{"missing", "reordered", "corrupt", "oversized", "different identity"} {
		t.Run(variant, func(t *testing.T) {
			var assembler RunnerCorrectionAssembler
			var observedErr error
			for index, offset := 0, 0; offset < len(raw); index, offset = index+1, offset+correctionConditionChunkBytes {
				if variant == "missing" && index == 1 {
					continue
				}
				chunk := runnerCorrectionConditionChunk{Reference: *stored.ConditionRef, Index: index, Data: append([]byte(nil), raw[offset:min(offset+correctionConditionChunkBytes, len(raw))]...)}
				if variant == "reordered" && index == 1 {
					chunk.Index++
				}
				if variant == "corrupt" && index == 1 {
					chunk.Data[0] ^= 1
				}
				if variant == "oversized" && index == 1 {
					chunk.Data = append(chunk.Data, 'x')
				}
				_, observedErr = assembler.Observe(map[string]any{RunnerCorrectionConditionChunkField: chunk})
				if observedErr != nil {
					break
				}
			}
			if observedErr == nil {
				root := *stored
				if variant == "different identity" {
					ref := *root.ConditionRef
					ref.Fingerprint = strings.Repeat("a", 64)
					root.ConditionRef = &ref
				}
				_, observedErr = assembler.Resolve(root)
			}
			if observedErr == nil {
				t.Fatal("incomplete/corrupt evidence became a recovery condition")
			}
		})
	}
}
