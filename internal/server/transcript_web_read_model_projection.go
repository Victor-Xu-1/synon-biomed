package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type transcriptWebProjectionSourceMetadata struct {
	coordinates []canonicalReadCursorCoordinate
	ranges      []transcriptWebPublicationRange
	eventChain  string
	assistant   transcriptWebIncrementalAssistantCheckpoint
}

type transcriptWebProjectionSourceDigest struct {
	EventID         int64  `json:"event_id"`
	PublicationSeq  int64  `json:"publication_seq"`
	ClientMessageID string `json:"client_message_id"`
	Type            string `json:"type"`
	Source          string `json:"source"`
	RunnerAttempt   *int64 `json:"runner_attempt,omitempty"`
	PayloadSHA256   string `json:"payload_sha256"`
	FrameEventID    string `json:"frame_event_id,omitempty"`
}

type transcriptWebProjectorCheckpointV1 struct {
	Version                    int                                         `json:"version"`
	Mode                       string                                      `json:"mode"`
	BranchGeneration           int64                                       `json:"branch_generation"`
	ThroughPublicationSequence int64                                       `json:"through_publication_sequence"`
	SourceRevision             int64                                       `json:"source_revision"`
	MessageCount               int                                         `json:"message_count"`
	EventChainSHA256           string                                      `json:"event_chain_sha256"`
	Assistant                  transcriptWebIncrementalAssistantCheckpoint `json:"assistant"`
	ErrorCode                  string                                      `json:"error_code,omitempty"`
}

func (s *Server) runTranscriptWebReadModelCycle(ctx context.Context) error {
	return s.runTranscriptWebReadModelBatch(ctx, transcriptWebBatch)
}

// runTranscriptWebReadModelBatch advances a bounded number of background
// projections. The delivery coordinator deliberately uses a one-item batch so
// a large, unrelated history repair cannot hold the realtime wake loop for
// dozens of incremental projector chunks. Explicit repair/tests retain the
// normal transcriptWebBatch behavior through runTranscriptWebReadModelCycle.
func (s *Server) runTranscriptWebReadModelBatch(ctx context.Context, limit int) error {
	if s == nil || s.transcriptWebReadModel == nil || s.transcriptStore == nil || s.transcriptContractErr != nil {
		return nil
	}
	if limit <= 0 {
		return errors.New("positive Transcript Web projection batch is required")
	}
	// The execution limit is not a candidate-scan limit. A skipped terminal
	// quarantine or a quarantine still inside its retry backoff must not occupy
	// the only coordinator slot and starve later active conversations forever.
	// Scan one bounded metadata page, while still rebuilding at most limit
	// projections in this pass.
	candidateLimit := max(limit, transcriptWebBatch)
	owners, err := s.transcriptWebReadModel.ListTranscriptWebProjectionOwners(ctx, candidateLimit)
	if err != nil {
		return err
	}
	attempted := 0
	for _, ownerID := range owners {
		work, err := s.transcriptWebReadModel.ListTranscriptWebProjectionWork(ctx, ownerID, candidateLimit)
		if err != nil {
			return err
		}
		for _, item := range work {
			if item.ProjectionStatus == "quarantined" && s.transcriptWebProjectionFrameTerminal(item.SessionID) {
				// A cancelled/failed Frame is no longer an active user path. Keep
				// its durable source and quarantine evidence available, but do not
				// let an unrecoverable historical projection consume the active
				// projector on every daemon tick. An explicit Frame resume changes
				// the status back to an active state and makes the same projection
				// chain eligible again.
				continue
			}
			if item.ProjectionStatus == "quarantined" &&
				!s.transcriptWebProjectionRetryDue(ctx, item) {
				continue
			}
			attempted++
			if err := s.rebuildTranscriptWebReadModel(ctx, item); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
					errors.Is(err, transcriptstore.ErrBranchStateStale) ||
					errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale) {
					continue
				}
				if quarantineErr := s.quarantineTranscriptWebReadModel(ctx, item, err); quarantineErr != nil &&
					!errors.Is(quarantineErr, transcriptstore.ErrBranchStateStale) &&
					!errors.Is(quarantineErr, transcriptstore.ErrTranscriptWebProjectionStale) {
					return errors.Join(err, quarantineErr)
				}
				s.noteTranscriptWebProjectionRetry(item.StreamUID)
			}
			if attempted >= limit {
				return nil
			}
		}
	}
	return nil
}

