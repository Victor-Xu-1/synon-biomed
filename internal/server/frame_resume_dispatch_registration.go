package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	"time"
)

func normalizeFrameResumeDispatchOptions(options FrameResumeDispatchOptions) FrameResumeDispatchOptions {
	options.WorkerID = strings.TrimSpace(options.WorkerID)
	if options.WorkerID == "" {
		options.WorkerID = defaultFrameResumeDispatchWorkerID
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultFrameResumeDispatchPollInterval
	}
	if options.ClaimTTL <= 0 {
		runnerLeaseTTL := options.Chat.LeaseTTL
		if runnerLeaseTTL <= 0 {
			runnerLeaseTTL = defaultSessionRunnerLeaseTTL
		}
		// The dispatch is the outer authority for the runner lease. It must not
		// expire first: under host pressure another dispatcher could otherwise
		// steal the claim while the transcript still proves the runner healthy,
		// then cancel that healthy runner as a split-brain precaution. The grace
		// is failover tolerance, not a task deadline; both leases keep renewing
		// for as long as the logical task runs.
		options.ClaimTTL = runnerLeaseTTL + frameResumeDispatchClaimLeaseGrace
	}
	if options.ReservationTTL <= 0 {
		options.ReservationTTL = defaultFrameResumeReservationTTL
	}
	return options
}

