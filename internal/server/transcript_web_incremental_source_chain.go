package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func (s *Server) transcriptWebPageSupersedesTerminal(
	ctx context.Context,
	stream transcriptstore.Stream,
	checkpoint transcriptWebIncrementalAssistantCheckpoint,
	page []transcriptstore.ProjectedEvent,
) (bool, error) {
	priorAttempt := checkpoint.TerminalThroughAttempt
	if priorAttempt <= 0 {
		return false, nil
	}
	var nextAttempt int64
	for _, projected := range page {
		if projected.Event.RunnerAttempt != nil && *projected.Event.RunnerAttempt > priorAttempt {
			nextAttempt = *projected.Event.RunnerAttempt
			break
		}
	}
	if nextAttempt <= 0 {
		return false, nil
	}
	prior, err := s.transcriptStore.GetRunnerRuntimeState(ctx, stream.UID, stream.OwnerID, priorAttempt)
	if err != nil {
		return false, err
	}
	if prior.Status != "failed" && prior.Status != "cancelled" && prior.Status != "canceled" {
		return false, nil
	}
	next, err := s.transcriptStore.GetRunnerRuntimeState(ctx, stream.UID, stream.OwnerID, nextAttempt)
	if err != nil {
		return false, err
	}
	return prior.ClaimedInputRevision > 0 && prior.ClaimedInputRevision == next.ClaimedInputRevision, nil
}

func (r *transcriptWebIncrementalAssistantReducer) restoreAssistantRollback(
	attempt *transcriptWebIncrementalAssistantAttemptCheckpoint,
) error {
	rollback := attempt.Rollback
	if rollback == nil || rollback.BeforeAttempt.CurrentMessage == nil {
		return transcriptstore.ErrEventConflict
	}
	postReset := cloneTranscriptWebIncrementalRecord(attempt.CurrentMessage)
	before := cloneTranscriptWebIncrementalAttempt(rollback.BeforeAttempt)
	before.Rollback = nil
	r.seenIdentities = map[string]bool{}
	for _, identity := range rollback.SeenIdentities {
		if strings.TrimSpace(identity) != identity || identity == "" || r.seenIdentities[identity] {
			return transcriptstore.ErrEventConflict
		}
		r.seenIdentities[identity] = true
	}
	if postReset != nil {
		if postReset.Ordinal != before.CurrentMessage.Ordinal {
			return errTranscriptWebIncrementalAssistantFallback
		}
		if err := r.replaceTailRecord(postReset, before.CurrentMessage); err != nil {
			return err
		}
	} else {
		if before.CurrentMessage.Ordinal != r.messageCount+1 || before.CurrentMessage.VisibleIndex == nil ||
			*before.CurrentMessage.VisibleIndex != r.visibleCount {
			return errTranscriptWebIncrementalAssistantFallback
		}
		if err := r.appendRecord(*before.CurrentMessage); err != nil {
			return err
		}
	}
	*attempt = before
	return nil
}