func (s *Server) transcriptWebProjectionFrameTerminal(sessionID string) bool {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	frame, found, err := s.workspaceStore.GetCompatibilityFrame(sessionID)
	if err != nil || !found {
		// Unknown state must remain fail-open for projection repair; only an
		// explicitly terminal Frame can suppress an automatic retry.
		return false
	}
	switch strings.ToLower(strings.TrimSpace(frame.Status)) {
	case "cancelled", "canceled", "failed", "orphaned":
		return true
	default:
		return false
	}
}

const transcriptWebProjectionRetryInterval = 30 * time.Second

// transcriptWebProjectionRetryDue reports whether a quarantined projection is
// eligible for one more automatic rebuild attempt. Time alone never makes an
// unchanged deterministic source retryable: the canonical source coordinates,
// branch generation, or projector version must also advance. This preserves
// automatic recovery without turning one corrupt historical event into an
// unbounded background loop.
func (s *Server) transcriptWebProjectionRetryDue(
	ctx context.Context,
	work transcriptstore.TranscriptWebProjectionWork,
) bool {
	s.projectionRetryMu.Lock()
	if s.projectionRetryAt == nil {
		s.projectionRetryAt = map[string]time.Time{}
	}
	next := s.projectionRetryAt[work.StreamUID]
	s.projectionRetryMu.Unlock()
	if !next.IsZero() && time.Since(next) < transcriptWebProjectionRetryInterval {
		return false
	}
	fence, err := s.transcriptWebReadModel.GetTranscriptWebProjectionUpgradeFence(
		ctx, work.OwnerID, work.StreamUID, work.BranchID,
	)
	if err != nil {
		return false
	}
	if !fence.StateFound || fence.StateProjectorVersion != transcriptstore.TranscriptWebProjectorVersion {
		return true
	}
	return work.BranchGeneration != fence.StateBranchGeneration ||
		work.ThroughPublicationSequence != fence.StateThroughPublicationSequence ||
		work.SourceRevision != fence.StateSourceRevision
}

func (s *Server) noteTranscriptWebProjectionRetry(streamUID string) {
	s.projectionRetryMu.Lock()
	defer s.projectionRetryMu.Unlock()
	if s.projectionRetryAt == nil {
		s.projectionRetryAt = map[string]time.Time{}
	}
	s.projectionRetryAt[streamUID] = time.Now()
}

