package server

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"os"

	"path/filepath"

	"strconv"
	"strings"

	"time"

	eventjournal "synon-go/internal/persistence/journal"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func (s *Server) executeSynonLinkTool(ctx context.Context, input map[string]any) (string, any, error) {
	if err := s.validateRegisteredTool("synon_link", input); err != nil {
		return "", nil, err
	}
	userID := stringValue(input["userId"])
	clientID := stringValue(input["clientId"])
	action := stringValue(input["action"])
	payload := mapValue(input["payload"])
	command, resultCh, err := s.synonLink.SendCommand(ctx, userID, clientID, action, payload)
	if err != nil {
		return "", nil, err
	}
	select {
	case result := <-resultCh:
		if !result.OK {
			return "", nil, errors.New(result.Error)
		}
		return command.ID, result.Value, nil
	case <-ctx.Done():
		return "", nil, errors.New("Synon Link tool execution was cancelled")
	}
}

func (s *Server) appendSessionToolEvent(input map[string]any) (*eventjournal.Entry, error) {
	sessionID := stringValue(input["sessionId"])
	role := strings.TrimSpace(stringValue(input["role"]))
	if role == "" {
		return nil, fmt.Errorf("session_append.role is required")
	}
	if !isSessionRole(role) {
		return nil, fmt.Errorf("unsupported session role: %s", role)
	}
	session, ok, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	runnerID := strings.TrimSpace(stringValue(input["runnerId"]))
	if runnerID != "" {
		claim, err := runnerMutationClaim(input, "session_append")
		if err != nil {
			return nil, err
		}
		session, err = s.sessionStore.ValidateRunnerClaim(claim, true)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(stringValue(input["clientMessageId"])) == "" {
			return nil, errors.New("session_append.clientMessageId is required for runner-owned mutation")
		}
	}
	message, ok := input["message"].(map[string]any)
	if !ok || message == nil {
		return nil, fmt.Errorf("session_append.message must be an object")
	}
	journalMessage := eventjournal.Message{}
	for key, value := range message {
		journalMessage[key] = value
	}
	if _, ok := journalMessage["type"].(string); !ok {
		journalMessage["type"] = "message"
	}
	journalMessage["role"] = role
	if runnerID != "" {
		journalMessage["runnerId"] = runnerID
		journalMessage["runnerAttempt"] = session.Runner.Attempt
	}
	entry, _, err := s.eventJournal.AppendIdempotent(sessionID, journalMessage, eventjournal.Metadata{
		RunID:           stringValue(input["runId"]),
		ClientMessageID: stringValue(input["clientMessageId"]),
	})
	if err != nil {
		return nil, err
	}
	if err := s.sessionStore.AppendMessage(sessionID, role); err != nil {
		return nil, err
	}
	if err := s.mirrorSessionEntryToWorkspaceFrame(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func sessionRunnerOwnsLease(session sessionstore.Session, runnerID string, now time.Time) bool {
	if session.Runner == nil {
		return false
	}
	if session.Runner.RunnerID != runnerID {
		return false
	}
	return session.Runner.ExpiresAt.After(now)
}

func (s *Server) claimSessionRunner(input map[string]any) (map[string]any, error) {
	ttl, err := sessionLeaseTTL(input)
	if err != nil {
		return nil, err
	}
	session, claimed, err := s.sessionStore.ClaimRunner(
		stringValue(input["sessionId"]),
		strings.TrimSpace(stringValue(input["runnerId"])),
		ttl,
	)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"claimed": claimed, "session": session}
	if session.Runner != nil {
		result["ownerRunnerId"] = session.Runner.RunnerID
		if claimed {
			addRunnerClaimCredential(result, session)
		}
	}
	return result, nil
}

func (s *Server) heartbeatSessionRunner(input map[string]any) (map[string]any, error) {
	ttl, err := sessionLeaseTTL(input)
	if err != nil {
		return nil, err
	}
	claim, err := runnerMutationClaim(input, "session_heartbeat")
	if err != nil {
		return nil, err
	}
	session, renewed, err := s.sessionStore.HeartbeatRunner(claim, ttl)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"renewed": renewed, "session": session}
	if session.Runner != nil {
		result["ownerRunnerId"] = session.Runner.RunnerID
	}
	return result, nil
}

func (s *Server) releaseSessionRunner(input map[string]any) (map[string]any, error) {
	claim, err := runnerMutationClaim(input, "session_release")
	if err != nil {
		return nil, err
	}
	session, released, err := s.sessionStore.ReleaseRunner(claim)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"released": released, "session": session}
	if session.Runner != nil {
		result["ownerRunnerId"] = session.Runner.RunnerID
	}
	return result, nil
}

