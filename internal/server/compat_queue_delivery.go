package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) advanceCompatibilityFrameAfterRunner(frameID, terminalStatus string) (int, error) {
	if s == nil || s.workspaceStore == nil ||
		(s.transcriptStore == nil && (s.sessionStore == nil || s.eventJournal == nil)) {
		return 0, nil
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return 0, nil
	}
	frame, found, err := s.workspaceStore.GetFrame(frameID)
	if err != nil || !found {
		return 0, err
	}
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	records, err := s.workspaceStore.ClaimCompatibilityQueuedMessages(frame.ID)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		if s.transcriptStore != nil {
			return 0, nil
		}
		status := "completed"
		if terminalStatus == "failed" {
			status = "failed"
		} else if terminalStatus == "cancelled" {
			status = "cancelled"
		}
		_, err := s.workspaceStore.UpdateFrame(frame.ID, workspace.UpdateFrameInput{Status: &status})
		return 0, err
	}
	if s.transcriptStore == nil {
		processing := "processing"
		if _, err := s.workspaceStore.UpdateFrame(frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
			_ = s.releaseCompatibilityQueueClaims(records)
			return 0, err
		}
	}
	for index, record := range records {
		text := strings.TrimSpace(stringValue(record.DeliveryPayload["text"]))
		if text == "" {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, fmt.Errorf("compatibility queued intent %s has no message text", record.IntentID)
		}
		// A completed runner is the terminal boundary for the logical task. A
		// continuation directive that was queued before that boundary is stale
		// (for example, a resume control racing with the final answer) and must
		// not reopen the conversation or create a duplicate final response. Keep
		// ordinary queued user messages eligible: those are explicit follow-up
		// work and remain valid after a prior task completes.
		if strings.TrimSpace(terminalStatus) == "completed" &&
			transcriptstore.IsExplicitTaskContinuationDirective(text) {
			if err := s.workspaceStore.CompleteCompatibilityQueuedMessageDelivery(record.IntentID); err != nil {
				_ = s.releaseCompatibilityQueueClaims(records[index:])
				return index, fmt.Errorf("settle stale compatibility continuation %s: %w", record.IntentID, err)
			}
			continue
		}
		runtimeConfig, err := compatibilityQueuedSessionConfig(record.DeliveryPayload)
		if err != nil {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, err
		}
		artifactReferences, messageContext, err := compatibilityQueuedArtifactContext(record.DeliveryPayload)
		if err != nil {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, err
		}
		inputData, ok := record.DeliveryPayload["inputData"].(map[string]any)
		if !ok {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, fmt.Errorf("compatibility queued intent %s has invalid input data", record.IntentID)
		}
		if s.transcriptStore == nil {
			if err := s.applyCompatibilityQueuedSessionConfig(frame.ID, record.DeliveryPayload); err != nil {
				_ = s.releaseCompatibilityQueueClaims(records[index:])
				return index, err
			}
		}
		if _, _, err := s.submitFrameMessage(s.workspaceStore, frameMessageSubmission{
			FrameID: frame.ID, MessageUUID: record.IntentID,
			ClientMessageID: record.IntentID, Text: text, RuntimeConfig: runtimeConfig,
			InputData:          copyMapAny(inputData),
			ArtifactReferences: artifactReferences, MessageContext: messageContext,
		}); err != nil {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, fmt.Errorf("deliver compatibility queued intent %s: %w", record.IntentID, err)
		}
		if err := s.workspaceStore.CompleteCompatibilityQueuedMessageDelivery(record.IntentID); err != nil {
			_ = s.releaseCompatibilityQueueClaims(records[index:])
			return index, fmt.Errorf("complete compatibility queued intent %s: %w", record.IntentID, err)
		}
	}
	return len(records), nil
}

func compatibilityQueuedArtifactContext(payload map[string]any) ([]transcriptstore.UserArtifactReferenceInput, string, error) {
	messageContext := strings.TrimSpace(stringValue(payload["messageContext"]))
	if messageContext != "" && messageContext != "onboarding_first_task" && messageContext != structuredOnboardingMessageContext {
		return nil, "", errors.New("compatibility queued message context is invalid")
	}
	raw, found := payload["artifactRefs"]
	if !found {
		return nil, messageContext, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, "", errors.New("compatibility queued artifact references are invalid")
	}
	refs := make([]transcriptstore.UserArtifactReferenceInput, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok || len(record) != 2 {
			return nil, "", errors.New("compatibility queued artifact reference is invalid")
		}
		artifactID := strings.TrimSpace(stringValue(record["artifact_id"]))
		versionID := strings.TrimSpace(stringValue(record["version_id"]))
		if artifactID == "" || versionID == "" {
			return nil, "", errors.New("compatibility queued artifact reference is invalid")
		}
		key := artifactID + "\x00" + versionID
		if _, duplicate := seen[key]; duplicate {
			return nil, "", errors.New("compatibility queued artifact references are not unique")
		}
		seen[key] = struct{}{}
		refs = append(refs, transcriptstore.UserArtifactReferenceInput{ArtifactID: artifactID, VersionID: versionID})
	}
	return refs, messageContext, nil
}

