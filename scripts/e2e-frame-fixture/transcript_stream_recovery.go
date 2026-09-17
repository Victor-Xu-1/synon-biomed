package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func beginTranscriptStreamingRecovery(
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
	runID := input.Run.RunID
	if err := appendTranscriptFixtureEvent(ctx, repository, claim,
		"e2e-recovery-intro:"+runID, "content_delta", map[string]any{
			"text":              input.Recovery.Intro,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	firstCallID := "e2e-recovery-first:" + runID
	firstInput := map[string]any{"code": "score_candidates(primary_source=True)"}
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-recovery-first-start:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": firstCallID,
			"toolName": "python", "toolInput": firstInput, "message": input.Recovery.First,
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-recovery-first-failed:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "failed", "toolPhase": "failed", "toolCallId": firstCallID,
			"toolName": "python", "toolInput": firstInput, "message": input.Recovery.First,
			"toolResult": map[string]any{
				"error":            "upstream timeout",
				"partial_evidence": "candidate 1/3 retained",
			},
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	if err := appendTranscriptFixtureEvent(ctx, repository, claim,
		"e2e-recovery-explanation:"+runID, "content_delta", map[string]any{
			"text":              input.Recovery.Recovery,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-recovery-second-start:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": "e2e-recovery-second:" + runID,
			"toolName": "python", "toolInput": map[string]any{"code": "score_candidates(fallback_source=True)"},
			"message": input.Recovery.Second,
		}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": 5}, nil
}

func completeTranscriptStreamingRecovery(
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
	runID := input.Run.RunID
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-recovery-second-completed:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "completed", "toolPhase": "completed", "toolCallId": "e2e-recovery-second:" + runID,
			"toolName": "python", "toolInput": map[string]any{"code": "score_candidates(fallback_source=True)"},
			"message":    input.Recovery.Second,
			"toolResult": "candidate 1/3 retained\ncandidate 2/3\ncandidate 3/3\ncomplete",
		}); err != nil {
		return nil, err
	}
	if err := appendTranscriptFixtureEvent(ctx, repository, claim,
		"e2e-recovery-assistant:"+runID, "assistant_message", map[string]any{
			"text":              input.Recovery.Final,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
		}); err != nil {
		return nil, fmt.Errorf("append recovery assistant message: %w", err)
	}
	finishPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "streaming recovery fixture completed",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(3, ""),
	})
	if err != nil {
		return nil, err
	}
	if err := retryTranscriptFixtureContention(ctx, "finish_recovery_runner", func() error {
		_, _, _, finishErr := repository.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
			Claim: claim, ClientMessageID: "e2e-recovery-finish:" + runID, Status: "completed",
			PayloadJSON: finishPayload, Destinations: []string{"ws"},
		})
		return finishErr
	}); err != nil {
		return nil, fmt.Errorf("finish recovery transcript runner: %w", err)
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": 3}, nil
}