func (s *Server) checkpointSessionRunner(input map[string]any) (map[string]any, error) {
	sessionID := stringValue(input["sessionId"])
	runnerID := strings.TrimSpace(stringValue(input["runnerId"]))
	status := strings.TrimSpace(stringValue(input["status"]))
	claim, err := runnerMutationClaim(input, "session_runner_checkpoint")
	if err != nil {
		return nil, err
	}
	if !isRunnerCheckpointStatus(status) {
		return nil, fmt.Errorf("unsupported runner checkpoint status: %s", status)
	}
	clientMessageID := strings.TrimSpace(stringValue(input["clientMessageId"]))
	if clientMessageID == "" {
		return nil, errors.New("session_runner_checkpoint.clientMessageId is required")
	}
	session, err := s.sessionStore.ValidateRunnerClaim(claim, true)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	checkpointText := strings.TrimSpace(stringValue(input["message"]))
	message := eventjournal.Message{
		"type":          "runner_checkpoint",
		"role":          "system",
		"runnerId":      runnerID,
		"runnerAttempt": claim.Attempt,
		"status":        status,
	}
	addRunnerLeaseAuditToMessage(message, session.Runner)
	if checkpointText != "" {
		message["text"] = checkpointText
	}
	if afterEventID := numberValue(input["afterEventId"]); afterEventID > 0 {
		message["afterEventId"] = afterEventID
	}
	if _, present, err := transcriptstore.ParseRunnerInterruptionCause(input); err != nil {
		return nil, err
	} else if present {
		message[transcriptstore.RunnerInterruptionCauseField] = input[transcriptstore.RunnerInterruptionCauseField]
	}
	for _, key := range []string{"toolCallId", "toolName", "toolInput", "toolResult", "toolPhase", "modelToolCalls", "resumeCacheKey", "reasonCode", "resumeDetail", "planModeDenial"} {
		if value, ok := input[key]; ok && value != nil {
			message[key] = value
		}
	}
	entry, created, err := s.eventJournal.AppendIdempotent(sessionID, message, eventjournal.Metadata{
		RunID:           stringValue(input["runId"]),
		ClientMessageID: stringValue(input["clientMessageId"]),
	})
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errors.New("runner checkpoint was not persisted")
	}
	if err := s.mirrorTaskRunRunnerEvent(sessionID, entry); err != nil {
		return nil, err
	}
	session, err = s.sessionStore.CheckpointRunner(sessionstore.CheckpointRunnerInput{
		Claim: claim, Status: status, Checkpoint: checkpointText, EventID: entry.EventID, RecordedAt: now,
	})
	if err != nil {
		return nil, err
	}
	delivered := created && s.publishSessionEntry(entry)
	return map[string]any{"event": entry, "session": session, "delivered": delivered}, nil
}

func (s *Server) nextSessionRunner(input map[string]any) (map[string]any, error) {
	ttl, err := sessionLeaseTTL(input)
	if err != nil {
		return nil, err
	}
	sessionID := stringValue(input["sessionId"])
	runnerID := strings.TrimSpace(stringValue(input["runnerId"]))
	session, claimed, err := s.sessionStore.ClaimRunner(sessionID, runnerID, ttl)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"claimed":       claimed,
		"session":       session,
		"ownerRunnerId": "",
		"entries":       []eventjournal.Entry{},
		"nextEventId":   numberValue(input["afterEventId"]),
	}
	if session.Runner != nil {
		result["ownerRunnerId"] = session.Runner.RunnerID
		if claimed {
			addRunnerClaimCredential(result, session)
		}
	}
	if session.Project != nil {
		result["project"] = session.Project
	}
	if session.WorkDir != "" {
		result["workDir"] = session.WorkDir
	}
	if !claimed {
		return result, nil
	}
	entries, err := s.eventJournal.ReadAfter(
		sessionID,
		numberValue(input["afterEventId"]),
		sessionReplayLimit(numberValue(input["limit"])),
	)
	if err != nil {
		return nil, err
	}
	result["entries"] = entries
	result["nextEventId"] = maxEventID(numberValue(input["afterEventId"]), entries)
	return result, nil
}

