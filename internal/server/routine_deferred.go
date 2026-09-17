package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/routinescheduler"
)

const routineDeferredOrchestrationKey = "routineDeferred"

type routineDeferredMarker struct {
	RoutineID   string
	TickAttempt int
	RunnerID    string
}

func (e *routineExecutor) persistRoutineDeferred(tick routinescheduler.Tick, runnerID string) error {
	session, found, err := e.server.sessionStore.Get(tick.RootFrameID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("routine deferred session %q was not found", tick.RootFrameID)
	}
	session.Orchestration = copyStringAnyMap(session.Orchestration)
	session.Orchestration[routineDeferredOrchestrationKey] = map[string]any{
		"routineId": tick.RoutineID, "tickAttempt": tick.Attempt,
		"runnerId": runnerID, "deferredAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := e.server.sessionStore.Upsert(session); err != nil {
		return fmt.Errorf("persist routine deferred marker: %w", err)
	}
	event, err := e.server.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
		ID: routineTickStableID("waiting", tick), FrameID: tick.RootFrameID, Type: "routine_tick_waiting",
		Payload: map[string]any{
			"routineId": tick.RoutineID, "tickAttempt": tick.Attempt,
			"runnerId": runnerID, "status": "awaiting_user_response",
		},
	})
	if err != nil {
		return fmt.Errorf("persist routine waiting event: %w", err)
	}
	if err := e.server.publishWorkspaceEvent(event); err != nil {
		return fmt.Errorf("publish routine waiting event: %w", err)
	}
	return nil
}

func (e *routineExecutor) reconcileDeferredRoutineTick(
	ctx context.Context,
	frame workspace.Frame,
	tick routinescheduler.Tick,
) (routinescheduler.Result, bool, error) {
	session, found, err := e.server.sessionStore.Get(frame.ID)
	if err != nil || !found {
		return routinescheduler.Result{}, false, err
	}
	marker, found := routineDeferredMarkerFromSession(session)
	if !found || marker.RoutineID != tick.RoutineID || marker.TickAttempt != tick.Attempt {
		return routinescheduler.Result{}, false, nil
	}

	status := strings.ToLower(strings.TrimSpace(frame.Status))
	switch status {
	case "completed", "failed", "cancelled":
		cycle, summary, err := e.deferredRoutineTerminalCycle(ctx, frame, session)
		if err != nil {
			return routinescheduler.Result{}, true, err
		}
		successful := status == "completed" && cycle.Status == "completed"
		if !successful && strings.TrimSpace(summary) == "" {
			summary = fmt.Sprintf("routine Agent resume ended with frame=%s runner=%s", status, cycle.Status)
		}
		summary = boundRoutineOutcomeSummary(summary)
		if err := e.persistRoutineOutcome(tick, marker.RunnerID, cycle, summary, successful); err != nil {
			return routinescheduler.Result{}, true, err
		}
		if err := e.clearRoutineDeferred(tick); err != nil {
			return routinescheduler.Result{}, true, err
		}
		result := routinescheduler.Result{Summary: summary}
		if successful {
			return result, true, nil
		}
		return result, true, errors.New(summary)
	default:
		return routinescheduler.Result{Summary: "awaiting user response", Deferred: true}, true, nil
	}
}

func routineDeferredMarkerFromSession(session sessionstore.Session) (routineDeferredMarker, bool) {
	raw, ok := session.Orchestration[routineDeferredOrchestrationKey].(map[string]any)
	if !ok {
		return routineDeferredMarker{}, false
	}
	marker := routineDeferredMarker{
		RoutineID: strings.TrimSpace(stringValue(raw["routineId"])),
		RunnerID:  strings.TrimSpace(stringValue(raw["runnerId"])),
	}
	marker.TickAttempt = int(numberValue(raw["tickAttempt"]))
	return marker, marker.RoutineID != "" && marker.TickAttempt > 0 && marker.RunnerID != ""
}

func (e *routineExecutor) deferredRoutineTerminalCycle(
	ctx context.Context,
	frame workspace.Frame,
	session sessionstore.Session,
) (SessionRunnerCycleResult, string, error) {
	if e.server.transcriptStore != nil && e.server.workspaceStore != nil {
		ownerID, found, err := e.server.workspaceStore.ProjectOwnerID(frame.ProjectID)
		if err != nil {
			return SessionRunnerCycleResult{}, "", err
		}
		if !found || strings.TrimSpace(ownerID) == "" {
			return SessionRunnerCycleResult{}, "", errors.New("routine resume frame owner is unavailable")
		}
		stream, found, err := e.server.transcriptStore.GetFrameStreamBySession(ctx, ownerID, frame.ID)
		if err != nil {
			return SessionRunnerCycleResult{}, "", err
		}
		if found {
			return e.deferredTranscriptRoutineTerminalCycle(ctx, stream)
		}
	}
	if session.Runner == nil {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume reached a terminal frame without a terminal session runner")
	}
	status := strings.TrimSpace(session.Runner.Status)
	if status != "completed" && status != "failed" && status != "cancelled" {
		return SessionRunnerCycleResult{}, "", fmt.Errorf("routine resume session runner is not terminal: %s", status)
	}
	finishEventID := session.Runner.LastCheckpointEventID
	if finishEventID <= 0 {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume terminal runner has no finish event")
	}
	entries, err := e.server.eventJournal.ReadAfter(session.ID, finishEventID-1, 1)
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	if len(entries) != 1 || entries[0].EventID != finishEventID || strings.TrimSpace(stringValue(entries[0].Message["type"])) != "runner_finished" {
		return SessionRunnerCycleResult{}, "", fmt.Errorf("routine resume finish event %d was not found", finishEventID)
	}
	journalStatus := strings.TrimSpace(stringValue(entries[0].Message["status"]))
	if journalStatus != status {
		return SessionRunnerCycleResult{}, "", fmt.Errorf("routine resume runner status %q disagrees with journal status %q", status, journalStatus)
	}
	assistantEventID := numberValue(entries[0].Message["afterEventId"])
	summary, err := e.journalMessageText(session.ID, assistantEventID)
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	if status != "completed" && strings.TrimSpace(summary) == "" {
		summary = strings.TrimSpace(stringValue(entries[0].Message["text"]))
	}
	cycle := SessionRunnerCycleResult{
		Claimed: true, SessionID: session.ID, RunnerID: session.Runner.RunnerID,
		Attempt: session.Runner.Attempt, Status: status,
		AssistantEventID: assistantEventID, FinishEventID: finishEventID,
	}
	return cycle, summary, nil
}