func (s *Server) registerFrameResumeDispatch(resumeEventID string, _ time.Duration) error {
	if s == nil || s.workspaceStore == nil {
		return errors.New("frame resume dispatch runtime is not configured")
	}
	dispatch, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatch(resumeEventID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("resume dispatch event %q not found", resumeEventID)
	}
	if dispatch.Status == "completed" || dispatch.Status == "failed" || dispatch.Status == "cancelled" {
		return nil
	}
	if _, authoritative, err := s.resolveTranscriptFrameStream(context.Background(), dispatch.FrameID); err != nil {
		return err
	} else if authoritative {
		return nil
	}
	if s.sessionStore == nil || s.eventJournal == nil {
		return errors.New("legacy resume dispatch runtime is not configured")
	}
	frame, found, err := s.workspaceStore.GetFrame(dispatch.FrameID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("resume dispatch frame %q not found", dispatch.FrameID)
	}
	project, found, err := s.workspaceStore.GetProject(dispatch.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("resume dispatch project %q not found", dispatch.ProjectID)
	}
	s.sessionSubmissionMu.Lock()
	defer s.sessionSubmissionMu.Unlock()
	if err := s.copyWorkspaceFrameMessagesToResumeJournal(dispatch.FrameID); err != nil {
		return err
	}
	entries, err := s.eventJournal.ReadAll(dispatch.FrameID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	session, sessionFound, err := s.sessionStore.Get(dispatch.FrameID)
	if err != nil {
		return err
	}
	if !sessionFound {
		session = sessionstore.Session{
			ID: dispatch.FrameID, CreatedAt: frame.CreatedAt,
		}
	}
	session.Title = strings.TrimSpace(frame.Name)
	if session.Title == "" {
		session.Title = dispatch.FrameID
	}
	session.WorkDir = s.fileRoot
	session.UpdatedAt = now
	session.LastRole = "user"
	session.LastUserMessageAt = latestResumeJournalUserTime(entries, now)
	session.MessageCount = resumeJournalMessageCount(entries)
	session.Project = &sessionstore.Project{
		ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: now,
	}
	orchestration := cloneResumeOrchestration(session.Orchestration)
	orchestration["frameResumeDispatch"] = map[string]any{
		"resumeEventId": dispatch.ResumeEvent.ID,
		"frameId":       dispatch.FrameID,
		"rootFrameId":   dispatch.RootFrameID,
		"status":        dispatch.Status,
		"attempt":       dispatch.Attempt,
	}
	sessionConfig := map[string]any{"agentName": dispatch.AgentName}
	if controls, ok := dispatch.ResumeEvent.Payload["controls"].(map[string]any); ok {
		for key, value := range controls {
			sessionConfig[key] = value
		}
	}
	orchestration["sessionConfig"] = sessionConfig
	session.Orchestration = orchestration

	if _, err := s.sessionStore.SaveProjectionPreservingRunner(session); err != nil {
		return err
	}
	registrationClientID := "frame-resume-register:" + dispatch.ResumeEvent.ID
	registered, err := s.eventJournal.HasClientMessage(dispatch.FrameID, registrationClientID)
	if err != nil {
		return err
	}
	if !registered {
		if _, err := s.eventJournal.Append(dispatch.FrameID, eventjournal.Message{
			"type": "frame_resume_registered", "role": "system",
			"frameResumeEventId": dispatch.ResumeEvent.ID,
			"rootFrameId":        dispatch.RootFrameID,
			"controls":           dispatch.ResumeEvent.Payload["controls"],
		}, eventjournal.Metadata{ClientMessageID: registrationClientID}); err != nil {
			return err
		}
	}
	return nil
}

func legacyResumeHasUnresolvedSideEffectingTool(entries []eventjournal.Entry) bool {
	type invocationKey struct {
		runnerID string
		attempt  int
		callID   string
		toolName string
	}
	pending := make(map[invocationKey]struct{})
	for _, entry := range entries {
		message := entry.Message
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
			continue
		}
		callID := strings.TrimSpace(stringValue(message["toolCallId"]))
		if callID == "" {
			continue
		}
		toolName := canonicalLegacyResumeToolName(stringValue(message["toolName"]))
		key := invocationKey{
			runnerID: strings.TrimSpace(stringValue(message["runnerId"])),
			attempt:  int(numberValue(message["runnerAttempt"])),
			callID:   callID,
			toolName: toolName,
		}
		phase := strings.ToLower(strings.TrimSpace(stringValue(message["toolPhase"])))
		switch phase {
		case "start", "started", "running":
			pending[key] = struct{}{}
		case "completed", "complete", "finish", "finished":
			delete(pending, key)
		case "failed":
			if strings.TrimSpace(stringValue(message["toolOutcomeCertainty"])) == "not_started" {
				delete(pending, key)
			}
		case prestartToolFailurePhase:
			delete(pending, key)
		}
	}
	readOnly := make(map[string]struct{})
	for _, toolName := range legacyReplayReadOnlyToolNames() {
		readOnly[canonicalLegacyResumeToolName(toolName)] = struct{}{}
	}
	for key := range pending {
		if _, ok := readOnly[key.toolName]; !ok {
			return true
		}
	}
	return false
}

func canonicalLegacyResumeToolName(value string) string {
	value = strings.TrimSpace(value)
	if canonical, err := canonicalRuntimeToolName(value); err == nil {
		return canonical
	}
	return value
}

func isFrameResumeTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func runnerInterruptionPauseStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "awaiting_approval", "awaiting_user_response":
		return true
	default:
		return false
	}
}

func (s *Server) copyWorkspaceFrameMessagesToResumeJournal(frameID string) error {
	var after int64
	for {
		events, err := s.workspaceStore.ListFrameEvents(frameID, after, 1000)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, event := range events {
			after = event.Sequence
			role := ""
			switch event.Type {
			case "user_message":
				role = "user"
			case "assistant_message":
				role = "assistant"
			default:
				continue
			}
			clientID := "frame-resume-source:" + event.ID
			exists, err := s.eventJournal.HasClientMessage(frameID, clientID)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			message := eventjournal.Message{}
			for key, value := range event.Payload {
				message[key] = value
			}
			message["type"] = "message"
			message["role"] = role
			if _, err := s.eventJournal.Append(frameID, message, eventjournal.Metadata{ClientMessageID: clientID}); err != nil {
				return err
			}
		}
		if len(events) < 1000 {
			return nil
		}
	}
}