func (s *Server) pickSessionRunner(input map[string]any) (map[string]any, error) {
	ttl, err := sessionLeaseTTL(input)
	if err != nil {
		return nil, err
	}
	runnerID := strings.TrimSpace(stringValue(input["runnerId"]))
	session, claimed, err := s.sessionStore.ClaimNextRunner(runnerID, ttl)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"claimed":       claimed,
		"ownerRunnerId": "",
		"entries":       []eventjournal.Entry{},
		"nextEventId":   numberValue(input["afterEventId"]),
	}
	if !claimed {
		return result, nil
	}
	result["session"] = session
	if session.Runner != nil {
		result["ownerRunnerId"] = session.Runner.RunnerID
		addRunnerClaimCredential(result, session)
	}
	if session.Project != nil {
		result["project"] = session.Project
	}
	if session.WorkDir != "" {
		result["workDir"] = session.WorkDir
	}
	entries, err := s.eventJournal.ReadAfter(
		session.ID,
		numberValue(input["afterEventId"]),
		sessionReplayLimit(numberValue(input["limit"])),
	)
	if err != nil {
		return nil, err
	}
	result["entries"] = entries
	result["nextEventId"] = maxEventID(numberValue(input["afterEventId"]), entries)
	return result, nil
}

func maxEventID(current int64, entries []eventjournal.Entry) int64 {
	maximum := current
	for _, entry := range entries {
		if entry.EventID > maximum {
			maximum = entry.EventID
		}
	}
	return maximum
}

func (s *Server) existingRunnerEventByClientMessageID(sessionID string, clientMessageID string, eventType string, runnerID string) (*eventjournal.Entry, bool, error) {
	clientMessageID = strings.TrimSpace(clientMessageID)
	if clientMessageID == "" {
		return nil, false, nil
	}
	entries, err := s.eventJournal.ReadByClientMessage(sessionID, clientMessageID)
	if err != nil {
		return nil, false, err
	}
	if len(entries) > 1 {
		return nil, false, fmt.Errorf("clientMessageId resolves to multiple durable runner events")
	}
	for _, entry := range entries {
		messageType, _ := entry.Message["type"].(string)
		messageRunnerID, _ := entry.Message["runnerId"].(string)
		if messageType == eventType && messageRunnerID == runnerID {
			matched := entry
			return &matched, true, nil
		}
	}
	if len(entries) > 0 {
		return nil, false, fmt.Errorf("clientMessageId is already used by another runner event")
	}
	return nil, false, nil
}

