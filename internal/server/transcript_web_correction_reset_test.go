package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptWebIncrementalCheckpointSkipsRetiredCorrectionSegment(t *testing.T) {
	const sessionID = "frame-retired-correction-segment"
	attempt := int64(1)
	event := func(eventID int64, eventType string, payload map[string]any) transcriptstore.ProjectedEvent {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ProjectedEvent{
			Event: transcriptstore.Event{
				StreamUID: "frame:" + sessionID, EventID: eventID, PublicationSeq: eventID,
				Type: eventType, Source: transcriptstore.EventSourcePayload,
				RunnerAttempt: &attempt, ClientMessageID: fmt.Sprintf("event-%d", eventID),
			},
			ResolvedPayloadJSON: encoded,
		}
	}
	events := []transcriptstore.ProjectedEvent{
		event(1, "content_delta", map[string]any{
			"text": "rejected candidate", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}),
		event(2, "content_reset", map[string]any{
			"text": "", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
		}),
		event(3, "content_delta", map[string]any{
			"text": "corrected result", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
		}),
	}
	reducer := newTranscriptWebCoordinateReducer(sessionID, false)
	for _, projected := range events {
		if err := reducer.consume(projected); err != nil {
			t.Fatalf("consume event %d: %v", projected.Event.EventID, err)
		}
	}
	checkpoint, err := transcriptWebIncrementalAssistantStateFromReducer(reducer, events)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	retired := transcriptAssistantMessageID(sessionID, attempt, 1)
	corrected := transcriptAssistantMessageID(sessionID, attempt, 2)
	if len(checkpoint.Attempts) != 1 || checkpoint.Attempts[0].CurrentIdentity != corrected ||
		len(checkpoint.Attempts[0].SegmentIdentities) != 1 || checkpoint.Attempts[0].SegmentIdentities[0] != corrected {
		t.Fatalf("checkpoint retained retired segment %q: %#v", retired, checkpoint)
	}
	if len(checkpoint.SeenIdentities) != 2 || checkpoint.SeenIdentities[0] != retired || checkpoint.SeenIdentities[1] != corrected {
		t.Fatalf("checkpoint lost durable identity history: %#v", checkpoint.SeenIdentities)
	}
}

func TestTranscriptWebHistoryAcceptsConsecutiveEmptyCorrectionResets(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-consecutive-correction-resets", "frame-consecutive-correction-resets")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-consecutive-correction-resets", MessageUUID: "consecutive-reset-user",
		ClientMessageID: "consecutive-reset-user-client", Text: "complete a task with automatic quality correction",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-consecutive-correction-resets")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "consecutive-reset-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "consecutive-reset-candidate", "content_delta", map[string]any{
		"text":              "candidate rejected by the first quality gate",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "consecutive-reset-first", "content_reset", map[string]any{
		"text":              "",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "consecutive-reset-second", "content_reset", map[string]any{
		"text":              "",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, transcriptstore.AssistantReplaceScopeSegment),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "consecutive-reset-corrected", "content_delta", map[string]any{
		"text":              "corrected evidence-backed result",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(
		context.Background(), "local", "frame-consecutive-correction-resets",
	)
	if err != nil || !authoritative || len(messages) != 2 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	assertTranscriptTextMessage(t, messages[1], "corrected evidence-backed result")

	frame, found, err := store.GetCompatibilityFrame("frame-consecutive-correction-resets")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	readCursor, active, err := server.compatibilityFrameReadCursorCoordinates(context.Background(), frame)
	if err != nil || !active || len(readCursor.coordinates) != len(messages) {
		t.Fatalf("readCursor=%#v active=%t messages=%#v err=%v", readCursor, active, messages, err)
	}
}

func TestTranscriptWebHistorySkipsUnmaterializedCandidateAfterCorrectionFence(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-empty-correction-candidate", "frame-empty-correction-candidate")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-empty-correction-candidate", MessageUUID: "empty-correction-user",
		ClientMessageID: "empty-correction-user-client", Text: "complete a task with automatic quality correction",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-empty-correction-candidate")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "empty-correction-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "empty-correction-candidate", "content_delta", map[string]any{
		"text":              "candidate rejected by the first quality gate",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "empty-correction-reset", "content_reset", map[string]any{
		"text":              "",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, transcriptstore.AssistantReplaceScopeSegment),
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "empty-correction-fence",
		ReasonCode:               sessionRunnerResponseLanguageMismatchReasonCode,
		ResumeDetail:             "restart the current response in Simplified Chinese",
		RecoveryContractRevision: sessionRunnerRecoveryContractRevision, AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "empty-correction-runner-resumed",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, reclaimed.Claim, "empty-correction-resume-state", map[string]any{
		"status": "running", "stage": "resume_state",
		"detail": "restoring the durable assistant continuation state", "lifecyclePhase": "recovery",
	})
	appendRunnerPayloadEvent(t, repo, reclaimed.Claim, "empty-correction-result", "content_delta", map[string]any{
		"text": "修复后的证据结果",
		// Legacy writers counted private rejected candidates even though no
		// public segment was emitted. The projection rebases that raw gap.
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(9, ""),
	})

	messages, authoritative, err := server.loadTranscriptWebHistory(
		context.Background(), "local", "frame-empty-correction-candidate",
	)
	if err != nil || !authoritative || len(messages) != 2 {
		t.Fatalf("messages=%#v authoritative=%t err=%v", messages, authoritative, err)
	}
	assertTranscriptTextMessage(t, messages[1], "修复后的证据结果")
}