func (s *Server) rebuildTranscriptWebReadModel(
	ctx context.Context,
	work transcriptstore.TranscriptWebProjectionWork,
) error {
	stream, err := s.transcriptStore.GetStream(ctx, work.StreamUID, work.OwnerID)
	if err != nil {
		return err
	}
	if stream.SessionID != work.SessionID || stream.Kind != transcriptstore.StreamKindFrameRef {
		return transcriptstore.ErrEventConflict
	}
	if work.Reason != "projection_projector_version_stale" {
		incremental, err := s.tryIncrementalTranscriptWebReadModel(ctx, stream, work)
		if err != nil {
			return fmt.Errorf("incremental projection: %w", err)
		}
		if incremental.applied() {
			return nil
		}
	}
	messages, snapshot, err := s.projectTranscriptWebHistory(
		ctx, stream, work.OwnerID, work.SessionID, work.BranchID,
	)
	if err != nil {
		return fmt.Errorf("full history projection: %w", err)
	}
	if snapshot.StreamUID != work.StreamUID || snapshot.BranchID != work.BranchID ||
		snapshot.BranchGeneration != work.BranchGeneration ||
		snapshot.ThroughPublicationSequence != work.ThroughPublicationSequence {
		return transcriptstore.ErrBranchStateStale
	}
	metadata, err := s.projectTranscriptWebSourceMetadata(ctx, stream, work.OwnerID, work.SessionID, snapshot)
	if err != nil {
		return fmt.Errorf("source metadata: %w", err)
	}
	records, references, err := buildTranscriptWebMessageRecords(messages, metadata)
	if err != nil {
		return fmt.Errorf("build records: %w", err)
	}
	assistant, err := transcriptWebAttachIncrementalAssistantRecords(metadata.assistant, records)
	if err != nil {
		return err
	}
	var fence transcriptstore.TranscriptWebProjectionFence
	if work.Reason == "projection_projector_version_stale" {
		fence, err = s.transcriptWebReadModel.GetTranscriptWebProjectionUpgradeFence(
			ctx, work.OwnerID, work.StreamUID, work.BranchID,
		)
	} else {
		fence, err = s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
			ctx, work.OwnerID, work.StreamUID, work.BranchID,
		)
	}
	if err != nil {
		return err
	}
	if fence.SessionID != work.SessionID || fence.BranchGeneration != snapshot.BranchGeneration ||
		fence.ThroughPublicationSequence != snapshot.ThroughPublicationSequence ||
		fence.SourceRevision != work.SourceRevision {
		return transcriptstore.ErrBranchStateStale
	}
	stateJSON, err := json.Marshal(transcriptWebProjectorCheckpointV1{
		Version: transcriptWebIncrementalCheckpointVersion, Mode: "full_rebuild",
		BranchGeneration:           snapshot.BranchGeneration,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
		SourceRevision:             fence.SourceRevision, MessageCount: len(records),
		EventChainSHA256: metadata.eventChain,
		Assistant:        assistant,
	})
	if err != nil {
		return fmt.Errorf("encode Transcript Web projector checkpoint: %w", err)
	}
	sourceChain, err := transcriptWebProjectionSourceChain(metadata.eventChain, snapshot, fence.SourceRevision, records)
	if err != nil {
		return err
	}
	projectionRevision := int64(1)
	if fence.StateFound {
		projectionRevision = fence.StateProjectionRevision + 1
	}
	state := transcriptstore.TranscriptWebProjectionState{
		StreamUID: work.StreamUID, BranchID: work.BranchID,
		BranchGeneration: snapshot.BranchGeneration,
		ProjectorVersion: transcriptstore.TranscriptWebProjectorVersion, ProjectionRevision: projectionRevision,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
		SourceRevision:             fence.SourceRevision, MessageCount: len(records), VisibleMessageCount: len(records),
		MessageArtifactReferenceCount: len(references), ProjectorStateJSON: stateJSON,
		ProjectorStateSHA256: transcriptstore.TranscriptWebSHA256(stateJSON),
		SourceChainSHA256:    sourceChain, Status: "ready", UpdatedAt: time.Now().UTC(),
	}
	err = s.transcriptWebReadModel.ApplyTranscriptWebProjection(ctx, transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: work.OwnerID, State: state,
		ExpectedBranchGeneration:   fence.StateBranchGeneration,
		ExpectedThroughPublication: fence.StateThroughPublicationSequence,
		ExpectedProjectionRevision: fence.StateProjectionRevision,
		ExpectedSourceRevision:     fence.StateSourceRevision,
		ExpectedSourceChainSHA256:  fence.StateSourceChainSHA256,
		ReplaceAll:                 true, Messages: records, ArtifactReferences: references,
	})
	return err
}