func (s *Server) finishSessionRunner(input map[string]any) (map[string]any, error) {
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	runnerID := strings.TrimSpace(stringValue(input["runnerId"]))
	status := strings.TrimSpace(stringValue(input["status"]))
	clientMessageID := strings.TrimSpace(stringValue(input["clientMessageId"]))
	if sessionID == "" {
		return nil, fmt.Errorf("session_runner_finish.sessionId is required")
	}
	if runnerID == "" {
		return nil, fmt.Errorf("session_runner_finish.runnerId is required")
	}
	if clientMessageID == "" {
		return nil, fmt.Errorf("session_runner_finish.clientMessageId is required")
	}
	if !isRunnerFinishStatus(status) {
		return nil, fmt.Errorf("unsupported runner finish status: %s", status)
	}
	claim, err := runnerMutationClaim(input, "session_runner_finish")
	if err != nil {
		return nil, err
	}
	session, err := s.sessionStore.ValidateRunnerClaim(claim, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	attempt := claim.Attempt
	finishText := strings.TrimSpace(stringValue(input["message"]))
	failureReason := strings.TrimSpace(stringValue(input["reasonCode"]))
	if failureReason != "" && (status != "failed" || !validRunnerFailureReason(failureReason)) {
		return nil, errors.New("runner failure reason is invalid for the terminal state")
	}
	message := eventjournal.Message{
		"type":          "runner_finished",
		"role":          "system",
		"runnerId":      runnerID,
		"runnerAttempt": attempt,
		"status":        status,
	}
	addRunnerLeaseAuditToMessage(message, session.Runner)
	message["runnerAttempt"] = attempt
	if finishText != "" {
		message["text"] = finishText
	}
	if failureReason != "" {
		message["reason_code"] = failureReason
	}
	if afterEventID := numberValue(input["afterEventId"]); afterEventID > 0 {
		message["afterEventId"] = afterEventID
	}
	existing, found, err := s.existingRunnerEventByClientMessageID(sessionID, clientMessageID, "runner_finished", runnerID)
	if err != nil {
		return nil, err
	}
	if found {
		if err := validateRunnerFinishEntry(existing, message, stringValue(input["runId"]), clientMessageID); err != nil {
			return nil, fmt.Errorf("clientMessageId is already used by a different durable event: %w", eventjournal.ErrIdempotencyConflict)
		}
	}
	if session.Runner == nil || session.Runner.RunnerID != runnerID || session.Runner.Attempt != attempt {
		if found {
			return map[string]any{"event": existing, "session": session, "delivered": false}, nil
		}
		return nil, sessionstore.ErrRunnerClaimStale
	}
	supersedesCheckpointEventID := int64(0)
	if isRunnerFinishStatus(session.Runner.Status) {
		checkpoint, err := s.runnerTerminalCheckpointEvent(
			sessionID, runnerID, attempt, session.Runner.Status, session.Runner.LastCheckpoint, session.Runner.LastCheckpointEventID,
		)
		if err != nil {
			return nil, err
		}
		if checkpoint {
			if !sessionRunnerOwnsLease(session, runnerID, now) {
				return nil, sessionstore.ErrRunnerClaimStale
			}
			supersedesCheckpointEventID = session.Runner.LastCheckpointEventID
		} else if session.Runner.Status != status {
			return nil, fmt.Errorf("%w: runner status %s does not match requested status %s", sessionstore.ErrRunnerFinalizeConflict, session.Runner.Status, status)
		}
	} else if !sessionRunnerOwnsLease(session, runnerID, now) {
		return nil, fmt.Errorf("session runner lease is not owned by %s", runnerID)
	}
	entry, created, err := s.eventJournal.AppendIdempotent(sessionID, message, eventjournal.Metadata{
		RunID:           stringValue(input["runId"]),
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		if errors.Is(err, eventjournal.ErrIdempotencyConflict) {
			return nil, fmt.Errorf("clientMessageId is already used by a different durable event: %w", err)
		}
		return nil, err
	}
	if entry == nil {
		return nil, errors.New("runner finish was not persisted")
	}
	if err := validateRunnerFinishEntry(entry, message, stringValue(input["runId"]), clientMessageID); err != nil {
		return nil, err
	}
	if err := s.mirrorTaskRunRunnerEvent(sessionID, entry); err != nil {
		return nil, err
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, entry.CreatedAt)
	if err != nil {
		return nil, errors.New("runner finish has an invalid durable timestamp")
	}
	session, _, err = s.sessionStore.FinalizeRunner(sessionstore.FinalizeRunnerInput{
		SessionID: sessionID, RunnerID: runnerID, Attempt: attempt, Status: status,
		Checkpoint: finishText, CheckpointAt: finishedAt, EventID: entry.EventID, FinishedAt: finishedAt,
		SupersedesCheckpointEventID: supersedesCheckpointEventID,
	})
	if err != nil {
		return nil, fmt.Errorf("finalize runner attempt: %w", err)
	}
	if _, err := s.advanceCompatibilityFrameAfterRunner(sessionID, status); err != nil {
		return nil, err
	}
	if updated, ok, err := s.sessionStore.Get(sessionID); err != nil {
		return nil, err
	} else if ok {
		session = updated
	}
	delivered := created && s.publishSessionEntry(entry)
	return map[string]any{"event": entry, "session": session, "delivered": delivered}, nil
}

func (s *Server) runnerTerminalCheckpointEvent(sessionID, runnerID string, attempt int, status, checkpointText string, eventID int64) (bool, error) {
	if eventID <= 0 {
		return false, nil
	}
	entries, err := s.eventJournal.ReadAfter(sessionID, eventID-1, 1)
	if err != nil {
		return false, err
	}
	if len(entries) != 1 || entries[0].EventID != eventID {
		return false, nil
	}
	entry := entries[0]
	return strings.TrimSpace(stringValue(entry.Message["type"])) == "runner_checkpoint" &&
		strings.TrimSpace(stringValue(entry.Message["runnerId"])) == runnerID &&
		int(numberValue(entry.Message["runnerAttempt"])) == attempt &&
		strings.TrimSpace(stringValue(entry.Message["status"])) == strings.TrimSpace(status) &&
		strings.TrimSpace(stringValue(entry.Message["text"])) == strings.TrimSpace(checkpointText), nil
}

func runnerMutationClaim(input map[string]any, toolName string) (sessionstore.RunnerMutationClaim, error) {
	claim := sessionstore.RunnerMutationClaim{
		SessionID:  strings.TrimSpace(stringValue(input["sessionId"])),
		RunnerID:   strings.TrimSpace(stringValue(input["runnerId"])),
		ClaimToken: strings.TrimSpace(stringValue(input["claimToken"])),
	}
	if claim.SessionID == "" {
		return sessionstore.RunnerMutationClaim{}, fmt.Errorf("%s.sessionId is required", toolName)
	}
	if claim.RunnerID == "" {
		return sessionstore.RunnerMutationClaim{}, fmt.Errorf("%s.runnerId is required", toolName)
	}
	attempt, valid := exactPositiveInt(input["runnerAttempt"])
	if !valid {
		return sessionstore.RunnerMutationClaim{}, fmt.Errorf("%s.runnerAttempt must be a positive integer", toolName)
	}
	if claim.ClaimToken == "" {
		return sessionstore.RunnerMutationClaim{}, fmt.Errorf("%s.claimToken is required", toolName)
	}
	claim.Attempt = attempt
	return claim, nil
}

func addRunnerClaimCredential(result map[string]any, session sessionstore.Session) {
	claim := sessionstore.RunnerClaimFromSession(session)
	if claim.Attempt <= 0 || claim.ClaimToken == "" {
		return
	}
	result["runnerAttempt"] = claim.Attempt
	result["claimToken"] = claim.ClaimToken
}

func exactPositiveInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed > 0
	case int64:
		return int(typed), typed > 0 && int64(int(typed)) == typed
	case float64:
		converted := int(typed)
		return converted, typed > 0 && float64(converted) == typed
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 32)
		return int(parsed), err == nil && parsed > 0
	default:
		return 0, false
	}
}

