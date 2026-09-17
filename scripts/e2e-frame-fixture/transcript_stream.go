package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	transcriptFixtureRunnerTTL   = 10 * time.Minute
	transcriptFixtureStreamDelay = 12 * time.Millisecond
)

func applyTranscriptStreamFixture(store *workspace.Store, request fixtureRequest) (map[string]any, error) {
	switch request.Action {
	case "begin-transcript-stream":
		return beginTranscriptStreamFixture(store, request.FrameID, request.Transcript.BatchID)
	case "append-transcript-deltas":
		return appendTranscriptFixtureDeltas(store, request.FrameID, *request.Transcript)
	case "append-transcript-assistant":
		return appendTranscriptFixtureAssistant(store, request.FrameID, *request.Transcript)
	case "begin-streaming-parity":
		return beginTranscriptStreamingParity(store, request.FrameID, *request.Transcript)
	case "complete-streaming-parity":
		return completeTranscriptStreamingParity(store, request.FrameID, *request.Transcript)
	case "begin-streaming-recovery":
		return beginTranscriptStreamingRecovery(store, request.FrameID, *request.Transcript)
	case "complete-streaming-recovery":
		return completeTranscriptStreamingRecovery(store, request.FrameID, *request.Transcript)
	default:
		return nil, fmt.Errorf("unsupported transcript stream fixture action %q", request.Action)
	}
}

func beginTranscriptStreamFixture(store *workspace.Store, frameID, runID string) (map[string]any, error) {
	ctx := context.Background()
	repository, stream, err := transcriptFixtureRepository(ctx, store, frameID)
	if err != nil {
		return nil, err
	}
	if err := ensureTranscriptFixtureTaskIntent(ctx, store, repository, stream, runID); err != nil {
		return nil, err
	}
	var claimed transcriptstore.ClaimRunnerResult
	err = retryTranscriptFixtureContention(ctx, "claim_transcript_fixture_runner", func() error {
		var claimErr error
		claimed, claimErr = repository.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "e2e-transcript-stream:" + runID,
			TTL: transcriptFixtureRunnerTTL, ResumeSource: transcriptstore.ResumeSourceFresh,
		})
		return claimErr
	})
	if err != nil || !claimed.Claimed {
		if err == nil {
			err = fmt.Errorf("runner is owned by %q", claimed.OwnerRunnerID)
		}
		return nil, fmt.Errorf("claim transcript fixture runner: %w", err)
	}
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claimed.Claim,
		"e2e-transcript-start:"+runID, transcriptstore.RunnerPhasePlanning,
		map[string]any{"status": "running", "detail": "runner chat started"}); err != nil {
		return nil, err
	}
	run := transcriptStreamFixtureRunFromClaim(frameID, runID, claimed.Claim)
	return map[string]any{"ok": true, "frameId": frameID, "run": run}, nil
}

func appendTranscriptFixtureDeltas(
	store *workspace.Store,
	frameID string,
	input transcriptStreamFixtureRequest,
) (map[string]any, error) {
	ctx := context.Background()
	repository, _, err := transcriptFixtureRepositoryForRun(ctx, store, frameID, input.Run)
	if err != nil {
		return nil, err
	}
	claim := input.Run.claim()
	for index, chunk := range input.Chunks {
		payload := map[string]any{
			"text": chunk, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}
		if err := appendTranscriptFixtureEvent(ctx, repository, claim,
			fmt.Sprintf("e2e-transcript-delta:%s:%03d", input.BatchID, index), "content_delta", payload); err != nil {
			return nil, fmt.Errorf("append transcript delta %d: %w", index, err)
		}
		if index+1 < len(input.Chunks) {
			time.Sleep(transcriptFixtureStreamDelay)
		}
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": len(input.Chunks)}, nil
}

func appendTranscriptFixtureAssistant(
	store *workspace.Store,
	frameID string,
	input transcriptStreamFixtureRequest,
) (map[string]any, error) {
	ctx := context.Background()
	repository, _, err := transcriptFixtureRepositoryForRun(ctx, store, frameID, input.Run)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"text": input.Text, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	}
	claim := input.Run.claim()
	if err := appendTranscriptFixtureEvent(ctx, repository, claim,
		"e2e-transcript-assistant:"+input.BatchID, "assistant_message", payload); err != nil {
		return nil, err
	}
	finishPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "transcript fixture assistant completed",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		return nil, err
	}
	if err := retryTranscriptFixtureContention(ctx, "finish_transcript_fixture_runner", func() error {
		_, _, _, finishErr := repository.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
			Claim: claim, ClientMessageID: "e2e-transcript-finish:" + input.BatchID, Status: "completed",
			PayloadJSON: finishPayload, Destinations: []string{"ws"},
		})
		return finishErr
	}); err != nil {
		return nil, fmt.Errorf("finish transcript fixture assistant: %w", err)
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": 2}, nil
}

func transcriptFixtureRepository(
	ctx context.Context,
	store *workspace.Store,
	frameID string,
) (*transcriptstore.Repository, transcriptstore.Stream, error) {
	frameContext, found, err := store.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame realtime context is unavailable")
		}
		return nil, transcriptstore.Stream{}, err
	}
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		return nil, transcriptstore.Stream{}, err
	}
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, frameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame transcript stream is unavailable")
		}
		return nil, transcriptstore.Stream{}, err
	}
	return repository, stream, nil
}