func (s *Server) quarantineTranscriptWebReadModel(
	ctx context.Context,
	work transcriptstore.TranscriptWebProjectionWork,
	cause error,
) error {
	var fence transcriptstore.TranscriptWebProjectionFence
	var err error
	if work.Reason == "projection_projector_version_stale" {
		fence, err = s.transcriptWebReadModel.GetTranscriptWebProjectionUpgradeFence(
			ctx, work.OwnerID, work.StreamUID, work.BranchID,
		)
	} else {
		fence, err = s.transcriptWebReadModel.GetTranscriptWebProjectionFence(
			ctx, work.OwnerID, work.StreamUID, work.BranchID,
		)
	}
	if err != nil {
		return err
	}
	code := transcriptWebProjectionErrorCode(cause)
	stateJSON, err := json.Marshal(transcriptWebProjectorCheckpointV1{
		Version: transcriptstore.TranscriptWebProjectorVersion, Mode: "quarantined", ErrorCode: code,
		BranchGeneration:           fence.BranchGeneration,
		ThroughPublicationSequence: fence.ThroughPublicationSequence,
		SourceRevision:             fence.SourceRevision,
		MessageCount:               fence.StateMessageCount,
		EventChainSHA256:           transcriptstore.TranscriptWebSHA256([]byte("quarantined:" + code)),
	})
	if err != nil {
		return err
	}
	replaceAll := !fence.StateFound || fence.StateBranchGeneration != fence.BranchGeneration
	messageCount := fence.StateMessageCount
	visibleCount := fence.VisibleMessageCount
	artifactCount := fence.StateArtifactReferenceCount
	if replaceAll {
		messageCount, visibleCount, artifactCount = 0, 0, 0
	}
	state := transcriptstore.TranscriptWebProjectionState{
		StreamUID: work.StreamUID, BranchID: work.BranchID, BranchGeneration: fence.BranchGeneration,
		ProjectorVersion:           transcriptstore.TranscriptWebProjectorVersion,
		ProjectionRevision:         fence.StateProjectionRevision + 1,
		ThroughPublicationSequence: fence.ThroughPublicationSequence,
		SourceRevision:             fence.SourceRevision, MessageCount: messageCount, VisibleMessageCount: visibleCount,
		MessageArtifactReferenceCount: artifactCount, ProjectorStateJSON: stateJSON,
		ProjectorStateSHA256: transcriptstore.TranscriptWebSHA256(stateJSON),
		SourceChainSHA256:    transcriptstore.TranscriptWebSHA256([]byte("quarantined-source:" + code)),
		Status:               "quarantined", LastErrorCode: code, UpdatedAt: time.Now().UTC(),
	}
	err = s.transcriptWebReadModel.ApplyTranscriptWebProjection(ctx, transcriptstore.ApplyTranscriptWebProjectionInput{
		OwnerID: work.OwnerID, State: state,
		ExpectedBranchGeneration:   fence.StateBranchGeneration,
		ExpectedThroughPublication: fence.StateThroughPublicationSequence,
		ExpectedProjectionRevision: fence.StateProjectionRevision,
		ExpectedSourceRevision:     fence.StateSourceRevision,
		ExpectedSourceChainSHA256:  fence.StateSourceChainSHA256,
		ReplaceAll:                 replaceAll,
	})
	if err != nil {
		return err
	}
	// Deliberately omit the cause text and all payload/message data. The stable
	// code and bounded canonical coordinates are sufficient to locate the raw
	// durable evidence without duplicating sensitive content into logs.
	log.Printf("transcript_web_projection_quarantined code=%s stream=%q branch=%q generation=%d through_publication_sequence=%d source_revision=%d cause_type=%T",
		code, work.StreamUID, work.BranchID, fence.BranchGeneration,
		fence.ThroughPublicationSequence, fence.SourceRevision, cause)
	return nil
}

func transcriptWebProjectionErrorCode(err error) string {
	switch {
	case errors.Is(err, transcriptstore.ErrOwnerMismatch):
		return "projection_owner_mismatch"
	case errors.Is(err, transcriptstore.ErrArtifactMissing), errors.Is(err, transcriptstore.ErrArtifactMismatch):
		return "projection_artifact_invalid"
	case errors.Is(err, transcriptstore.ErrEventConflict):
		return "projection_source_conflict"
	default:
		return "projection_build_failed"
	}
}

func (s *Server) projectTranscriptWebSourceMetadata(
	ctx context.Context,
	stream transcriptstore.Stream,
	ownerID, sessionID string,
	snapshot transcriptstore.ProjectionSnapshot,
) (transcriptWebProjectionSourceMetadata, error) {
	typedRichHistory, err := s.transcriptStore.HasActiveTypedHistoryBootstrap(ctx, stream.UID, ownerID, stream.Epoch)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	projectedEvents := make([]transcriptstore.ProjectedEvent, 0, 128)
	if err := s.visitTranscriptWebProjectionSnapshotRaw(
		ctx, stream, ownerID, snapshot, true,
		func(projected transcriptstore.ProjectedEvent) error {
			projectedEvents = append(projectedEvents, projected)
			return nil
		},
	); err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	eventChain, err := transcriptWebSourceEventChain(projectedEvents)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	projectedEvents, err = normalizeTranscriptWebProjectionEvents(projectedEvents)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	assistantSourceEvents := append([]transcriptstore.ProjectedEvent(nil), projectedEvents...)
	projectedEvents, err = preserveTranscriptTerminalFailureCandidates(projectedEvents)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	typedFrameReferences, err := transcriptTypedAskUserFrameReferences(projectedEvents)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	reducer := newTranscriptWebCoordinateReducer(sessionID, typedRichHistory)
	for _, projected := range projectedEvents {
		if err := ctx.Err(); err != nil {
			return transcriptWebProjectionSourceMetadata{}, err
		}
		if projected.Event.FrameEventID != nil && typedFrameReferences[*projected.Event.FrameEventID] {
			continue
		}
		if err := reducer.consume(projected); err != nil {
			return transcriptWebProjectionSourceMetadata{}, fmt.Errorf("projection event id=%d type=%s attempt=%v: %w", projected.Event.EventID, projected.Event.Type, projected.Event.RunnerAttempt, err)
		}
	}
	coordinates, ranges, err := reducer.finalizeWithPublicationRanges()
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	assistant, err := transcriptWebIncrementalAssistantStateFromReducer(reducer, assistantSourceEvents)
	if err != nil {
		return transcriptWebProjectionSourceMetadata{}, err
	}
	return transcriptWebProjectionSourceMetadata{
		coordinates: coordinates,
		ranges:      ranges,
		eventChain:  eventChain,
		assistant:   assistant,
	}, nil
}

