package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	taskruns "synon-go/internal/persistence/taskruns"
	"time"
)

func (s *Server) autoRepairTaskRun(runID string, message string) (taskruns.Record, bool, error) {
	run, err := s.reconcileTaskRun(runID)
	if err != nil {
		return taskruns.Record{}, false, err
	}
	repaired, queued, err := s.taskRunStore.AutoRepair(run.RunID, message)
	if err != nil {
		return taskruns.Record{}, false, err
	}
	if !queued {
		return repaired, false, nil
	}
	queuedRun, err := s.queueTaskRunSession(repaired, firstNonEmpty(message, "TaskRun auto-repair queued."))
	if err != nil {
		return taskruns.Record{}, false, err
	}
	return queuedRun, true, nil
}

func (s *Server) reconcileTaskRun(runID string) (taskruns.Record, error) {
	run, found, err := s.taskRunStore.Get(runID)
	if err != nil {
		return taskruns.Record{}, err
	}
	if !found {
		return taskruns.Record{}, fmt.Errorf("TaskRun not found: %s", runID)
	}
	if s.sessionStore == nil || s.eventJournal == nil || run.SessionID == "" {
		if err := s.syncTaskRunGoalLedger(run); err != nil {
			return taskruns.Record{}, err
		}
		return run, nil
	}
	session, ok, err := s.sessionStore.Get(run.SessionID)
	if err != nil || !ok || session.Runner == nil {
		if err == nil {
			err = s.syncTaskRunGoalLedger(run)
		}
		return run, err
	}
	assistant := s.latestTaskRunAssistantMessage(run.SessionID)
	reconciled, err := s.taskRunStore.ReconcileWithRunner(run.RunID, session.Runner.Status, assistant)
	if err != nil {
		return taskruns.Record{}, err
	}
	if err := s.syncTaskRunGoalLedger(reconciled); err != nil {
		return taskruns.Record{}, err
	}
	return reconciled, nil
}