func validateRunnerFinishEntry(entry *eventjournal.Entry, expected eventjournal.Message, runID, clientMessageID string) error {
	if entry == nil || strings.TrimSpace(entry.RunID) != strings.TrimSpace(runID) ||
		strings.TrimSpace(entry.ClientMessageID) != strings.TrimSpace(clientMessageID) {
		return errors.New("runner finish durable metadata does not match the request")
	}
	for _, key := range []string{"type", "role", "runnerId", "runnerAttempt", "status", "text", "afterEventId", "reason_code"} {
		actual, actualOK := entry.Message[key]
		wanted, wantedOK := expected[key]
		if actualOK != wantedOK || (actualOK && fmt.Sprint(actual) != fmt.Sprint(wanted)) {
			return errors.New("runner finish durable payload does not match the request")
		}
	}
	return nil
}

func addRunnerLeaseAuditToMessage(message eventjournal.Message, runner *sessionstore.Runner) {
	if message == nil || runner == nil {
		return
	}
	if runner.Attempt > 0 {
		message["runnerAttempt"] = runner.Attempt
	}
	if runner.ReclaimedExpiredLease {
		message["leaseReclaimed"] = true
	}
	if runner.PreviousRunnerID != "" {
		message["previousRunnerId"] = runner.PreviousRunnerID
	}
}

func isRunnerFinishStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isRunnerCheckpointStatus(status string) bool {
	switch status {
	case "running", "waiting", "blocked", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func (s *Server) bindSessionProject(input map[string]any) (map[string]any, error) {
	sessionID := stringValue(input["sessionId"])
	projectID := strings.TrimSpace(stringValue(input["projectId"]))
	if projectID == "" {
		return nil, fmt.Errorf("session_bind_project.projectId is required")
	}
	workDir, rel, err := resolveSessionProjectPath(s.fileRoot, stringValue(input["path"]))
	if err != nil {
		return nil, err
	}
	session, ok, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	now := time.Now().UTC()
	session.WorkDir = workDir
	session.UpdatedAt = now
	session.Project = &sessionstore.Project{
		ID:      projectID,
		Name:    strings.TrimSpace(stringValue(input["projectName"])),
		Path:    rel,
		BoundAt: now,
	}
	if err := s.sessionStore.Upsert(session); err != nil {
		return nil, err
	}
	return map[string]any{"session": session}, nil
}

func (s *Server) forkSession(input map[string]any) (map[string]any, error) {
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	sourceSessionID := strings.TrimSpace(stringValue(input["sourceSessionId"]))
	if sessionID == "" {
		return nil, errors.New("session_fork.sessionId is required")
	}
	if sourceSessionID == "" {
		return nil, errors.New("session_fork.sourceSessionId is required")
	}
	if sessionID == sourceSessionID {
		return nil, errors.New("session_fork.sessionId must differ from sourceSessionId")
	}
	source, ok, err := s.sessionStore.Get(sourceSessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("source session not found: %s", sourceSessionID)
	}
	if _, exists, err := s.sessionStore.Get(sessionID); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("target session already exists: %s", sessionID)
	}

	entries, err := s.eventJournal.CloneSession(sourceSessionID, sessionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	forked := source
	forked.ID = sessionID
	forked.CreatedAt = now
	forked.UpdatedAt = now
	if title := strings.TrimSpace(stringValue(input["title"])); title != "" {
		forked.Title = title
	} else {
		forked.Title = "Fork of " + source.Title
	}
	if forked.Title == "Fork of " {
		forked.Title = "Fork of " + sourceSessionID
	}
	if workDir := strings.TrimSpace(stringValue(input["workDir"])); workDir != "" {
		forked.WorkDir = workDir
	}
	if boolValue(input["resetRunner"], true) {
		forked.Runner = nil
	}
	if forked.Project != nil {
		project := *forked.Project
		if projectID := strings.TrimSpace(stringValue(input["projectId"])); projectID != "" {
			project.ID = projectID
		}
		if projectName := strings.TrimSpace(stringValue(input["projectName"])); projectName != "" {
			project.Name = projectName
		}
		project.BoundAt = now
		forked.Project = &project
	}
	forked = sessionWithJournalStats(forked, entries)
	if err := s.sessionStore.Save(forked); err != nil {
		return nil, err
	}
	return map[string]any{
		"session":         forked,
		"sourceSessionId": sourceSessionID,
		"targetSessionId": sessionID,
		"eventCount":      len(entries),
		"lastEventId":     maxEventID(0, entries),
		"audit": map[string]any{
			"operation":       "fork",
			"sourceSessionId": sourceSessionID,
			"targetSessionId": sessionID,
			"clonedEvents":    len(entries),
			"lastEventId":     maxEventID(0, entries),
			"resetRunner":     boolValue(input["resetRunner"], true),
		},
	}, nil
}

func (s *Server) rewindSession(input map[string]any) (map[string]any, error) {
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	if sessionID == "" {
		return nil, errors.New("session_rewind.sessionId is required")
	}
	afterEventID := numberValue(input["afterEventId"])
	if afterEventID < 0 {
		return nil, errors.New("session_rewind.afterEventId must be non-negative")
	}
	session, ok, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	before, err := s.eventJournal.ReadAll(sessionID)
	if err != nil {
		return nil, err
	}
	kept, err := s.eventJournal.TruncateAfter(sessionID, afterEventID)
	if err != nil {
		return nil, err
	}
	removedCount := len(before) - len(kept)
	removedLastEventID := maxEventID(0, before)
	session = sessionWithJournalStats(session, kept)
	session.UpdatedAt = time.Now().UTC()
	if boolValue(input["resetRunner"], true) {
		session.Runner = nil
	}
	if err := s.sessionStore.Save(session); err != nil {
		return nil, err
	}
	return map[string]any{
		"session":      session,
		"eventCount":   len(kept),
		"lastEventId":  maxEventID(0, kept),
		"afterEventId": afterEventID,
		"removedCount": removedCount,
		"audit": map[string]any{
			"operation":           "rewind",
			"sessionId":           sessionID,
			"keptEvents":          len(kept),
			"removedEvents":       removedCount,
			"lastEventIdBefore":   removedLastEventID,
			"lastEventIdAfter":    maxEventID(0, kept),
			"rewoundAfterEventId": afterEventID,
			"resetRunner":         boolValue(input["resetRunner"], true),
		},
	}, nil
}

func (s *Server) exportSession(input map[string]any) (map[string]any, error) {
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	if sessionID == "" {
		return nil, errors.New("session_export.sessionId is required")
	}
	session, ok, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	limit := 500
	if _, ok := input["maxEntries"]; ok {
		limit = sessionReplayLimit(numberValue(input["maxEntries"]))
	}
	entries, err := s.eventJournal.ReadAfter(sessionID, 0, limit)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(stringValue(input["format"])))
	if format == "" {
		format = "json"
	}
	includeMeta := boolValue(input["includeMeta"], true)
	var data []byte
	switch format {
	case "json":
		payload := map[string]any{
			"sessionId":  sessionID,
			"eventCount": len(entries),
			"entries":    entries,
		}
		if includeMeta {
			payload["session"] = session
		}
		data, err = json.MarshalIndent(payload, "", "  ")
	case "jsonl":
		lines := make([]string, 0, len(entries))
		for _, entry := range entries {
			encoded, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				return nil, marshalErr
			}
			lines = append(lines, string(encoded))
		}
		if len(lines) == 0 {
			data = []byte{}
		} else {
			data = []byte(strings.Join(lines, "\n") + "\n")
		}
	case "markdown", "md":
		format = "markdown"
		data = []byte(formatSessionTranscriptMarkdown(session, entries, includeMeta))
	case "text", "transcript":
		format = "text"
		data = []byte(formatSessionTranscriptText(session, entries, includeMeta))
	default:
		return nil, fmt.Errorf("session_export.format must be json, jsonl, markdown, or text, got %q", format)
	}
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"sessionId":   sessionID,
		"format":      format,
		"eventCount":  len(entries),
		"lastEventId": maxEventID(0, entries),
		"bytes":       len(data),
	}
	if outputPath := strings.TrimSpace(stringValue(input["outputPath"])); outputPath != "" {
		resolved, rel, err := resolveSessionExportPath(s.fileRoot, outputPath)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(resolved, data, 0o600); err != nil {
			return nil, err
		}
		result["path"] = resolved
		result["relativePath"] = rel
		registered, err := s.registerArtifactWithPolicy(map[string]any{
			"path":        rel,
			"kind":        "session_export",
			"title":       "Session export " + sessionID,
			"description": "Durable session transcript export.",
			"sessionId":   sessionID,
			"metadata": map[string]any{
				"format":      format,
				"eventCount":  len(entries),
				"lastEventId": maxEventID(0, entries),
			},
		}, true)
		if err != nil {
			return nil, err
		}
		if artifact, ok := registered["artifact"]; ok {
			result["artifact"] = artifact
			if artifactMap, ok := artifact.(map[string]any); ok {
				result["artifactId"] = artifactMap["artifactId"]
			}
		}
	} else {
		result["content"] = string(data)
	}
	return result, nil
}