func buildIncrementalTranscriptWebUserMessage(
	projected transcriptstore.ProjectedEvent,
	sessionID, branchID string,
	priorMessageCount, priorVisibleCount int,
) (transcriptstore.TranscriptWebMessageRecord, []transcriptstore.TranscriptWebMessageArtifactReference, error) {
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return transcriptstore.TranscriptWebMessageRecord{}, nil, err
	}
	messageID := strings.TrimSpace(webString(payload["messageUuid"]))
	if messageID == "" {
		messageID = strings.TrimSpace(webString(payload["_uuid"]))
	}
	if messageID == "" {
		messageID = projected.Event.ClientMessageID
	}
	artifactReferences := transcriptArtifactReferences(projected.ArtifactReferences)
	payloadReferences, err := transcriptUserArtifactReferences(payload)
	if err != nil {
		return transcriptstore.TranscriptWebMessageRecord{}, nil, err
	}
	if len(payloadReferences) > 0 {
		if len(artifactReferences) > 0 {
			return transcriptstore.TranscriptWebMessageRecord{}, nil, transcriptstore.ErrEventConflict
		}
		artifactReferences = payloadReferences
	}
	message := map[string]any{
		"id": messageID, "msg_id": projected.Event.ClientMessageID,
		"conversation_id": sessionID, "type": "text", "position": "right", "status": "finish",
		"created_at": projected.Event.CreatedAt.UnixMilli(),
		"content": map[string]any{
			"content": transcriptPayloadText(payload),
			"synonBiomed": map[string]any{
				"messageIndex": priorVisibleCount, "blockIndex": 0, "branchId": branchID,
			},
		},
		"artifact_refs": artifactReferences,
	}
	records, references, err := buildTranscriptWebMessageRecords([]map[string]any{message}, transcriptWebProjectionSourceMetadata{
		coordinates: []canonicalReadCursorCoordinate{{id: messageID, messageID: projected.Event.ClientMessageID}},
		ranges: []transcriptWebPublicationRange{{
			First: projected.Event.PublicationSeq, Last: projected.Event.PublicationSeq,
			FirstEventID: projected.Event.EventID, LastEventID: projected.Event.EventID,
		}},
	})
	if err != nil || len(records) != 1 {
		if err != nil {
			return transcriptstore.TranscriptWebMessageRecord{}, nil, err
		}
		return transcriptstore.TranscriptWebMessageRecord{}, nil, transcriptstore.ErrEventConflict
	}
	ordinal := priorMessageCount + 1
	visibleIndex := priorVisibleCount
	records[0].Ordinal = ordinal
	records[0].VisibleIndex = &visibleIndex
	for index := range references {
		references[index].MessageOrdinal = ordinal
	}
	records[0].ArtifactReferences = references
	return records[0], references, nil
}

func extendTranscriptWebSourceEventChain(
	prior string,
	projectedEvents []transcriptstore.ProjectedEvent,
) (string, error) {
	var chain []byte
	if prior == "" {
		seed := sha256.Sum256([]byte("synon.transcript.web-source-chain.v2"))
		chain = seed[:]
	} else {
		if !validTranscriptWebSourceEventChain(prior) {
			return "", transcriptstore.ErrEventConflict
		}
		decoded, err := hex.DecodeString(prior)
		if err != nil {
			return "", transcriptstore.ErrEventConflict
		}
		chain = decoded
	}
	for _, projected := range projectedEvents {
		if projected.Event.EventID <= 0 || projected.Event.PublicationSeq <= 0 ||
			strings.TrimSpace(projected.Event.ClientMessageID) == "" || !json.Valid(projected.ResolvedPayloadJSON) {
			return "", transcriptstore.ErrEventConflict
		}
		payloadDigest := sha256.Sum256(projected.ResolvedPayloadJSON)
		frameEventID := ""
		if projected.Event.FrameEventID != nil {
			frameEventID = strings.TrimSpace(*projected.Event.FrameEventID)
		}
		encoded, err := json.Marshal(transcriptWebProjectionSourceDigest{
			EventID: projected.Event.EventID, PublicationSeq: projected.Event.PublicationSeq,
			ClientMessageID: projected.Event.ClientMessageID, Type: projected.Event.Type,
			Source: string(projected.Event.Source), RunnerAttempt: projected.Event.RunnerAttempt,
			PayloadSHA256: hex.EncodeToString(payloadDigest[:]), FrameEventID: frameEventID,
		})
		if err != nil {
			return "", fmt.Errorf("hash Transcript Web source event: %w", err)
		}
		digest := sha256.New()
		_, _ = digest.Write([]byte("synon.transcript.web-source-chain.v2\x00"))
		_, _ = digest.Write(chain)
		_, _ = digest.Write(encoded)
		chain = digest.Sum(nil)
	}
	return hex.EncodeToString(chain), nil
}

func validTranscriptWebSourceEventChain(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
