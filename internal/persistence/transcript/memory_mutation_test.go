package transcript

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestImmediateMemoryMutationInvocationAndReceiptAreStrictAndAudited(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-memory", "owner")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-memory", OwnerID: "owner", RunnerID: "runner", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	_, source, created, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "memory-source", Phase: RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"toolCallId":"call-memory","toolName":"write_memory","toolPhase":"start","toolInput":{"z":1,"a":"fact"}}`),
	})
	if err != nil || !created {
		t.Fatalf("source created=%t err=%v", created, err)
	}
	var invocation MemoryMutationInvocation
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		invocation, err = tx.ResolveMemoryMutationInvocation(context.Background(), claimed.Claim, source.EventID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if invocation.Receipt != nil || invocation.ToolCallID != "call-memory" || string(invocation.ToolInputJSON) != `{"a":"fact","z":1}` || len(invocation.InputSHA256) != 64 {
		t.Fatalf("invocation=%#v input=%s", invocation, invocation.ToolInputJSON)
	}
	input := MemoryMutationReceiptInput{
		Claim: claimed.Claim, SourceEventID: source.EventID,
		InputSHA256: invocation.InputSHA256, MutationSHA256: strings.Repeat("a", 64),
		Result: MemoryMutationReceiptResult{Appended: []string{"memory-1"}},
	}
	var receipt MemoryMutationReceipt
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		receipt, err = tx.AppendMemoryMutationReceipt(context.Background(), input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if receipt.Event.RunnerAttempt == nil || *receipt.Event.RunnerAttempt != claimed.Claim.Attempt ||
		receipt.InputSHA256 != invocation.InputSHA256 || receipt.MutationSHA256 != strings.Repeat("a", 64) ||
		receipt.Result.Appended[0] != "memory-1" {
		t.Fatalf("receipt=%#v", receipt)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "forged-memory-receipt", Type: memoryMutationReceiptEventType,
		Source: EventSourcePayload, PayloadJSON: []byte(`{"version":1}`),
	}); !errors.Is(err, ErrReservedEventType) {
		t.Fatalf("generic forged receipt error=%v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET payload_json=json_set(payload_json,'$.unexpected',1)
		WHERE stream_uid=? AND event_id=?`, receipt.Event.StreamUID, receipt.Event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.ResolveMemoryMutationInvocation(context.Background(), claimed.Claim, source.EventID)
		return err
	}); !errors.Is(err, ErrMemoryMutationReceiptConflict) {
		t.Fatalf("unknown receipt field error=%v", err)
	}
}

func TestImmediateMemoryMutationReceiptRequiresCurrentAttemptButReplaysToLaterClaim(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedTranscriptInput(t, repo, "stream-memory-source", "owner")
	claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-memory-source", OwnerID: "owner", RunnerID: "runner", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	checkpoint, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "memory-source", Phase: RunnerPhaseExecuting,
		Resumable:   true,
		PayloadJSON: []byte(`{"toolCallId":"call-memory","toolName":"write_memory","toolPhase":"start","toolInput":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var invocation MemoryMutationInvocation
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		invocation, err = tx.ResolveMemoryMutationInvocation(context.Background(), claimed.Claim, source.EventID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.AppendMemoryMutationReceipt(context.Background(), MemoryMutationReceiptInput{
			Claim: claimed.Claim, SourceEventID: source.EventID, InputSHA256: invocation.InputSHA256,
			MutationSHA256: strings.Repeat("b", 64), Result: MemoryMutationReceiptResult{Appended: []string{"memory"}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claimed.Claim.StreamUID, claimed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: claimed.Claim.StreamUID, OwnerID: claimed.Claim.OwnerID, RunnerID: "runner-2", TTL: time.Minute,
		ResumeSource: ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt ||
		reclaimed.Claim.ClaimToken == claimed.Claim.ClaimToken {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.ResolveMemoryMutationInvocation(context.Background(), claimed.Claim, source.EventID)
		return err
	}); !errors.Is(err, ErrClaimStale) {
		t.Fatalf("old lease error=%v", err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		replayed, err := tx.ResolveMemoryMutationInvocation(context.Background(), reclaimed.Claim, source.EventID)
		if err == nil && (replayed.Receipt == nil || replayed.SourceAttempt != claimed.Claim.Attempt) {
			t.Fatalf("replayed=%#v", replayed)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.ResolveMemoryMutationInvocation(context.Background(), reclaimed.Claim, 999)
		return err
	}); !errors.Is(err, ErrMemoryMutationReceiptConflict) {
		t.Fatalf("invalid source error=%v", err)
	}
}

func TestImmediateMemoryMutationInvocationRejectsMalformedCheckpointInput(t *testing.T) {
	for _, payload := range []string{
		`{"toolCallId":"call","toolName":"read_memory","toolPhase":"start","toolInput":{}}`,
		`{"toolCallId":"call","toolName":"write_memory","toolPhase":"finish","toolInput":{}}`,
		`{"toolCallId":"call","toolName":"write_memory","toolPhase":"start","toolInput":[]}`,
		`{"toolCallId":"call","toolName":"write_memory","toolPhase":"start"}`,
	} {
		repo, _, _ := newTranscriptRepository(t)
		streamID := "stream-malformed-" + strings.ReplaceAll(payload[:8], `"`, "")
		seedTranscriptInput(t, repo, streamID, "owner")
		claimed, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: streamID, OwnerID: "owner", RunnerID: "runner", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
		})
		if err != nil || !claimed.Claimed {
			t.Fatalf("claim=%#v err=%v", claimed, err)
		}
		_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claimed.Claim, ClientMessageID: "source", Phase: RunnerPhaseExecuting, PayloadJSON: []byte(payload),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
			_, err := tx.ResolveMemoryMutationInvocation(context.Background(), claimed.Claim, source.EventID)
			return err
		}); !errors.Is(err, ErrMemoryMutationReceiptConflict) {
			t.Fatalf("payload=%s error=%v", payload, err)
		}
	}
}