type sessionTranscriptLine struct {
	EventID         int64
	CreatedAt       string
	Role            string
	Type            string
	Text            string
	RunID           string
	ClientMessageID string
}

func sessionTranscriptLines(entries []eventjournal.Entry) []sessionTranscriptLine {
	lines := make([]sessionTranscriptLine, 0, len(entries))
	for _, entry := range entries {
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		messageType := strings.TrimSpace(stringValue(entry.Message["type"]))
		text := runnerMessageText(entry.Message)
		if modelToolText := runnerModelToolCallsTranscriptText(entry.Message); modelToolText != "" {
			if text == "" {
				text = modelToolText
			} else {
				text += "\n" + modelToolText
			}
		}
		if toolText := runnerToolTranscriptText(entry.Message); toolText != "" {
			if text == "" {
				text = toolText
			} else {
				text += "\n" + toolText
			}
		}
		if text == "" {
			text = firstNonEmpty(
				stringValue(entry.Message["summary"]),
				stringValue(entry.Message["message"]),
				stringValue(entry.Message["status"]),
				messageType,
			)
		}
		if role == "" {
			role = firstNonEmpty(stringValue(entry.Message["source"]), messageType, "event")
		}
		if messageType == "" {
			messageType = "message"
		}
		lines = append(lines, sessionTranscriptLine{
			EventID:         entry.EventID,
			CreatedAt:       entry.CreatedAt,
			Role:            role,
			Type:            messageType,
			Text:            strings.TrimSpace(text),
			RunID:           firstNonEmpty(entry.RunID, stringValue(entry.Message["runId"])),
			ClientMessageID: firstNonEmpty(entry.ClientMessageID, stringValue(entry.Message["clientMessageId"])),
		})
	}
	return lines
}

func runnerModelToolCallsTranscriptText(message eventjournal.Message) string {
	calls, ok := message["modelToolCalls"]
	if !ok || calls == nil {
		return ""
	}
	if formatted := formatTranscriptJSONValue(calls); formatted != "" {
		return "Model Tool Calls: " + formatted
	}
	return ""
}

func runnerToolTranscriptText(message eventjournal.Message) string {
	toolName := strings.TrimSpace(stringValue(message["toolName"]))
	if toolName == "" {
		return ""
	}
	lines := []string{"Tool: " + toolName}
	if callID := strings.TrimSpace(stringValue(message["toolCallId"])); callID != "" {
		lines = append(lines, "Tool Call: "+callID)
	}
	if status := strings.TrimSpace(stringValue(message["status"])); status != "" {
		lines = append(lines, "Status: "+status)
	}
	if input := formatTranscriptJSONValue(message["toolInput"]); input != "" {
		lines = append(lines, "Input: "+input)
	}
	if result := formatTranscriptJSONValue(message["toolResult"]); result != "" {
		lines = append(lines, "Result: "+result)
	}
	return strings.Join(lines, "\n")
}

