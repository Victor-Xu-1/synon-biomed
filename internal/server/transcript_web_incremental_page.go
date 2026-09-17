package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

func (s *Server) applyIncrementalTranscriptWebAssistantPage(
	ctx context.Context,
	stream transcriptstore.Stream,
	work transcriptstore.TranscriptWebProjectionWork,
	fence transcriptstore.TranscriptWebProjectionFence,
	checkpoint transcriptWebProjectorCheckpointV1,
	page []transcriptstore.ProjectedEvent,
) (transcriptWebIncrementalResult, error) {
	result := transcriptWebIncrementalResult{EventsRead: len(page)}
	reducer, err := newTranscriptWebIncrementalAssistantReducer(checkpoint.Assistant, fence, stream.SessionID, work.BranchID)
	if err != nil {
		result.Disposition = transcriptWebIncrementalFallbackLegacy
		return result, nil
	}
	for _, projected := range page {
		if err := ctx.Err(); err != nil {
			return transcriptWebIncrementalResult{}, err
		}
		switch projected.Event.Type {
		case "user_message":
			record, _, err := buildIncrementalTranscriptWebUserMessage(
				projected, stream.SessionID, work.BranchID, reducer.messageCount, reducer.visibleCount,
			)
			if err != nil {
				return transcriptWebIncrementalResult{}, err
			}
			if err := reducer.appendRecord(record); err != nil {
				return transcriptWebIncrementalResult{}, err
			}
		case "content_delta", "content_reset", "assistant_message", "runner_finished":
			if err := reducer.consumeAssistantEvent(ctx, s, stream, projected); err != nil {
				if errors.Is(err, errTranscriptWebIncrementalAssistantFallback) {
					result.Disposition = transcriptWebIncrementalFallbackAssistant
					return result, nil
				}
				return transcriptWebIncrementalResult{}, err
			}
		case "runner_checkpoint":
			if !transcriptWebIncrementalProviderBoundary(projected) {
				result.Disposition = transcriptWebIncrementalFallbackTool
				return result, nil
			}
		default:
			result.Disposition = transcriptWebIncrementalFallbackUnsupported
			return result, nil
		}
	}
	lastThrough := page[len(page)-1].Event.PublicationSeq
	if lastThrough <= fence.StateThroughPublicationSequence || lastThrough > work.ThroughPublicationSequence {
		return transcriptWebIncrementalResult{}, transcriptstore.ErrEventConflict
	}
	ready := lastThrough == work.ThroughPublicationSequence
	if !ready && len(page) < transcriptWebIncrementalBatch && page[len(page)-1].Event.Type != "content_reset" {
		return transcriptWebIncrementalResult{}, transcriptstore.ErrEventConflict
	}
	nextChain, err := extendTranscriptWebSourceEventChain(fence.StateSourceChainSHA256, page)
	if err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	assistant, messages, references, err := reducer.finish()
	if err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	checkpointJSON, err := json.Marshal(transcriptWebProjectorCheckpointV1{
		Version: transcriptWebIncrementalCheckpointVersion, Mode: "incremental_append",
		BranchGeneration:           work.BranchGeneration,
		ThroughPublicationSequence: lastThrough,
		SourceRevision:             work.SourceRevision,
		MessageCount:               reducer.messageCount,
		EventChainSHA256:           nextChain,
		Assistant:                  assistant,
	})
	if err != nil {
		return transcriptWebIncrementalResult{}, fmt.Errorf("encode assistant Transcript Web checkpoint: %w", err)
	}
	status := "building"
	result.Disposition = transcriptWebIncrementalAppliedBuilding
	if ready {
		status = "ready"
		result.Disposition = transcriptWebIncrementalAppliedReady
	}
	state := transcriptstore.TranscriptWebProjectionState{
		StreamUID: work.StreamUID, BranchID: work.BranchID,
		BranchGeneration:              work.BranchGeneration,
		ProjectorVersion:              transcriptstore.TranscriptWebProjectorVersion,
		ProjectionRevision:            fence.StateProjectionRevision + 1,
		ThroughPublicationSequence:    lastThrough,
		SourceRevision:                work.SourceRevision,
		MessageCount:                  reducer.messageCount,
		VisibleMessageCount:           reducer.visibleCount,
		MessageArtifactReferenceCount: reducer.artifactCount,
		ProjectorStateJSON:            checkpointJSON,
		ProjectorStateSHA256:          transcriptstore.TranscriptWebSHA256(checkpointJSON),
		SourceChainSHA256:             nextChain,
		Status:                        status,
		UpdatedAt:                     time.Now().UTC(),
	}
	if err := s.transcriptWebReadModel.ApplyTranscriptWebProjection(ctx, transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: work.OwnerID, State: state,
		ExpectedBranchGeneration:   fence.StateBranchGeneration,
		ExpectedThroughPublication: fence.StateThroughPublicationSequence,
		ExpectedProjectionRevision: fence.StateProjectionRevision,
		ExpectedSourceRevision:     fence.StateSourceRevision,
		ExpectedSourceChainSHA256:  fence.StateSourceChainSHA256,
		TailReplaceFromOrdinal:     reducer.tailReplaceFrom,
		Messages:                   messages,
		ArtifactReferences:         references,
	}); err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	result.MessagesWritten = len(messages)
	result.ThroughPublicationSequence = lastThrough
	return result, nil
}

func transcriptWebIncrementalProviderBoundary(projected transcriptstore.ProjectedEvent) bool {
	if projected.Event.Type != "runner_checkpoint" || len(projected.ResolvedPayloadJSON) == 0 {
		return false
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(projected.ResolvedPayloadJSON))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return false
	}
	if _, present := payload["provider_continuation"]; present {
		if !mapHasOnlyKeys(payload, "status", "detail", "provider_continuation") ||
			strings.TrimSpace(stringValue(payload["status"])) != "running" {
			return false
		}
		_, parsed, err := parseProviderContinuationV1(payload)
		return parsed && err == nil
	}
	if !mapHasOnlyKeys(payload, "status", "reason_code", "resume_detail", "recovery_contract_revision", "auto_resume") ||
		strings.TrimSpace(stringValue(payload["status"])) != "interrupted" {
		return false
	}
	switch strings.TrimSpace(stringValue(payload["reason_code"])) {
	case "provider_stream_interrupted", "provider_stream_no_progress",
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerToolRoundNoProgressReasonCode, sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerToolRoundLimitReasonCode:
		return true
	default:
		return false
	}
}

func mapHasOnlyKeys(value map[string]any, allowed ...string) bool {
	if len(value) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range value {
		if _, ok := set[key]; !ok {
			return false
		}
	}
	return true
}
