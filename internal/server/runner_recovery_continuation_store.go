package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func (s *Server) scanTranscriptProjection(
	ctx context.Context,
	snapshot transcriptstore.ProjectionSnapshot,
	ownerID string,
	through int64,
	visit func(transcriptstore.ProjectedEvent) error,
) error {
	if s == nil || s.transcriptStore == nil || snapshot.StreamUID == "" || snapshot.BranchID == "" ||
		snapshot.BranchGeneration <= 0 || strings.TrimSpace(ownerID) == "" || through < 0 {
		return errors.New("provider continuation projection authority is invalid")
	}
	if through == 0 {
		return nil
	}
	after := int64(0)
	for after < through {
		page, err := s.transcriptStore.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: snapshot.StreamUID, OwnerID: ownerID, BranchID: snapshot.BranchID,
			BranchGeneration: snapshot.BranchGeneration, AfterPublicationSequence: after,
			ThroughPublicationSequence: through, Limit: 1000,
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return errors.New("provider continuation projection ended before its accepted fence")
		}
		for _, projected := range page {
			if projected.Event.PublicationSeq <= after || projected.Event.PublicationSeq > through {
				return errors.New("provider continuation projection cursor is invalid")
			}
			if err := visit(projected); err != nil {
				return err
			}
			after = projected.Event.PublicationSeq
		}
	}
	return nil
}

