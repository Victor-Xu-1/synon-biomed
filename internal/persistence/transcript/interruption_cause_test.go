package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunnerInterruptionCauseCodec(t *testing.T) {
	cause := RunnerInterruptionCause{ReasonCode: "artifact_reference_correction_required", Detail: "exact unresolved obligation"}
	encoded, err := RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	decoded, present, err := ParseRunnerInterruptionCause(map[string]any{RunnerInterruptionCauseField: encoded})
	if err != nil || !present || decoded != cause {
		t.Fatalf("decoded=%#v present=%t error=%v", decoded, present, err)
	}
	if _, present, err := ParseRunnerInterruptionCause(map[string]any{}); err != nil || present {
		t.Fatalf("legacy absence=%t %v", present, err)
	}
	for _, invalid := range []RunnerInterruptionCause{
		{}, {ReasonCode: "bad code", Detail: "detail"}, {ReasonCode: "valid", Detail: ""},
		{ReasonCode: "valid", Detail: strings.Repeat("x", maxRunnerInterruptionResumeDetailBytes+1)},
		{ReasonCode: "valid", Detail: "invalid\x00detail"}, {ReasonCode: "valid", Detail: string([]byte{0xff})},
	} {
		if _, err := RunnerInterruptionCausePayload(invalid); err == nil {
			t.Errorf("invalid cause accepted: reason=%q bytes=%d", invalid.ReasonCode, len(invalid.Detail))
		}
	}
	for _, invalid := range []any{
		nil, "not an object", map[string]any{"schema": "unknown", "reason_code": "valid", "detail": "detail"},
		map[string]any{"schema": runnerInterruptionCauseSchema, "reason_code": "valid"},
		map[string]any{"schema": runnerInterruptionCauseSchema, "reason_code": 2, "detail": "detail"},
		map[string]any{"schema": runnerInterruptionCauseSchema, "reason_code": "valid", "detail": "detail", "extra": true},
	} {
		if _, present, err := ParseRunnerInterruptionCause(map[string]any{RunnerInterruptionCauseField: invalid}); !present || err == nil {
			t.Errorf("malformed cause accepted: present=%t error=%v", present, err)
		}
	}
}

func TestRepositoryInterruptionCauseIsAtomicAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	repo, _, dsn := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "cause-stream", "owner-a")
	claimed, err := repo.ClaimRunner(ctx, ClaimRunnerInput{StreamUID: "cause-stream", OwnerID: "owner-a", RunnerID: "cause-runner", TTL: time.Minute, ResumeSource: ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v error=%v", claimed, err)
	}
	cause := RunnerInterruptionCause{ReasonCode: "artifact_reference_correction_required", Detail: strings.Repeat("x", maxRunnerInterruptionResumeDetailBytes)}
	input := InterruptRunnerInput{Claim: claimed.Claim, ClientMessageID: "interruption-cause", ReasonCode: "strategy_paused", ResumeDetail: "preserve the exact correction", Cause: &cause, Resumable: true}
	invalid := input
	invalid.Cause = &RunnerInterruptionCause{ReasonCode: "valid", Detail: strings.Repeat("x", maxRunnerInterruptionResumeDetailBytes+1)}
	if _, err := repo.InterruptRunner(ctx, invalid); err == nil {
		t.Fatal("oversized cause accepted")
	}
	if _, found, err := repo.LatestResumableCheckpoint(ctx, "cause-stream", "owner-a"); err != nil || found {
		t.Fatalf("invalid cause partially persisted: found=%t error=%v", found, err)
	}
	state, err := repo.GetRunnerRuntimeState(ctx, "cause-stream", "owner-a", claimed.Claim.Attempt)
	if err != nil || state.Status != "running" {
		t.Fatalf("invalid cause changed runner: %#v %v", state, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.InterruptRunner(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	foreign := input
	foreign.Claim.OwnerID = "owner-b"
	if _, err := repo.InterruptRunner(ctx, foreign); err == nil {
		t.Fatal("foreign owner changed interruption")
	}
	result, err := repo.InterruptRunner(ctx, input)
	if err != nil || !result.Created || !result.Checkpoint.Resumable {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	again, err := repo.InterruptRunner(ctx, input)
	if err != nil || again.Created || again.Event.EventID != result.Event.EventID {
		t.Fatalf("idempotent retry=%#v error=%v", again, err)
	}
	changed := input
	changed.Cause = &RunnerInterruptionCause{ReasonCode: cause.ReasonCode, Detail: "different obligation"}
	if _, err := repo.InterruptRunner(ctx, changed); err == nil {
		t.Fatal("same identity overwrote its correction cause")
	}
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	projected, err := reopened.ListProjectedCoordinateEvents(ctx, ListProjectedEventsInput{StreamUID: "cause-stream", OwnerID: "owner-a", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range projected {
		if event.Event.EventID != result.Event.EventID {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		actual, present, err := ParseRunnerInterruptionCause(payload)
		if err != nil || !present || actual != cause || payload["auto_resume"] != false || payload["reason_code"] != "strategy_paused" {
			t.Fatalf("reopened correction lost: present=%t bytes=%d error=%v", present, len(actual.Detail), err)
		}
		found = true
	}
	if !found {
		t.Fatal("reopened interruption missing")
	}
}