func transcriptFixtureRepositoryForRun(
	ctx context.Context,
	store *workspace.Store,
	frameID string,
	run *transcriptStreamFixtureRun,
) (*transcriptstore.Repository, transcriptstore.Stream, error) {
	repository, stream, err := transcriptFixtureRepository(ctx, store, frameID)
	if err != nil {
		return nil, transcriptstore.Stream{}, err
	}
	if run == nil || run.FrameID != frameID || run.StreamUID != stream.UID || run.OwnerID != stream.OwnerID {
		return nil, transcriptstore.Stream{}, errors.New("transcript fixture run does not match the active frame stream")
	}
	return repository, stream, nil
}

func ensureTranscriptFixtureTaskIntent(
	ctx context.Context,
	store *workspace.Store,
	repository *transcriptstore.Repository,
	stream transcriptstore.Stream,
	runID string,
) error {
	expectedFrameEventID := "e2e-transcript-task-event:" + runID
	expectedSourceEventID := "e2e-transcript-task:" + runID
	expectedSourceMessageID := "e2e-transcript-task-message:" + runID
	if intent, found, err := repository.GetActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID); err != nil {
		return err
	} else if found {
		scrollEventPrefix := "e2e-scroll-client:" + stream.FrameID + ":"
		scrollMessagePrefix := "e2e-scroll-message:" + stream.FrameID + ":"
		if (intent.SourceEventID == expectedSourceEventID && intent.SourceMessageID == expectedSourceMessageID) ||
			(strings.HasPrefix(intent.SourceEventID, scrollEventPrefix) &&
				strings.HasPrefix(intent.SourceMessageID, scrollMessagePrefix)) {
			return nil
		}
		return errors.New("frame already has an active task intent; refusing to replace non-fixture work")
	}
	messageID := expectedSourceMessageID
	text := "Exercise the durable transcript browser fixture through the real message stream."
	var frameEvent workspace.FrameEvent
	err := retryTranscriptFixtureContention(ctx, "append_transcript_fixture_frame_event", func() error {
		var appendErr error
		frameEvent, appendErr = store.AppendFrameEvent(workspace.FrameEventInput{
			ID: expectedFrameEventID, FrameID: stream.FrameID, Type: "user_message",
			Payload: map[string]any{"role": "user", "content": text, "uuid": messageID},
		})
		return appendErr
	})
	if err != nil {
		return err
	}
	err = retryTranscriptFixtureContention(ctx, "append_transcript_fixture_user_event", func() error {
		_, _, _, appendErr := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: expectedSourceEventID, FrameEventID: frameEvent.ID,
			MessageUUID: messageID, MessageOrigin: "task_intent", Text: text, Destinations: []string{"ws"},
		})
		return appendErr
	})
	if err != nil {
		return err
	}
	return nil
}

func appendTranscriptFixtureEvent(
	ctx context.Context,
	repository *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	clientID, eventType string,
	payload map[string]any,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := retryTranscriptFixtureContention(ctx, "append_transcript_fixture_event", func() error {
		_, _, appendErr := repository.AppendRunnerEvent(ctx, transcriptstore.AppendEventInput{
			Claim: claim, ClientMessageID: clientID, Type: eventType,
			Source: transcriptstore.EventSourcePayload, PayloadJSON: raw, Destinations: []string{"ws"},
		})
		return appendErr
	}); err != nil {
		return err
	}
	return nil
}

func appendTranscriptFixtureCheckpoint(
	ctx context.Context,
	repository *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	clientID string,
	phase transcriptstore.RunnerPhase,
	payload map[string]any,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := retryTranscriptFixtureContention(ctx, "append_transcript_fixture_checkpoint", func() error {
		_, _, _, appendErr := repository.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: clientID, Phase: phase, Resumable: true,
			PayloadJSON: raw, Destinations: []string{"ws"},
		})
		return appendErr
	}); err != nil {
		return err
	}
	return nil
}

func transcriptStreamFixtureRunFromClaim(
	frameID, runID string,
	claim transcriptstore.RunnerClaim,
) transcriptStreamFixtureRun {
	return transcriptStreamFixtureRun{
		FrameID: frameID, RunID: runID, StreamUID: claim.StreamUID, OwnerID: claim.OwnerID,
		RunnerID: claim.RunnerID, Attempt: claim.Attempt, ClaimToken: claim.ClaimToken,
		ClaimedInputRevision: claim.ClaimedInputRevision, ResumeSource: claim.ResumeSource,
		ResumeCheckpoint: claim.ResumeCheckpoint, ResumeCheckpointAttempt: claim.ResumeCheckpointAttempt,
		ClaimedAt: claim.ClaimedAt, ExpiresAt: claim.ExpiresAt,
	}
}

func (run transcriptStreamFixtureRun) claim() transcriptstore.RunnerClaim {
	return transcriptstore.RunnerClaim{
		StreamUID: run.StreamUID, OwnerID: run.OwnerID, RunnerID: run.RunnerID,
		Attempt: run.Attempt, ClaimToken: run.ClaimToken, ClaimedInputRevision: run.ClaimedInputRevision,
		ResumeSource: run.ResumeSource, ResumeCheckpoint: run.ResumeCheckpoint,
		ResumeCheckpointAttempt: run.ResumeCheckpointAttempt, ClaimedAt: run.ClaimedAt, ExpiresAt: run.ExpiresAt,
	}
}