func formatTranscriptJSONValue(value any) string {
	if value == nil {
		return ""
	}
	if text := strings.TrimSpace(stringValue(value)); text != "" {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func formatSessionTranscriptMarkdown(session sessionstore.Session, entries []eventjournal.Entry, includeMeta bool) string {
	var b strings.Builder
	title := firstNonEmpty(session.Title, session.ID)
	b.WriteString("# Session Transcript\n\n")
	b.WriteString("- Session: " + session.ID + "\n")
	b.WriteString("- Title: " + title + "\n")
	b.WriteString(fmt.Sprintf("- Events: %d\n", len(entries)))
	if includeMeta {
		if session.WorkDir != "" {
			b.WriteString("- Workdir: " + session.WorkDir + "\n")
		}
		if session.Project != nil {
			b.WriteString("- Project: " + firstNonEmpty(session.Project.Name, session.Project.ID) + "\n")
		}
	}
	b.WriteString("\n")
	for _, line := range sessionTranscriptLines(entries) {
		b.WriteString(fmt.Sprintf("## Event %d - %s", line.EventID, line.Role))
		if line.Type != "" && line.Type != "message" {
			b.WriteString(" (" + line.Type + ")")
		}
		b.WriteString("\n\n")
		if includeMeta {
			b.WriteString("- Created: " + line.CreatedAt + "\n")
			if line.RunID != "" {
				b.WriteString("- Run: " + line.RunID + "\n")
			}
			if line.ClientMessageID != "" {
				b.WriteString("- Client Message: " + line.ClientMessageID + "\n")
			}
			b.WriteString("\n")
		}
		if line.Text != "" {
			b.WriteString(line.Text + "\n\n")
		}
	}
	return b.String()
}

func formatSessionTranscriptText(session sessionstore.Session, entries []eventjournal.Entry, includeMeta bool) string {
	lines := []string{
		"Session: " + session.ID,
		"Title: " + firstNonEmpty(session.Title, session.ID),
		fmt.Sprintf("Events: %d", len(entries)),
	}
	if includeMeta && session.WorkDir != "" {
		lines = append(lines, "Workdir: "+session.WorkDir)
	}
	lines = append(lines, "")
	for _, line := range sessionTranscriptLines(entries) {
		header := fmt.Sprintf("[%d] %s", line.EventID, line.Role)
		if line.Type != "" && line.Type != "message" {
			header += " " + line.Type
		}
		if includeMeta && line.CreatedAt != "" {
			header += " " + line.CreatedAt
		}
		lines = append(lines, header)
		if line.Text != "" {
			lines = append(lines, line.Text)
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func sessionWithJournalStats(session sessionstore.Session, entries []eventjournal.Entry) sessionstore.Session {
	session.MessageCount = 0
	session.LastRole = ""
	session.LastUserMessageAt = time.Time{}
	for _, entry := range entries {
		if !isSessionStatsEntry(entry) {
			continue
		}
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		if role == "" {
			continue
		}
		session.MessageCount++
		session.LastRole = role
		if role == "user" {
			if parsed, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); err == nil {
				session.LastUserMessageAt = parsed
			}
		}
	}
	return session
}

func isSessionStatsEntry(entry eventjournal.Entry) bool {
	messageType := strings.TrimSpace(stringValue(entry.Message["type"]))
	switch messageType {
	case "runner_checkpoint", "runner_finished":
		return false
	default:
		return true
	}
}

func resolveSessionExportPath(root string, requestedPath string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", errors.New("file tool root is not configured")
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return "", "", errors.New("session_export.outputPath is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	var target string
	if filepath.IsAbs(requestedPath) {
		target, err = filepath.Abs(requestedPath)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requestedPath))
	}
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("export path escapes file root: %s", filepath.ToSlash(rel))
	}
	if rel == "" {
		return "", "", errors.New("session_export.outputPath must name a file")
	}
	return target, filepath.ToSlash(rel), nil
}

func resolveSessionProjectPath(root string, requestedPath string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", errors.New("file tool root is not configured")
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return "", "", errors.New("session_bind_project.path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	var target string
	if filepath.IsAbs(requestedPath) {
		target, err = filepath.Abs(requestedPath)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requestedPath))
	}
	if err != nil {
		return "", "", err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("project path escapes file root: %s", filepath.ToSlash(rel))
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("project path is not a directory: %s", filepath.ToSlash(rel))
	}
	if rel == "" {
		rel = "."
	}
	return target, filepath.ToSlash(rel), nil
}

func sessionReplayLimit(limit int64) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return int(limit)
}

func sessionLeaseTTL(input map[string]any) (time.Duration, error) {
	raw, exists := input["ttlSeconds"]
	if !exists || raw == nil {
		return 2 * time.Minute, nil
	}
	seconds := numberValue(raw)
	if seconds <= 0 {
		return 0, fmt.Errorf("ttlSeconds must be positive")
	}
	if seconds > 3600 {
		return 0, fmt.Errorf("ttlSeconds must be at most 3600")
	}
	return time.Duration(seconds) * time.Second, nil
}

func isSessionRole(role string) bool {
	switch role {
	case "user", "assistant", "system", "tool":
		return true
	default:
		return false
	}
}
