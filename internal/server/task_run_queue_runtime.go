package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	sessionstore "synon-go/internal/persistence/sessions"
	taskruns "synon-go/internal/persistence/taskruns"
	"time"
)

func taskRunInput(input map[string]any) (taskruns.Input, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return taskruns.Input{}, err
	}
	var parsed taskruns.Input
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return taskruns.Input{}, err
	}
	return parsed, nil
}
func (s *Server) resolveDelegationParentSession(parentSessionID string) (sessionstore.Session, bool, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" || s == nil || s.sessionStore == nil {
		return sessionstore.Session{}, false, nil
	}
	parent, found, err := s.sessionStore.Get(parentSessionID)
	if err != nil || found {
		return parent, found, err
	}
	// Transcript-backed web conversations use a workspace Frame as the
	// authoritative parent identity and may not have a legacy JSON Session
	// projection. Project only the inherited execution boundary; do not write
	// a synthetic parent session to the legacy store.
	if s.workspaceStore == nil {
		return sessionstore.Session{}, false, nil
	}
	frameContext, frameFound, err := s.workspaceStore.GetFrameRealtimeContext(parentSessionID)
	if err != nil || !frameFound {
		return sessionstore.Session{}, false, err
	}
	project, projectFound, err := s.workspaceStore.GetProject(frameContext.Frame.ProjectID)
	if err != nil {
		return sessionstore.Session{}, false, err
	}
	if !projectFound {
		return sessionstore.Session{}, false, fmt.Errorf("delegation parent frame project %q not found", frameContext.Frame.ProjectID)
	}
	title := strings.TrimSpace(frameContext.Frame.Name)
	if title == "" {
		title = parentSessionID
	}
	workDir := strings.TrimSpace(project.Path)
	if workDir == "" {
		workDir = strings.TrimSpace(s.fileRoot)
	}
	return sessionstore.Session{
		ID: parentSessionID, Title: title, WorkDir: workDir,
		CreatedAt: frameContext.Frame.CreatedAt, UpdatedAt: frameContext.Frame.UpdatedAt,
		Project: &sessionstore.Project{ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: frameContext.Frame.UpdatedAt},
	}, true, nil
}

func (s *Server) queueTaskRunSession(run taskruns.Record, message string) (taskruns.Record, error) {
	if s.sessionStore == nil || s.eventJournal == nil {
		return taskruns.Record{}, errors.New("session store is not configured")
	}
	now := time.Now().UTC()
	childSession := sessionstore.Session{
		ID:            run.SessionID,
		Title:         run.Objective,
		WorkDir:       s.fileRoot,
		CreatedAt:     now,
		UpdatedAt:     now,
		Orchestration: copyMapAny(run.Orchestration),
	}
	if parentSessionID := strings.TrimSpace(stringValue(run.Orchestration["parentSessionId"])); parentSessionID != "" {
		parent, found, err := s.resolveDelegationParentSession(parentSessionID)
		if err != nil {
			return taskruns.Record{}, err
		}
		if !found {
			return taskruns.Record{}, fmt.Errorf("parent Agent session %q does not exist", parentSessionID)
		}
		childSession.WorkDir = parent.WorkDir
		if parent.Project != nil {
			project := *parent.Project
			childSession.Project = &project
		}
	}
	if err := s.sessionStore.Upsert(childSession); err != nil {
		return taskruns.Record{}, err
	}
	prompt := buildTaskRunSessionPrompt(run, message)
	_, err := s.appendSessionToolEvent(map[string]any{
		"sessionId":       run.SessionID,
		"role":            "user",
		"runId":           run.RunID,
		"clientMessageId": "taskrun-" + run.RunID + "-queue-" + strconv.FormatInt(now.UnixNano(), 10),
		"message": map[string]any{
			"type":          "message",
			"text":          prompt,
			"taskRun":       run.RunID,
			"runnerRuntime": taskRunRunnerRuntimeMetadata(run),
		},
	})
	if err != nil {
		return taskruns.Record{}, err
	}
	queued, err := s.taskRunStore.MarkQueued(run.RunID)
	if err != nil {
		return taskruns.Record{}, err
	}
	if err := s.syncTaskRunGoalLedger(queued); err != nil {
		return taskruns.Record{}, err
	}
	return queued, nil
}

func (s *Server) advanceTaskRun(runID string, message string) (taskruns.Record, error) {
	run, err := s.reconcileTaskRun(runID)
	if err != nil {
		return taskruns.Record{}, err
	}
	switch run.Status {
	case "waiting_next_step", "blocked":
		resumed, err := s.taskRunStore.Resume(run.RunID, message)
		if err != nil {
			return taskruns.Record{}, err
		}
		return s.dispatchTaskRunExternalOrQueue(resumed, firstNonEmpty(message, "TaskRun advanced to the next runnable step."))
	case "running":
		if len(run.ActiveChildren) > 0 {
			return run, nil
		}
		return s.dispatchTaskRunExternalOrQueue(run, firstNonEmpty(message, "TaskRun advanced and re-queued."))
	default:
		return run, nil
	}
}

