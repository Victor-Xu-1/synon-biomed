package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

const (
	transcriptWebIncrementalCheckpointVersion = 5
	transcriptWebIncrementalBatch             = 256
)

type transcriptWebIncrementalAssistantCheckpoint struct {
	Resumable              bool                                                 `json:"resumable"`
	TerminalThroughAttempt int64                                                `json:"terminal_through_attempt,omitempty"`
	SeenAttempts           []int64                                              `json:"seen_attempts,omitempty"`
	SeenIdentities         []string                                             `json:"seen_identities,omitempty"`
	Attempts               []transcriptWebIncrementalAssistantAttemptCheckpoint `json:"attempts,omitempty"`
}

type transcriptWebIncrementalAssistantAttemptCheckpoint struct {
	Attempt                    int64                                       `json:"attempt"`
	CurrentIdentity            string                                      `json:"current_identity,omitempty"`
	SegmentIdentities          []string                                    `json:"segment_identities,omitempty"`
	SplitPending               bool                                        `json:"split_pending,omitempty"`
	CurrentMessage             *transcriptstore.TranscriptWebMessageRecord `json:"current_message,omitempty"`
	CurrentIsSyntheticProgress bool                                        `json:"current_is_synthetic_progress,omitempty"`
	Rollback                   *transcriptWebIncrementalAssistantRollback  `json:"rollback,omitempty"`
}

type transcriptWebIncrementalAssistantRollback struct {
	BeforeAttempt  transcriptWebIncrementalAssistantAttemptCheckpoint `json:"before_attempt"`
	SeenIdentities []string                                           `json:"seen_identities"`
}

type transcriptWebIncrementalDisposition string

const (
	transcriptWebIncrementalAppliedReady        transcriptWebIncrementalDisposition = "applied_ready"
	transcriptWebIncrementalAppliedBuilding     transcriptWebIncrementalDisposition = "applied_building"
	transcriptWebIncrementalFallbackNoState     transcriptWebIncrementalDisposition = "fallback_no_state"
	transcriptWebIncrementalFallbackState       transcriptWebIncrementalDisposition = "fallback_state_not_resumable"
	transcriptWebIncrementalFallbackBranch      transcriptWebIncrementalDisposition = "fallback_branch_generation_changed"
	transcriptWebIncrementalFallbackDirty       transcriptWebIncrementalDisposition = "fallback_source_dirty"
	transcriptWebIncrementalFallbackSource      transcriptWebIncrementalDisposition = "fallback_source_revision_changed"
	transcriptWebIncrementalFallbackNoAdvance   transcriptWebIncrementalDisposition = "fallback_not_append"
	transcriptWebIncrementalFallbackLegacy      transcriptWebIncrementalDisposition = "fallback_legacy_checkpoint"
	transcriptWebIncrementalFallbackTool        transcriptWebIncrementalDisposition = "fallback_tool_state"
	transcriptWebIncrementalFallbackAssistant   transcriptWebIncrementalDisposition = "fallback_assistant_state"
	transcriptWebIncrementalFallbackAskUser     transcriptWebIncrementalDisposition = "fallback_ask_user_state"
	transcriptWebIncrementalFallbackInput       transcriptWebIncrementalDisposition = "fallback_user_input_response"
	transcriptWebIncrementalFallbackHistory     transcriptWebIncrementalDisposition = "fallback_imported_history"
	transcriptWebIncrementalFallbackUnsupported transcriptWebIncrementalDisposition = "fallback_unsupported_event"
)

type transcriptWebIncrementalResult struct {
	Disposition                transcriptWebIncrementalDisposition
	EventsRead                 int
	MessagesWritten            int
	ThroughPublicationSequence int64
}

var errTranscriptWebIncrementalAssistantFallback = errors.New("Transcript Web assistant increment cannot preserve full projection semantics")

type transcriptWebIncrementalAssistantReducer struct {
	attempts               map[int64]*transcriptWebIncrementalAssistantAttemptCheckpoint
	seenAttempts           map[int64]bool
	seenIdentities         map[string]bool
	changed                map[int]transcriptstore.TranscriptWebMessageRecord
	baseMessageCount       int
	messageCount           int
	visibleCount           int
	artifactCount          int
	tailReplaceFrom        int
	terminalThroughAttempt int64
	sessionID              string
	branchID               string
}

func (result transcriptWebIncrementalResult) applied() bool {
	return result.Disposition == transcriptWebIncrementalAppliedReady ||
		result.Disposition == transcriptWebIncrementalAppliedBuilding
}