func transcriptWebSourceEventChain(projectedEvents []transcriptstore.ProjectedEvent) (string, error) {
	return extendTranscriptWebSourceEventChain("", projectedEvents)
}

func buildTranscriptWebMessageRecords(
	messages []map[string]any,
	metadata transcriptWebProjectionSourceMetadata,
) ([]transcriptstore.TranscriptWebMessageRecord, []transcriptstore.TranscriptWebMessageArtifactReference, error) {
	if len(messages) != len(metadata.coordinates) || len(messages) != len(metadata.ranges) {
		return nil, nil, fmt.Errorf("message/coordinate/range lengths %d/%d/%d", len(messages), len(metadata.coordinates), len(metadata.ranges))
	}
	records := make([]transcriptstore.TranscriptWebMessageRecord, 0, len(messages))
	references := make([]transcriptstore.TranscriptWebMessageArtifactReference, 0)
	for index, message := range messages {
		messageID := strings.TrimSpace(webString(message["id"]))
		clientMessageID := strings.TrimSpace(webString(message["msg_id"]))
		coordinate := metadata.coordinates[index]
		publicationRange := metadata.ranges[index]
		if messageID == "" || clientMessageID == "" || messageID != coordinate.id ||
			clientMessageID != coordinate.messageID || publicationRange.First <= 0 ||
			publicationRange.Last < publicationRange.First || publicationRange.FirstEventID <= 0 ||
			publicationRange.LastEventID <= 0 {
			return nil, nil, fmt.Errorf("record mismatch index=%d message=%q/%q coordinate=%q/%q range=%d-%d events=%d-%d", index, messageID, clientMessageID, coordinate.id, coordinate.messageID, publicationRange.First, publicationRange.Last, publicationRange.FirstEventID, publicationRange.LastEventID)
		}
		messageReferences, err := transcriptWebMessageReferenceRecords(
			message["artifact_refs"], index+1, publicationRange.FirstEventID,
		)
		if err != nil {
			return nil, nil, err
		}
		storedMessage := cloneTranscriptWebProjectionMap(message)
		delete(storedMessage, "artifact_refs")
		raw, err := json.Marshal(storedMessage)
		if err != nil || !json.Valid(raw) {
			return nil, nil, transcriptstore.ErrEventConflict
		}
		updatedAt, err := transcriptWebMessageTimestamp(message["created_at"])
		if err != nil {
			return nil, nil, err
		}
		visibleIndex := index
		record := transcriptstore.TranscriptWebMessageRecord{
			Ordinal: index + 1, MessageID: messageID, ClientMessageID: clientMessageID,
			Visible: true, VisibleIndex: &visibleIndex, MessageJSON: raw,
			MessageSHA256:            transcriptstore.TranscriptWebSHA256(raw),
			FirstPublicationSequence: publicationRange.First,
			LastPublicationSequence:  publicationRange.Last,
			UpdatedAt:                updatedAt,
		}
		record.ArtifactReferences = messageReferences
		records = append(records, record)
		references = append(references, messageReferences...)
	}
	return records, references, nil
}