func (s *Server) loadProviderContinuation(
	ctx context.Context,
	run *sessionRunnerChatRun,
) error {
	if run == nil || run.Transcript == nil || s == nil || s.transcriptStore == nil {
		return nil
	}
	authority := run.Transcript
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return fmt.Errorf("load provider continuation projection snapshot: %w", err)
	}
	if snapshot.BranchID == "" || snapshot.BranchGeneration <= 0 {
		return errors.New("provider continuation requires a persisted active Transcript branch")
	}
	contracts := make([]sessionRunnerProviderContinuationV1, 0, 4)
	pendingInterruption := false
	chainOpen := false
	err = s.scanTranscriptProjection(ctx, snapshot, authority.Stream.OwnerID, snapshot.ThroughPublicationSequence, func(projected transcriptstore.ProjectedEvent) error {
		var payload map[string]any
		if len(projected.ResolvedPayloadJSON) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(projected.ResolvedPayloadJSON))
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				return fmt.Errorf("decode provider continuation event %d: %w", projected.Event.EventID, err)
			}
		}
		switch projected.Event.Type {
		case "user_message", "runner_finished", "assistant_message", "content_reset":
			contracts = contracts[:0]
			pendingInterruption = false
			chainOpen = false
			return nil
		case "runner_checkpoint":
			if _, hasCalls := payload["modelToolCalls"]; hasCalls || strings.TrimSpace(stringValue(payload["toolName"])) != "" {
				contracts = contracts[:0]
				pendingInterruption = false
				chainOpen = false
				return nil
			}
			contract, present, err := parseProviderContinuationV1(payload)
			if err != nil {
				return fmt.Errorf("decode provider continuation checkpoint event %d: %w", projected.Event.EventID, err)
			}
			if present {
				if pendingInterruption {
					return errors.New("provider continuation checkpoint is missing its typed interruption fence")
				}
				if projected.Event.RunnerAttempt == nil || *projected.Event.RunnerAttempt != contract.PreviousAttempt ||
					contract.StreamUID != authority.Stream.UID || contract.OwnerID != authority.Stream.OwnerID ||
					contract.BranchID != snapshot.BranchID || contract.BranchGeneration != snapshot.BranchGeneration ||
					contract.AcceptedThroughPublicationSequence >= projected.Event.PublicationSeq {
					return errors.New("provider continuation checkpoint authority does not match its Transcript event")
				}
				if len(contracts) == 0 || !chainOpen || !sameProviderContinuationRoot(contracts[len(contracts)-1], contract) {
					if contract.SegmentIndex != 1 || contract.RootAttempt != contract.PreviousAttempt ||
						contract.RootSegmentOrdinal != contract.CurrentSegmentOrdinal {
						return errors.New("provider continuation root checkpoint is invalid")
					}
					contracts = contracts[:0]
				} else if err := validateNextProviderContinuation(contracts[len(contracts)-1], contract); err != nil {
					return err
				}
				contracts = append(contracts, contract)
				pendingInterruption = true
				chainOpen = true
				return nil
			}
			reason := strings.TrimSpace(stringValue(payload["reason_code"]))
			if reason == "" {
				reason = strings.TrimSpace(stringValue(payload["reasonCode"]))
			}
			if providerContinuationInterruptionFenceReason(reason) && projected.Event.RunnerAttempt != nil {
				matched, err := matchProviderContinuationInterruptionFence(
					contracts, pendingInterruption, *projected.Event.RunnerAttempt,
				)
				if err != nil {
					return err
				}
				if matched {
					pendingInterruption = false
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("load provider continuation checkpoint chain: %w", err)
	}
	if len(contracts) == 0 {
		return nil
	}
	if pendingInterruption {
		return errors.New("provider continuation checkpoint is missing its typed interruption fence")
	}
	latest := contracts[len(contracts)-1]
	if authority.Claim.Attempt != latest.PreviousAttempt {
		return errors.New("provider continuation lease is not bound to its stable task attempt")
	}
	selectedSegments := make(map[int64]bool, len(contracts))
	for _, contract := range contracts {
		selectedSegments[contract.CurrentSegmentOrdinal] = true
	}
	state := &sessionRunnerProviderContinuationState{Contract: latest, Digest: sha256.New()}
	selectedStarted := false
	var firstEventID, firstPublication, lastEventID, lastPublication int64
	err = s.scanTranscriptProjection(ctx, snapshot, authority.Stream.OwnerID, latest.AcceptedThroughPublicationSequence, func(projected transcriptstore.ProjectedEvent) error {
		if projected.Event.RunnerAttempt == nil {
			return nil
		}
		attempt := *projected.Event.RunnerAttempt
		if attempt != latest.RootAttempt {
			return nil
		}
		var payload map[string]any
		if len(projected.ResolvedPayloadJSON) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(projected.ResolvedPayloadJSON))
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				return err
			}
		}
		if projected.Event.Type == "runner_checkpoint" && selectedStarted {
			if _, hasCalls := payload["modelToolCalls"]; hasCalls || strings.TrimSpace(stringValue(payload["toolName"])) != "" {
				return errors.New("provider continuation crossed a durable tool-call boundary")
			}
		}
		if projected.Event.Type == "content_reset" && selectedStarted {
			return errors.New("provider continuation crossed an assistant replacement boundary")
		}
		privateText, private, err := privateProviderCandidateText(payload)
		if err != nil {
			return err
		}
		if projected.Event.Type != "content_delta" && !(projected.Event.Type == "runner_checkpoint" && private) {
			return nil
		}
		segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
		if err != nil {
			return err
		}
		if !present || !selectedSegments[segment.Ordinal] {
			return nil
		}
		if attempt == latest.RootAttempt && projected.Event.PublicationSeq < latest.RootStartedPublicationSequence {
			return nil
		}
		text := stringValue(payload["text"])
		if private {
			text = privateText
		}
		if text == "" {
			return errors.New("provider continuation content delta is empty")
		}
		if firstEventID == 0 {
			firstEventID = projected.Event.EventID
			firstPublication = projected.Event.PublicationSeq
		}
		selectedStarted = true
		lastEventID = projected.Event.EventID
		lastPublication = projected.Event.PublicationSeq
		state.Content.WriteString(text)
		if private {
			state.PrivateCandidate.WriteString(text)
		}
		_, _ = state.Digest.Write([]byte(text))
		return nil
	})
	if err != nil {
		return fmt.Errorf("rebuild provider continuation accepted content: %w", err)
	}
	acceptedSHA := hex.EncodeToString(state.Digest.Sum(nil))
	if firstEventID != latest.RootStartedEventID || firstPublication != latest.RootStartedPublicationSequence ||
		lastEventID != latest.AcceptedThroughEventID || lastPublication != latest.AcceptedThroughPublicationSequence ||
		int64(state.Content.Len()) != latest.AcceptedSemanticBytes || acceptedSHA != latest.AcceptedSHA256 {
		return errors.New("provider continuation accepted content does not match its durable fence and digest")
	}
	for index := len(contracts) - 1; index > 0; index-- {
		current, previous := contracts[index], contracts[index-1]
		if current.AcceptedThroughEventID != previous.AcceptedThroughEventID ||
			current.AcceptedThroughPublicationSequence != previous.AcceptedThroughPublicationSequence ||
			current.AcceptedSemanticBytes != previous.AcceptedSemanticBytes || current.AcceptedSHA256 != previous.AcceptedSHA256 {
			break
		}
		state.ConsecutiveNoProgress++
	}
	run.ProviderContinuation = state
	run.ProviderAttemptSemanticBytes = 0
	run.AssistantSegmentOrdinal = latest.CurrentSegmentOrdinal
	run.AssistantSegmentHasContent = false
	return nil
}

func matchProviderContinuationInterruptionFence(
	contracts []sessionRunnerProviderContinuationV1,
	pending bool,
	attempt int64,
) (bool, error) {
	if !pending {
		// No new accepted-content boundary is open. This is a valid no-progress
		// fence even when an earlier continuation chain remains available.
		return false, nil
	}
	if len(contracts) == 0 || attempt != contracts[len(contracts)-1].PreviousAttempt {
		return false, errors.New("provider continuation interruption fence has no matching boundary")
	}
	return true, nil
}

func providerContinuationInterruptionFenceReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "provider_stream_interrupted", "provider_stream_no_progress",
		sessionRunnerProviderOutputTokenLimitReasonCode, sessionRunnerModelProtocolErrorReasonCode:
		return true
	default:
		return false
	}
}