func (s *Server) tryIncrementalTranscriptWebReadModel(
	ctx context.Context,
	stream transcriptstore.Stream,
	work transcriptstore.TranscriptWebProjectionWork,
) (transcriptWebIncrementalResult, error) {
	fallback := func(disposition transcriptWebIncrementalDisposition) transcriptWebIncrementalResult {
		return transcriptWebIncrementalResult{Disposition: disposition}
	}
	fence, err := s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
		ctx, work.OwnerID, work.StreamUID, work.BranchID,
	)
	if err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	if fence.SessionID != work.SessionID || fence.BranchGeneration != work.BranchGeneration ||
		fence.ThroughPublicationSequence != work.ThroughPublicationSequence ||
		fence.SourceRevision != work.SourceRevision {
		return transcriptWebIncrementalResult{}, transcriptstore.ErrBranchStateStale
	}
	if !fence.StateFound {
		return fallback(transcriptWebIncrementalFallbackNoState), nil
	}
	if fence.StateStatus != "ready" && fence.StateStatus != "building" {
		return fallback(transcriptWebIncrementalFallbackState), nil
	}
	if fence.StateBranchGeneration != fence.BranchGeneration {
		return fallback(transcriptWebIncrementalFallbackBranch), nil
	}
	if work.DirtyFirstAffectedOrdinal > 0 || work.DirtyReasonMask > 0 {
		return fallback(transcriptWebIncrementalFallbackDirty), nil
	}
	if fence.StateSourceRevision != fence.SourceRevision {
		return fallback(transcriptWebIncrementalFallbackSource), nil
	}
	if fence.StateThroughPublicationSequence >= fence.ThroughPublicationSequence {
		return fallback(transcriptWebIncrementalFallbackNoAdvance), nil
	}
	var checkpoint transcriptWebProjectorCheckpointV1
	if len(fence.StateProjectorStateJSON) == 0 ||
		json.Unmarshal(fence.StateProjectorStateJSON, &checkpoint) != nil ||
		checkpoint.Version != transcriptWebIncrementalCheckpointVersion ||
		(checkpoint.Mode != "full_rebuild" && checkpoint.Mode != "incremental_append") ||
		checkpoint.BranchGeneration != fence.StateBranchGeneration ||
		checkpoint.ThroughPublicationSequence != fence.StateThroughPublicationSequence ||
		checkpoint.SourceRevision != fence.StateSourceRevision ||
		checkpoint.MessageCount != fence.StateMessageCount ||
		checkpoint.EventChainSHA256 != fence.StateSourceChainSHA256 ||
		!validTranscriptWebSourceEventChain(checkpoint.EventChainSHA256) {
		return fallback(transcriptWebIncrementalFallbackLegacy), nil
	}

	page, err := s.transcriptStore.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: work.OwnerID,
		BranchID: work.BranchID, BranchGeneration: work.BranchGeneration,
		AfterPublicationSequence:   fence.StateThroughPublicationSequence,
		ThroughPublicationSequence: work.ThroughPublicationSequence,
		Limit:                      transcriptWebIncrementalBatch,
	})
	if err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	result := transcriptWebIncrementalResult{EventsRead: len(page)}
	if len(page) == 0 {
		return result, transcriptstore.ErrEventConflict
	}
	for index, projected := range page {
		if projected.Event.Type == "content_reset" {
			page = page[:index+1]
			break
		}
	}
	if supersedes, err := s.transcriptWebPageSupersedesTerminal(ctx, stream, checkpoint.Assistant, page); err != nil {
		return transcriptWebIncrementalResult{}, err
	} else if supersedes {
		result.Disposition = transcriptWebIncrementalFallbackAssistant
		return result, nil
	}
	assistantPage := false
	for _, projected := range page {
		switch projected.Event.Type {
		case "content_delta", "content_reset", "assistant_message", "runner_finished":
			assistantPage = true
			continue
		case "runner_checkpoint":
			if transcriptWebIncrementalProviderBoundary(projected) {
				continue
			}
		}
		if disposition := classifyTranscriptWebIncrementalEvent(projected); disposition != "" {
			result.Disposition = disposition
			return result, nil
		}
	}
	result.EventsRead = len(page)
	if assistantPage {
		if !checkpoint.Assistant.Resumable {
			result.Disposition = transcriptWebIncrementalFallbackAssistant
			return result, nil
		}
		return s.applyIncrementalTranscriptWebAssistantPage(ctx, stream, work, fence, checkpoint, page)
	}

	records := make([]transcriptstore.TranscriptWebMessageRecord, 0, len(page))
	references := make([]transcriptstore.TranscriptWebMessageArtifactReference, 0)
	for index, projected := range page {
		record, eventReferences, err := buildIncrementalTranscriptWebUserMessage(
			projected, stream.SessionID, work.BranchID,
			fence.StateMessageCount+index, fence.VisibleMessageCount+index,
		)
		if err != nil {
			return transcriptWebIncrementalResult{}, err
		}
		records = append(records, record)
		references = append(references, eventReferences...)
	}
	lastThrough := page[len(page)-1].Event.PublicationSeq
	if lastThrough <= fence.StateThroughPublicationSequence || lastThrough > work.ThroughPublicationSequence {
		return transcriptWebIncrementalResult{}, transcriptstore.ErrEventConflict
	}
	ready := lastThrough == work.ThroughPublicationSequence
	if !ready && len(page) < transcriptWebIncrementalBatch {
		return transcriptWebIncrementalResult{}, transcriptstore.ErrEventConflict
	}
	nextChain, err := extendTranscriptWebSourceEventChain(fence.StateSourceChainSHA256, page)
	if err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	messageCount := fence.StateMessageCount + len(records)
	referenceCount := fence.StateArtifactReferenceCount + len(references)
	checkpointJSON, err := json.Marshal(transcriptWebProjectorCheckpointV1{
		Version: transcriptWebIncrementalCheckpointVersion, Mode: "incremental_append",
		BranchGeneration:           work.BranchGeneration,
		ThroughPublicationSequence: lastThrough,
		SourceRevision:             work.SourceRevision, MessageCount: messageCount,
		EventChainSHA256: nextChain,
		Assistant:        checkpoint.Assistant,
	})
	if err != nil {
		return transcriptWebIncrementalResult{}, fmt.Errorf("encode incremental Transcript Web checkpoint: %w", err)
	}
	status := "building"
	result.Disposition = transcriptWebIncrementalAppliedBuilding
	if ready {
		status = "ready"
		result.Disposition = transcriptWebIncrementalAppliedReady
	}
	state := transcriptstore.TranscriptWebProjectionState{
		StreamUID: work.StreamUID, BranchID: work.BranchID,
		BranchGeneration:           work.BranchGeneration,
		ProjectorVersion:           transcriptstore.TranscriptWebProjectorVersion,
		ProjectionRevision:         fence.StateProjectionRevision + 1,
		ThroughPublicationSequence: lastThrough, SourceRevision: work.SourceRevision,
		MessageCount: messageCount, VisibleMessageCount: fence.VisibleMessageCount + len(records),
		MessageArtifactReferenceCount: referenceCount,
		ProjectorStateJSON:            checkpointJSON,
		ProjectorStateSHA256:          transcriptstore.TranscriptWebSHA256(checkpointJSON),
		SourceChainSHA256:             nextChain, Status: status, UpdatedAt: time.Now().UTC(),
	}
	if err := s.transcriptWebReadModel.ApplyTranscriptWebProjection(ctx, transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: work.OwnerID, State: state,
		ExpectedBranchGeneration:   fence.StateBranchGeneration,
		ExpectedThroughPublication: fence.StateThroughPublicationSequence,
		ExpectedProjectionRevision: fence.StateProjectionRevision,
		ExpectedSourceRevision:     fence.StateSourceRevision,
		ExpectedSourceChainSHA256:  fence.StateSourceChainSHA256,
		ReplaceAll:                 false, Messages: records, ArtifactReferences: references,
	}); err != nil {
		return transcriptWebIncrementalResult{}, err
	}
	result.MessagesWritten = len(records)
	result.ThroughPublicationSequence = lastThrough
	return result, nil
}

func classifyTranscriptWebIncrementalEvent(projected transcriptstore.ProjectedEvent) transcriptWebIncrementalDisposition {
	switch projected.Event.Type {
	case "user_message":
		if projected.Event.RunnerAttempt == nil {
			return ""
		}
		return transcriptWebIncrementalFallbackUnsupported
	case "user_input_response":
		return transcriptWebIncrementalFallbackInput
	case "history_user_message", "history_assistant_message":
		return transcriptWebIncrementalFallbackHistory
	case "content_delta", "content_reset", "assistant_message", "runner_finished":
		return transcriptWebIncrementalFallbackAssistant
	case "runner_checkpoint", transcriptstore.ToolOperationObservationEventType:
		return transcriptWebIncrementalFallbackTool
	case transcriptstore.AskUserPromptEventType, transcriptstore.AskUserResultEventType:
		return transcriptWebIncrementalFallbackAskUser
	default:
		return transcriptWebIncrementalFallbackUnsupported
	}
}