func (s *Server) applyCompatibilityQueuedSessionConfig(frameID string, payload map[string]any) error {
	config, err := compatibilityQueuedSessionConfig(payload)
	if err != nil || config == nil {
		return err
	}
	session, found, err := s.sessionStore.Get(frameID)
	if err != nil {
		return err
	}
	if !found {
		session = sessionstore.Session{ID: frameID, Title: frameID, WorkDir: s.fileRoot}
		if err := s.sessionStore.Upsert(session); err != nil {
			return err
		}
		session, found, err = s.sessionStore.Get(frameID)
		if err != nil || !found {
			return errors.New("compatibility queue session was not created")
		}
	}
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		orchestration = map[string]any{}
	}
	orchestration["sessionConfig"] = copyMapAny(config)
	if inputData, ok := payload["inputData"].(map[string]any); ok {
		orchestration["inputData"] = copyMapAny(inputData)
	}
	session.Orchestration = orchestration
	return s.sessionStore.Save(session)
}

func compatibilityQueuedSessionConfig(payload map[string]any) (map[string]any, error) {
	rawConfig, ok := payload["sessionConfig"]
	if !ok {
		return nil, nil
	}
	config, ok := rawConfig.(map[string]any)
	if !ok {
		return nil, errors.New("compatibility queued session config is invalid")
	}
	return copyMapAny(config), nil
}

func (s *Server) releaseCompatibilityQueueClaims(records []workspace.CompatibilityMessageIntent) error {
	var releaseErr error
	for _, record := range records {
		if err := s.workspaceStore.ReleaseCompatibilityQueuedMessageDelivery(record.IntentID); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}

func (s *Server) recoverCompatibilityQueuedMessages() (int, error) {
	if s == nil || s.workspaceStore == nil || s.sessionStore == nil {
		return 0, nil
	}
	frameIDs, err := s.workspaceStore.CompatibilityQueuedFrameIDs()
	if err != nil {
		return 0, err
	}
	advanced := 0
	now := time.Now().UTC()
	for _, frameID := range frameIDs {
		if s.transcriptStore != nil {
			frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
			if err != nil {
				return advanced, err
			}
			if !found {
				return advanced, fmt.Errorf("queued frame %s has no owner-scoped context", frameID)
			}
			stream, found, err := s.transcriptStore.GetFrameStreamBySession(
				context.Background(), frameContext.UserID, frameID,
			)
			if err != nil {
				return advanced, err
			}
			if !found {
				return advanced, fmt.Errorf("queued frame %s has no canonical transcript stream", frameID)
			}
			state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(
				context.Background(), stream.UID, frameContext.UserID,
			)
			if err != nil {
				return advanced, err
			}
			if !found || state.Phase != transcriptstore.RunnerPhaseTerminal || !isRunnerFinishStatus(state.Status) {
				continue
			}
			if frameContext.Frame.Status != state.Status {
				return advanced, fmt.Errorf(
					"queued frame %s terminal status conflicts with transcript authority", frameID,
				)
			}
			count, err := s.advanceCompatibilityFrameAfterRunner(frameID, state.Status)
			if err != nil {
				return advanced, err
			}
			advanced += count
			continue
		}
		session, found, err := s.sessionStore.Get(frameID)
		if err != nil {
			return advanced, err
		}
		terminalStatus := "completed"
		if found {
			if session.Runner != nil && session.Runner.ExpiresAt.After(now) {
				continue
			}
			if session.Runner == nil && session.LastRole == "user" {
				continue
			}
			if session.Runner != nil && session.Runner.Status == "failed" {
				terminalStatus = "failed"
			}
		}
		count, err := s.advanceCompatibilityFrameAfterRunner(frameID, terminalStatus)
		if err != nil {
			return advanced, err
		}
		advanced += count
	}
	return advanced, nil
}