func (s *Server) monitorTaskRuns(runID string) (map[string]any, error) {
	if s == nil || s.taskRunStore == nil {
		return nil, errors.New("TaskRun store is not configured")
	}
	candidates, err := s.taskRunMonitorCandidates(runID)
	if err != nil {
		return nil, err
	}
	runs := make([]taskruns.Record, 0, len(candidates))
	scanned := 0
	advancedCount := 0
	selfChecked := 0
	waitingUser := 0
	skipped := 0
	for _, before := range candidates {
		scanned++
		if taskRunMonitorShouldSkip(before) {
			skipped++
			runs = append(runs, before)
			continue
		}
		reconciled, err := s.reconcileTaskRun(before.RunID)
		if err != nil {
			return nil, err
		}
		current := reconciled
		if taskRunMonitorCanAdvance(current) {
			advanced, err := s.advanceTaskRun(current.RunID, "TaskRun monitor advanced the next runnable step.")
			if err != nil {
				return nil, err
			}
			if countNewTaskRunActiveChildren(current, advanced) > 0 {
				advancedCount += countNewTaskRunActiveChildren(current, advanced)
			}
			current = advanced
		}
		if current.Status == "waiting_user" {
			waitingUser++
		}
		if taskRunMonitorCheckable(current) {
			verified, err := s.taskRunStore.Verify(current.RunID)
			if err != nil {
				return nil, err
			}
			if err := s.syncTaskRunGoalLedger(verified); err != nil {
				return nil, err
			}
			current = verified
			selfChecked++
		}
		runs = append(runs, current)
	}
	return map[string]any{
		"scanned":      scanned,
		"advanced":     advancedCount,
		"self_checked": selfChecked,
		"waiting_user": waitingUser,
		"skipped":      skipped,
		"runs":         runs,
	}, nil
}

func (s *Server) taskRunMonitorCandidates(runID string) ([]taskruns.Record, error) {
	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, found, err := s.taskRunStore.Get(runID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("TaskRun not found: %s", runID)
		}
		return []taskruns.Record{run}, nil
	}
	runs, err := s.taskRunStore.List()
	if err != nil {
		return nil, err
	}
	candidates := make([]taskruns.Record, 0, len(runs))
	for _, run := range runs {
		if run.Status == "planning" {
			continue
		}
		if taskRunMonitorShouldSkip(run) {
			continue
		}
		candidates = append(candidates, run)
	}
	return candidates, nil
}

func taskRunMonitorShouldSkip(run taskruns.Record) bool {
	if run.Status == "cancelled" || run.Status == "failed" {
		return true
	}
	return run.Status == "completed" && run.Completion.State != "pending_self_check"
}

func taskRunMonitorCanAdvance(run taskruns.Record) bool {
	if run.Status == "waiting_next_step" || run.Status == "blocked" {
		return true
	}
	return run.Status == "running" && len(run.ActiveChildren) == 0
}

func taskRunMonitorCheckable(run taskruns.Record) bool {
	if len(run.ActiveChildren) > 0 || len(run.Steps) == 0 {
		return false
	}
	for _, step := range run.Steps {
		if step.Status != "completed" {
			return false
		}
	}
	return run.SelfCheck.Status == "skipped" || run.Completion.State == "pending_self_check"
}

func countNewTaskRunActiveChildren(before taskruns.Record, after taskruns.Record) int {
	seen := map[string]struct{}{}
	for _, child := range before.ActiveChildren {
		seen[child.TaskID] = struct{}{}
	}
	count := 0
	for _, child := range after.ActiveChildren {
		if _, ok := seen[child.TaskID]; !ok {
			count++
		}
	}
	return count
}

func (s *Server) autoAdvanceTaskRunSession(sessionID string, message string) (bool, error) {
	if s == nil || s.taskRunStore == nil || !strings.HasPrefix(sessionID, "taskrun:") {
		return false, nil
	}
	run, found, err := s.taskRunBySessionID(sessionID)
	if err != nil || !found {
		return false, err
	}
	advanced, err := s.advanceTaskRun(run.RunID, message)
	if err != nil {
		return false, err
	}
	if advanced.Status == "running" && len(advanced.ActiveChildren) > 0 {
		return true, nil
	}
	if advanced.Status == "completed" || advanced.Completion.State == "pending_self_check" {
		_, queued, err := s.autoRepairTaskRun(advanced.RunID, "TaskRun auto-repair evaluated after runner completion.")
		return queued, err
	}
	return false, nil
}

func (s *Server) taskRunBySessionID(sessionID string) (taskruns.Record, bool, error) {
	runs, err := s.taskRunStore.List()
	if err != nil {
		return taskruns.Record{}, false, err
	}
	for _, run := range runs {
		if run.SessionID == sessionID {
			return run, true, nil
		}
	}
	return taskruns.Record{}, false, nil
}