func transcriptWebMessageTimestamp(value any) (time.Time, error) {
	milliseconds, ok := webPositiveSafeInteger(value)
	if !ok {
		return time.Time{}, transcriptstore.ErrEventConflict
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}

func transcriptWebMessageReferenceRecords(
	value any,
	messageOrdinal int,
	messageSourceEventID int64,
) ([]transcriptstore.TranscriptWebMessageArtifactReference, error) {
	values, err := transcriptWebArtifactReferenceObjects(value)
	if err != nil {
		return nil, err
	}
	result := make([]transcriptstore.TranscriptWebMessageArtifactReference, 0, len(values))
	for ordinal, value := range values {
		artifactID := strings.TrimSpace(webString(value["artifact_id"]))
		versionID := strings.TrimSpace(webString(value["version_id"]))
		relation := transcriptstore.ArtifactRelation(strings.TrimSpace(webString(value["relation"])))
		if artifactID == "" || versionID == "" {
			return nil, transcriptstore.ErrEventConflict
		}
		reference := transcriptstore.TranscriptWebMessageArtifactReference{
			MessageOrdinal: messageOrdinal, Ordinal: ordinal,
			ArtifactID: artifactID, VersionID: versionID, Relation: relation,
		}
		if availability := transcriptstore.ArtifactAvailability(strings.TrimSpace(webString(value["availability"]))); availability != "" &&
			availability != transcriptstore.ArtifactAvailable && availability != transcriptstore.ArtifactDeleted &&
			availability != transcriptstore.ArtifactMissing {
			return nil, transcriptstore.ErrEventConflict
		}
		if attempt, ok := webPositiveSafeInteger(value["attempt"]); ok {
			sourceEventID, sourceOK := webPositiveSafeInteger(value["source_event_id"])
			sourceOrdinal, ordinalOK := transcriptWebNonnegativeSafeInteger(value["ordinal"])
			if !sourceOK || !ordinalOK {
				return nil, transcriptstore.ErrEventConflict
			}
			reference.RunnerAttempt = &attempt
			reference.SourceEventID = sourceEventID
			reference.SourceReferenceOrdinal = int(sourceOrdinal)
		} else {
			sizeBytes, sizeOK := transcriptWebNonnegativeSafeInteger(value["size_bytes"])
			reference.SourceEventID = messageSourceEventID
			reference.SourceReferenceOrdinal = ordinal
			reference.Filename = strings.TrimSpace(webString(value["filename"]))
			reference.ContentType = strings.TrimSpace(webString(value["content_type"]))
			reference.SizeBytes = sizeBytes
			reference.Checksum = strings.TrimSpace(webString(value["checksum"]))
			if !sizeOK || relation != transcriptstore.ArtifactRelationAttached || reference.Filename == "" ||
				reference.ContentType == "" || reference.Checksum == "" {
				return nil, transcriptstore.ErrEventConflict
			}
		}
		result = append(result, reference)
	}
	return result, nil
}

func transcriptWebArtifactReferenceObjects(value any) ([]map[string]any, error) {
	if value == nil {
		return []map[string]any{}, nil
	}
	switch typed := value.(type) {
	case []map[string]any:
		return typed, nil
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, transcriptstore.ErrEventConflict
			}
			result = append(result, object)
		}
		return result, nil
	default:
		return nil, transcriptstore.ErrEventConflict
	}
}

func transcriptWebNonnegativeSafeInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case float64:
		return int64(typed), typed >= 0 && typed <= 1<<53-1 && float64(int64(typed)) == typed
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil && parsed >= 0 && parsed <= 1<<53-1
	default:
		return 0, false
	}
}

func transcriptWebProjectionSourceChain(
	eventChain string,
	snapshot transcriptstore.ProjectionSnapshot,
	sourceRevision int64,
	_ []transcriptstore.TranscriptWebMessageRecord,
) (string, error) {
	if !validTranscriptWebSourceEventChain(eventChain) || snapshot.BranchID == "" || snapshot.BranchGeneration <= 0 ||
		snapshot.ThroughPublicationSequence < 0 || sourceRevision < 0 {
		return "", errors.New("invalid Transcript Web source chain input")
	}
	// Every projected message is a deterministic reduction of the immutable
	// source event chain, while message rows carry their own SHA-256 checksums.
	// Keeping the state chain appendable is what lets a ready projection advance
	// without loading its complete message prefix.
	return eventChain, nil
}