func (e *routineExecutor) deferredTranscriptRoutineTerminalCycle(
	ctx context.Context,
	stream transcriptstore.Stream,
) (SessionRunnerCycleResult, string, error) {
	state, found, err := e.server.transcriptStore.GetLatestRunnerRuntimeState(
		ctx, stream.UID, stream.OwnerID,
	)
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	if !found {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume has no Transcript runner")
	}
	status := strings.TrimSpace(state.Status)
	if state.StreamUID != stream.UID || state.Attempt <= 0 || int64(int(state.Attempt)) != state.Attempt ||
		strings.TrimSpace(state.RunnerID) == "" ||
		(status != "completed" && status != "failed" && status != "cancelled") {
		return SessionRunnerCycleResult{}, "", fmt.Errorf("routine resume Transcript runner is not terminal: %s", status)
	}
	if state.Phase != transcriptstore.RunnerPhaseTerminal || state.FinishedAt == nil || state.FinishedEventID <= 0 {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume terminal Transcript runner has no finish event")
	}
	replay, err := e.server.transcriptStore.ListRunnerReplay(ctx, transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 1000, CheckpointLimit: 0,
	})
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	var assistantEventID int64
	for index := len(replay) - 1; index >= 0; index-- {
		item := replay[index]
		if item.Event.Type != "assistant_message" || item.Event.RunnerAttempt == nil ||
			*item.Event.RunnerAttempt != state.Attempt {
			continue
		}
		assistantEventID = item.Event.EventID
		break
	}
	messages, _, err := e.server.projectTranscriptWebHistory(ctx, stream, stream.OwnerID, stream.SessionID, "")
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	summary, err := transcriptRoutineTerminalSummary(messages, stream.SessionID, state.Attempt, status)
	if err != nil {
		return SessionRunnerCycleResult{}, "", err
	}
	if status == "completed" && assistantEventID <= 0 {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume completed without a Transcript assistant event")
	}
	if status == "completed" && strings.TrimSpace(summary) == "" {
		return SessionRunnerCycleResult{}, "", errors.New("routine resume completed without a Transcript assistant result")
	}
	cycle := SessionRunnerCycleResult{
		Claimed: true, SessionID: stream.SessionID, RunnerID: state.RunnerID,
		Attempt: int(state.Attempt), Status: status,
		AssistantEventID: assistantEventID, FinishEventID: state.FinishedEventID,
	}
	return cycle, summary, nil
}

func transcriptRoutineTerminalSummary(
	messages []map[string]any,
	sessionID string,
	attempt int64,
	status string,
) (string, error) {
	baseID := fmt.Sprintf("assistant-%s-%d", sessionID, attempt)
	eventPrefix := baseID + "-event-"
	matched := 0
	summary := ""
	for _, message := range messages {
		id := webString(message["id"])
		if webString(message["type"]) != "text" || webString(message["position"]) != "left" ||
			webString(message["terminal_status"]) != status ||
			!transcriptRoutineAssistantIdentityMatchesAttempt(id, baseID, eventPrefix) {
			continue
		}
		content, ok := message["content"].(map[string]any)
		if !ok {
			return "", transcriptstore.ErrEventConflict
		}
		text, ok := content["content"].(string)
		if !ok {
			return "", transcriptstore.ErrEventConflict
		}
		matched++
		summary = strings.TrimSpace(text)
	}
	if matched != 1 {
		return "", transcriptstore.ErrEventConflict
	}
	return summary, nil
}

func transcriptRoutineAssistantIdentityMatchesAttempt(id, baseID, eventPrefix string) bool {
	if id == baseID {
		return true
	}
	if strings.HasPrefix(id, eventPrefix) {
		eventID, err := strconv.ParseInt(strings.TrimPrefix(id, eventPrefix), 10, 64)
		return err == nil && eventID > 0
	}
	segmentPrefix := baseID + "-segment-"
	if strings.HasPrefix(id, segmentPrefix) {
		ordinal, err := strconv.ParseInt(strings.TrimPrefix(id, segmentPrefix), 10, 64)
		return err == nil && ordinal > 1
	}
	return false
}

func (e *routineExecutor) clearRoutineDeferred(tick routinescheduler.Tick) error {
	session, found, err := e.server.sessionStore.Get(tick.RootFrameID)
	if err != nil || !found {
		return err
	}
	marker, found := routineDeferredMarkerFromSession(session)
	if !found || marker.RoutineID != tick.RoutineID || marker.TickAttempt != tick.Attempt {
		return nil
	}
	session.Orchestration = copyStringAnyMap(session.Orchestration)
	delete(session.Orchestration, routineDeferredOrchestrationKey)
	if err := e.server.sessionStore.Upsert(session); err != nil {
		return fmt.Errorf("clear routine deferred marker: %w", err)
	}
	return nil
}
