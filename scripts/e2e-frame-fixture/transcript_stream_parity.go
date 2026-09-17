package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func beginTranscriptStreamingParity(
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
	thinkingPayload := map[string]any{
		"text": input.Copy.Thinking, "block_type": "thinking",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	}
	if err := appendTranscriptFixtureEvent(ctx, repository, claim,
		"e2e-parity-thinking:"+runID, "content_delta", thinkingPayload); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	searchInput := map[string]any{"query": "CRBN co-crystal structure"}
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-parity-search-start:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": "e2e-parity-search:" + runID,
			"toolName": "web_search", "toolInput": searchInput, "message": input.Copy.Search,
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-parity-search-completed:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "completed", "toolPhase": "completed", "toolCallId": "e2e-parity-search:" + runID,
			"toolName": "web_search", "toolInput": searchInput, "message": input.Copy.Search,
			"toolResult": "9FJX · 2.7 Å",
		}); err != nil {
		return nil, err
	}
	time.Sleep(transcriptFixtureStreamDelay)
	if err := appendTranscriptFixtureCheckpoint(ctx, repository, claim,
		"e2e-parity-compute-start:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": "e2e-parity-compute:" + runID,
			"toolName": "python", "toolInput": map[string]any{"code": "score_candidates()"},
			"message": input.Copy.Compute,
		}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": 4}, nil
}

func completeTranscriptStreamingParity(
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
		"e2e-parity-compute-completed:"+runID, transcriptstore.RunnerPhaseExecuting, map[string]any{
			"status": "completed", "toolPhase": "completed", "toolCallId": "e2e-parity-compute:" + runID,
			"toolName": "python", "toolInput": map[string]any{"code": "score_candidates()"},
			"message":    input.Copy.Compute,
			"toolResult": "candidate 1/3\ncandidate 2/3\ncandidate 3/3\ncomplete",
		}); err != nil {
		return nil, err
	}
	refs := make([]transcriptstore.ArtifactReferenceInput, len(input.ArtifactRefs))
	for index, ref := range input.ArtifactRefs {
		refs[index] = transcriptstore.ArtifactReferenceInput{
			ArtifactID: ref.ArtifactID, VersionID: ref.VersionID, Relation: transcriptstore.ArtifactRelationProduced,
		}
	}
	payload, err := json.Marshal(map[string]any{
		"text": input.Copy.Final, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if err != nil {
		return nil, err
	}
	if err := retryTranscriptFixtureContention(ctx, "append_parity_assistant", func() error {
		_, _, _, appendErr := repository.AppendAssistantEventWithArtifacts(ctx, transcriptstore.AppendAssistantEventWithArtifactsInput{
			Claim: claim, ClientMessageID: "e2e-parity-assistant:" + runID,
			Source: transcriptstore.EventSourcePayload, PayloadJSON: payload,
			Destinations: []string{"ws"}, References: refs,
		})
		return appendErr
	}); err != nil {
		return nil, fmt.Errorf("append parity assistant message: %w", err)
	}
	finishPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "streaming parity fixture completed",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if err != nil {
		return nil, err
	}
	if err := retryTranscriptFixtureContention(ctx, "finish_parity_runner", func() error {
		_, _, _, finishErr := repository.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
			Claim: claim, ClientMessageID: "e2e-parity-finish:" + runID, Status: "completed",
			PayloadJSON: finishPayload, Destinations: []string{"ws"},
		})
		return finishErr
	}); err != nil {
		return nil, fmt.Errorf("finish parity transcript runner: %w", err)
	}
	return map[string]any{"ok": true, "frameId": frameID, "eventCount": 3}, nil
}