func (s *Server) latestTaskRunAssistantMessage(sessionID string) string {
	entries, err := s.eventJournal.ReadAfter(sessionID, 0, 200)
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		role, _ := entries[i].Message["role"].(string)
		if role != "assistant" {
			continue
		}
		if text, _ := entries[i].Message["text"].(string); strings.TrimSpace(text) != "" {
			return text
		}
		if message, ok := entries[i].Message["message"].(map[string]any); ok {
			if text, _ := message["text"].(string); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func (s *Server) appendTaskRunSystemEvent(run taskruns.Record, text string) error {
	if s.eventJournal == nil || run.SessionID == "" {
		return nil
	}
	_, err := s.eventJournal.Append(run.SessionID, eventjournal.Message{
		"type":    "taskrun_event",
		"role":    "system",
		"text":    text,
		"taskRun": run.RunID,
	}, eventjournal.Metadata{RunID: run.RunID, ClientMessageID: "taskrun-" + run.RunID + "-system-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)})
	return err
}

func (s *Server) mirrorTaskRunRunnerEvent(sessionID string, entry *eventjournal.Entry) error {
	if s == nil || s.taskRunStore == nil || entry == nil || !strings.HasPrefix(sessionID, "taskrun:") {
		return nil
	}
	eventType := strings.TrimSpace(stringValue(entry.Message["type"]))
	if eventType != "runner_checkpoint" && eventType != "runner_finished" {
		return nil
	}
	runID := strings.TrimSpace(entry.RunID)
	if runID == "" {
		runID = strings.TrimPrefix(sessionID, "taskrun:")
	}
	run, found, err := s.taskRunStore.Get(runID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(run.SessionID) != sessionID {
		return errors.New("TaskRun runner event has no matching durable run")
	}
	now := time.Now().UTC().UnixMilli()
	trace := taskruns.TraceEvent{
		At:      now,
		Event:   eventType,
		StepID:  taskRunRunnerTraceStepID(run, sessionID),
		Message: compactText(firstNonEmpty(stringValue(entry.Message["text"]), "Runner status: "+stringValue(entry.Message["status"])), 500),
		Metadata: map[string]any{
			"sessionId": sessionID,
			"runnerId":  stringValue(entry.Message["runnerId"]),
			"status":    stringValue(entry.Message["status"]),
			"eventId":   entry.EventID,
		},
	}
	for _, key := range []string{"afterEventId", "toolName", "toolCallId", "toolPhase", "resumeCacheKey", "runnerAttempt", "leaseReclaimed", "previousRunnerId"} {
		if value, ok := entry.Message[key]; ok && value != nil {
			trace.Metadata[key] = value
		}
	}
	if calls, ok := entry.Message["modelToolCalls"].([]any); ok {
		trace.Metadata["modelToolCalls"] = len(calls)
	}
	_, err = s.taskRunStore.Update(run.RunID, func(record taskruns.Record) (taskruns.Record, error) {
		for _, existing := range record.ExecutionTrace {
			if existing.Event != trace.Event || existing.Metadata == nil {
				continue
			}
			if numberValue(existing.Metadata["eventId"]) == entry.EventID {
				return record, nil
			}
		}
		if trace.StepID == "" {
			trace.StepID = taskRunRunnerTraceStepID(record, sessionID)
		}
		record.UpdatedAt = now
		record.ExecutionTrace = append(record.ExecutionTrace, trace)
		record.History = append(record.History, taskruns.HistoryEvent{
			At:      now,
			Event:   eventType,
			Message: trace.Message,
		})
		return record, nil
	})
	if err != nil {
		return err
	}
	return s.appendDelegatedAgentProgressEvent(run, trace, entry)
}

func (s *Server) appendDelegatedAgentProgressEvent(run taskruns.Record, trace taskruns.TraceEvent, childEntry *eventjournal.Entry) error {
	if s == nil || s.eventJournal == nil || childEntry == nil {
		return nil
	}
	parentSessionID := strings.TrimSpace(stringValue(run.Orchestration["parentSessionId"]))
	if parentSessionID == "" || parentSessionID == run.SessionID {
		return nil
	}
	message := eventjournal.Message{
		"type":           "delegated_agent_progress",
		"role":           "system",
		"text":           trace.Message,
		"taskRun":        run.RunID,
		"event":          trace.Event,
		"stepId":         trace.StepID,
		"childSessionId": run.SessionID,
		"childEventId":   childEntry.EventID,
		"runnerId":       stringValue(childEntry.Message["runnerId"]),
		"status":         stringValue(childEntry.Message["status"]),
	}
	for _, key := range []string{"toolName", "toolCallId", "toolPhase", "resumeCacheKey"} {
		if value, ok := trace.Metadata[key]; ok && value != nil {
			message[key] = value
		}
	}
	clientMessageID := fmt.Sprintf("taskrun-%s-parent-progress-%d", run.RunID, childEntry.EventID)
	entry, created, err := s.eventJournal.AppendIdempotent(parentSessionID, message, eventjournal.Metadata{
		RunID:           run.RunID,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return err
	}
	if created && entry != nil {
		s.publishSessionEntry(entry)
	}
	return nil
}

func taskRunRunnerTraceStepID(run taskruns.Record, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	for _, child := range run.ActiveChildren {
		if sessionID != "" && child.TaskID != sessionID {
			continue
		}
		if strings.TrimSpace(child.StepID) != "" {
			return child.StepID
		}
	}
	for _, child := range run.ActiveChildren {
		if strings.TrimSpace(child.StepID) != "" {
			return child.StepID
		}
	}
	for _, step := range run.Steps {
		if sessionID != "" && step.ChildTaskID == sessionID && strings.TrimSpace(step.ID) != "" {
			return step.ID
		}
	}
	for _, step := range run.Steps {
		if step.Status == "running" && strings.TrimSpace(step.ID) != "" {
			return step.ID
		}
	}
	return ""
}