func appendProviderContinuationContext(
	messages []chatCompletionMessage,
	state *sessionRunnerProviderContinuationState,
) []chatCompletionMessage {
	if state == nil || state.Content.Len() == 0 {
		return messages
	}
	contract := state.Contract
	messages = append(messages, chatCompletionMessage{Role: "assistant", Content: state.Content.String()})
	guidance := fmt.Sprintf(
		"Synon provider continuation authority (server supplied): continue after the exact accepted assistant prefix above. Do not repeat, rewrite, summarize, or retract any accepted prefix bytes. Start with only the next semantic byte. continuation_root_attempt=%d continuation_root_segment=%d accepted_bytes=%d accepted_sha256=%s.",
		contract.RootAttempt, contract.RootSegmentOrdinal, contract.AcceptedSemanticBytes, contract.AcceptedSHA256,
	)
	messages = append(messages, chatCompletionMessage{Role: "system", Content: guidance})
	return messages
}

func (s *Server) persistProviderContinuationBoundary(
	ctx context.Context,
	run *sessionRunnerChatRun,
) (transcriptstore.Event, error) {
	if s == nil || s.transcriptStore == nil || run == nil || run.Transcript == nil || run.ProviderContinuation == nil {
		return transcriptstore.Event{}, errors.New("recoverable provider continuation requires Transcript authority and accepted content")
	}
	state := run.ProviderContinuation
	if state.Digest == nil || state.Content.Len() == 0 || state.Contract.AcceptedSemanticBytes != int64(state.Content.Len()) ||
		state.Contract.AcceptedThroughEventID <= 0 || state.Contract.AcceptedThroughPublicationSequence <= 0 {
		return transcriptstore.Event{}, errors.New("recoverable provider continuation has no exact accepted-content fence")
	}
	authority := run.Transcript
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return transcriptstore.Event{}, fmt.Errorf("snapshot provider continuation boundary: %w", err)
	}
	if snapshot.BranchID == "" || snapshot.BranchGeneration <= 0 ||
		state.Contract.AcceptedThroughPublicationSequence > snapshot.ThroughPublicationSequence {
		return transcriptstore.Event{}, errors.New("provider continuation boundary has no persisted active branch fence")
	}
	contract := state.Contract
	if contract.SegmentIndex > 0 && (contract.BranchID != snapshot.BranchID || contract.BranchGeneration != snapshot.BranchGeneration ||
		contract.StreamUID != authority.Stream.UID || contract.OwnerID != authority.Stream.OwnerID ||
		authority.Claim.Attempt != contract.PreviousAttempt) {
		return transcriptstore.Event{}, errors.New("provider continuation boundary no longer matches its durable chain authority")
	}
	contract.ContractVersion = sessionRunnerProviderContinuationContractVersion
	contract.StreamUID = authority.Stream.UID
	contract.OwnerID = authority.Stream.OwnerID
	contract.BranchID = snapshot.BranchID
	contract.BranchGeneration = snapshot.BranchGeneration
	contract.PreviousAttempt = authority.Claim.Attempt
	contract.CurrentSegmentOrdinal = run.assistantSegmentOrdinal()
	contract.SegmentIndex++
	contract.AcceptedSHA256 = hex.EncodeToString(state.Digest.Sum(nil))
	if state.Contract.SegmentIndex == 0 {
		if contract.SegmentIndex != 1 || contract.RootAttempt != authority.Claim.Attempt ||
			contract.PreviousAttempt != authority.Claim.Attempt ||
			contract.RootSegmentOrdinal != contract.CurrentSegmentOrdinal {
			return transcriptstore.Event{}, errors.New("provider continuation root boundary is invalid")
		}
	} else if err := validateNextProviderContinuation(state.Contract, contract); err != nil {
		return transcriptstore.Event{}, err
	}
	payload := map[string]any{
		"status":                "running",
		"detail":                "provider generation reached a recoverable content-only segment boundary",
		"provider_continuation": providerContinuationPayloadV1(contract),
	}
	if _, present, err := parseProviderContinuationV1(payload); err != nil || !present {
		if err == nil {
			err = errors.New("provider continuation checkpoint is missing")
		}
		return transcriptstore.Event{}, err
	}
	event, err := s.checkpointTranscriptRunnerEvent(
		ctx, authority, transcriptstore.RunnerPhaseExecuting,
		fmt.Sprintf("provider-continuation-%06d", contract.SegmentIndex), payload, true,
	)
	if err != nil {
		return transcriptstore.Event{}, err
	}
	if event.EventID <= contract.AcceptedThroughEventID || event.PublicationSeq <= contract.AcceptedThroughPublicationSequence {
		return transcriptstore.Event{}, errors.New("provider continuation checkpoint did not advance beyond its accepted-content fence")
	}
	state.Contract = contract
	if run.ProviderAttemptSemanticBytes > 0 {
		state.ConsecutiveNoProgress = 0
	} else {
		state.ConsecutiveNoProgress++
	}
	return event, nil
}
