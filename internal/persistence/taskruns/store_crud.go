package taskruns

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func NewStore(root string) *Store {
	return &Store{root: filepath.Join(root, "task_runs"), now: time.Now}
}

func (s *Store) Create(input Input) (Record, error) {
	now := s.nowMillis()
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = generateRunID()
	}
	objective := strings.TrimSpace(input.Objective)
	if objective == "" {
		objective = strings.TrimSpace(input.Message)
	}
	if objective == "" {
		return Record{}, errors.New("TaskRun objective or message is required")
	}
	action := normalizeAction(input.Action)
	status := "running"
	event := "started"
	nextActions := []string{"TaskRun has been queued for the Go session runner."}
	if action == "plan" {
		status = "planning"
		event = "planned"
		nextActions = []string{"Review the generated task graph, then call TaskRun action=start with the objective to execute it."}
	}
	graph := input.TaskGraph
	if graph == nil {
		return Record{}, errors.New("TaskRun.task_graph is required; generate_plan is the planning authority")
	}
	steps, err := normalizeSteps(graph.Steps, now)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		SchemaVersion:   SchemaVersion,
		RunID:           runID,
		GoalLedgerRunID: runID,
		Orchestration:   copyMap(input.Orchestration),
		Status:          status,
		Objective:       objective,
		SuccessCriteria: compactStrings(input.SuccessCriteria),
		Constraints:     compactStrings(input.Constraints),
		ExecutorScope:   executorScope(input.ExecutorScope),
		CreatedAt:       now,
		UpdatedAt:       now,
		OutputPath:      s.outputPath(runID),
		SessionID:       SessionID(runID),
		Steps:           steps,
		ActiveChildren:  []ActiveChild{},
		Artifacts:       artifactsFromGraph(graph),
		EvidenceIndex:   []Evidence{},
		Blockers:        []Blocker{},
		NextActions:     nextActions,
		Acceptance:      initialAcceptance(input.SuccessCriteria, steps, now),
		Quality:         calculateQuality(steps, nil, nil, now),
		Completion:      initialCompletion(status, now),
		SelfCheck:       initialSelfCheck(now),
		RepairPolicy:    initialRepairPolicy(now),
		ExecutionTrace: []TraceEvent{{
			At:      now,
			Event:   event,
			Message: "TaskRun created by the Go runtime.",
		}},
		CapabilityGaps: []CapabilityGap{},
		History:        []HistoryEvent{{At: now, Event: event}},
	}
	if len(record.Orchestration) > 0 {
		record.Orchestration["runId"] = record.RunID
		record.Orchestration["sessionId"] = record.SessionID
	}
	if status == "running" {
		record = markQueued(record, now)
	}
	return s.Write(record)
}

func (s *Store) Get(runID string) (Record, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Record{}, false, errors.New("TaskRun.run_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readLocked(runID)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	return record, err == nil, err
}

func (s *Store) Write(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(record)
}

func (s *Store) Update(runID string, update func(Record) (Record, error)) (Record, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Record{}, errors.New("TaskRun.run_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.readLocked(runID)
	if err != nil {
		return Record{}, err
	}
	next, err := update(current)
	if err != nil {
		return Record{}, err
	}
	return s.writeLocked(next)
}

func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	runs := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			continue
		}
		var record Record
		if json.Unmarshal(raw, &record) == nil && record.RunID != "" {
			runs = append(runs, record)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].UpdatedAt == runs[j].UpdatedAt {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].UpdatedAt > runs[j].UpdatedAt
	})
	return runs, nil
}

func (s *Store) MarkQueued(runID string) (Record, error) {
	now := s.nowMillis()
	return s.Update(runID, func(record Record) (Record, error) {
		if isTerminal(record.Status) {
			return record, nil
		}
		record.Status = "running"
		record.UpdatedAt = now
		record = markQueued(record, now)
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "queued", Message: "TaskRun queued for the Go session runner."})
		record.History = append(record.History, HistoryEvent{At: now, Event: "queued"})
		return record, nil
	})
}

func (s *Store) Resume(runID string, message string) (Record, error) {
	now := s.nowMillis()
	return s.Update(runID, func(record Record) (Record, error) {
		if record.Status == "cancelled" {
			return Record{}, errors.New("cancelled TaskRun cannot be resumed")
		}
		record = releaseRecoverableExternalBlockers(record, now)
		record.Status = "running"
		record.UpdatedAt = now
		record.Completion = Completion{State: "in_progress", Verified: false, Confidence: "medium", Reason: "TaskRun resumed.", UpdatedAt: now}
		record.NextActions = []string{"TaskRun resumed and queued for the Go session runner."}
		record = prepareSelfCheckRepair(record, now, message)
		record = markQueued(record, now)
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "resumed", Message: compactText(message, 240)})
		record.History = append(record.History, HistoryEvent{At: now, Event: "resumed", Message: compactText(message, 240)})
		return record, nil
	})
}

func releaseRecoverableExternalBlockers(record Record, now int64) Record {
	blockedSteps := map[string]struct{}{}
	for index := range record.Steps {
		step := &record.Steps[index]
		if step.Status != "blocked" || step.Blocker == nil || !isRecoverableExternalBlocker(step.Blocker.Kind) {
			continue
		}
		blockedSteps[step.ID] = struct{}{}
		step.Status = "pending"
		step.Blocker = nil
		step.Error = ""
		step.ChildTaskID = ""
		step.OutputPath = ""
		step.StartedAt = 0
		step.CompletedAt = 0
		step.UpdatedAt = now
	}
	if len(blockedSteps) == 0 {
		return record
	}
	record.Blockers = filterPersistentExternalBlockers(record.Blockers, blockedSteps)
	record.CapabilityGaps = filterPersistentExternalCapabilityGaps(record.CapabilityGaps, blockedSteps)
	record.ActiveChildren = []ActiveChild{}
	return record
}

func filterPersistentExternalBlockers(blockers []Blocker, released map[string]struct{}) []Blocker {
	out := make([]Blocker, 0, len(blockers))
	for _, blocker := range blockers {
		if _, ok := released[blocker.StepID]; ok && isRecoverableExternalBlocker(blocker.Kind) {
			continue
		}
		out = append(out, blocker)
	}
	return out
}

func filterPersistentExternalCapabilityGaps(gaps []CapabilityGap, released map[string]struct{}) []CapabilityGap {
	out := make([]CapabilityGap, 0, len(gaps))
	for _, gap := range gaps {
		if _, ok := released[gap.StepID]; ok && isRecoverableExternalBlocker(gap.Kind) {
			continue
		}
		out = append(out, gap)
	}
	return out
}

func isRecoverableExternalBlocker(kind string) bool {
	switch kind {
	case "needsClient", "missingCapability", "needsUserAction":
		return true
	default:
		return false
	}
}

func (s *Store) Cancel(runID string) (Record, error) {
	now := s.nowMillis()
	return s.Update(runID, func(record Record) (Record, error) {
		return cancelTaskRunRecord(record, now, "TaskRun cancelled by request."), nil
	})
}

func cancelTaskRunRecord(record Record, now int64, reason string) Record {
	record.Status = "cancelled"
	record.UpdatedAt = now
	for i := range record.Steps {
		if record.Steps[i].Status == "running" || record.Steps[i].Status == "pending" || record.Steps[i].Status == "waiting_user" {
			record.Steps[i].Status = "cancelled"
			record.Steps[i].CompletedAt = now
			record.Steps[i].UpdatedAt = now
		}
	}
	record.ActiveChildren = []ActiveChild{}
	record.NextActions = []string{"No further action; TaskRun was cancelled."}
	record.Completion = Completion{State: "cancelled", Verified: false, Confidence: "high", Reason: reason, UpdatedAt: now}
	record.Quality = calculateQuality(record.Steps, record.Acceptance, record.Artifacts, now)
	record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "cancelled"})
	record.History = append(record.History, HistoryEvent{At: now, Event: "cancelled"})
	return record
}

func (s *Store) Verify(runID string) (Record, error) {
	now := s.nowMillis()
	return s.Update(runID, func(record Record) (Record, error) {
		allDone := allSteps(record.Steps, "completed")
		issues := classifyTaskRunIssues(record)
		selfCheckStatus := finalSelfCheckStatus(record, issues, allDone)
		recommendations := recommendationsForIssues(issues)
		status := completionStateForSelfCheck(record, selfCheckStatus, allDone)
		verified := selfCheckStatus == "passed" || selfCheckStatus == "passed_with_warnings"
		confidence := completionConfidence(selfCheckStatus, verified)
		reason := completionReason(selfCheckStatus, allDone, issues)
		if allDone && (selfCheckStatus == "passed" || selfCheckStatus == "passed_with_warnings") {
			status = "verified"
			if selfCheckStatus == "passed_with_warnings" {
				status = "verified_with_warnings"
			}
			verified = true
			if selfCheckStatus == "passed" {
				confidence = "high"
				reason = "All TaskRun steps are completed and acceptance checks were marked passed."
			} else {
				confidence = "medium"
				reason = "All TaskRun steps are completed and acceptance checks were marked passed with warnings."
			}
			for i := range record.Acceptance {
				record.Acceptance[i].Status = "passed"
				record.Acceptance[i].UpdatedAt = now
				if record.Acceptance[i].EvidencePath == "" {
					record.Acceptance[i].EvidencePath = record.OutputPath
				}
			}
		} else if record.Status == "running" {
			status = "in_progress"
			reason = "TaskRun is still running; verification is deferred."
		}
		record.UpdatedAt = now
		record.Completion = Completion{State: status, Verified: verified, Confidence: confidence, Reason: reason, UpdatedAt: now}
		record.SelfCheck = SelfCheck{
			Status:           selfCheckStatus,
			Rounds:           maxInt(record.SelfCheck.Rounds, 1),
			Issues:           issues,
			RepairsAttempted: record.SelfCheck.RepairsAttempted,
			RemainingIssues:  issues,
			Recommendations:  recommendations,
			StopReason:       stopReasonForSelfCheck(selfCheckStatus, record),
			LastCheckedAt:    now,
			Metrics:          selfCheckMetrics(record, issues),
		}
		record.RepairPolicy = evaluateRepairPolicy(record, issues, now)
		if len(recommendations) > 0 && !verified {
			record.NextActions = recommendations
		}
		record.Quality = calculateQuality(record.Steps, record.Acceptance, record.Artifacts, now)
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "verified", Message: reason}, TraceEvent{At: now, Event: "self_check_" + selfCheckStatus, Message: "TaskRun self-check " + selfCheckStatus + ".", Metadata: map[string]any{"issues": len(issues), "metrics": record.SelfCheck.Metrics}})
		record.History = append(record.History, HistoryEvent{At: now, Event: "verified", Message: reason})
		return record, nil
	})
}

func (s *Store) AutoRepair(runID string, message string) (Record, bool, error) {
	verified, err := s.Verify(runID)
	if err != nil {
		return Record{}, false, err
	}
	if !verified.RepairPolicy.Queueable {
		return verified, false, nil
	}
	repaired, err := s.Resume(runID, firstNonEmpty(message, "Auto repair queued by TaskRun repair policy."))
	if err != nil {
		return Record{}, false, err
	}
	return repaired, len(repaired.ActiveChildren) > 0, nil
}

func (s *Store) ReconcileWithRunner(runID string, runnerStatus string, assistantMessage string) (Record, error) {
	now := s.nowMillis()
	return s.Update(runID, func(record Record) (Record, error) {
		runnerStatus = strings.TrimSpace(runnerStatus)
		if runnerStatus == "" || isTerminal(record.Status) {
			return record, nil
		}
		switch runnerStatus {
		case "completed":
			stepIDs := activeStepIDs(record)
			if len(stepIDs) == 0 {
				stepIDs = runningStepIDs(record)
			}
			if len(stepIDs) == 0 && record.Status != "running" {
				return record, nil
			}
			record = completeTaskRunSteps(record, stepIDs, now)
			record.ActiveChildren = []ActiveChild{}
			if assistantMessage != "" {
				stepID := firstStepIDFromSet(stepIDs, record)
				record.Artifacts = append(record.Artifacts, Artifact{
					StepID:      stepID,
					Kind:        "task_output",
					Path:        record.OutputPath,
					Description: "Assistant output produced by the Go session runner.",
					Metadata:    map[string]any{"preview": compactText(assistantMessage, 500)},
				})
				record.EvidenceIndex = append(record.EvidenceIndex, Evidence{
					ID:         fmt.Sprintf("evidence-%d", now),
					StepID:     stepID,
					Kind:       "task_output",
					Path:       record.OutputPath,
					Summary:    compactText(assistantMessage, 500),
					ProducedAt: now,
				})
			}
			if allSteps(record.Steps, "completed") {
				record.Status = "completed"
				record.NextActions = []string{"Run TaskRun action=verify to mark acceptance evidence explicitly."}
				record.Completion = Completion{State: "pending_self_check", Verified: false, Confidence: "medium", Reason: "Runner completed all TaskRun steps; final verification is pending.", NextAction: "Call TaskRun action=verify.", UpdatedAt: now}
			} else {
				record.Status = "waiting_next_step"
				record.NextActions = []string{"TaskRun step completed; call TaskRun action=resume to queue the next runnable step."}
				record.Completion = Completion{State: "step_completed", Verified: false, Confidence: "medium", Reason: "Runner completed the current TaskRun step; more steps remain.", NextAction: "Call TaskRun action=resume.", UpdatedAt: now}
			}
		case "failed":
			record.Status = "failed"
			record.NextActions = []string{"Inspect execution_trace and runner session events, then resume after fixing the blocker."}
			failedStepID := firstActiveOrRunningStepID(record)
			record.ActiveChildren = []ActiveChild{}
			blocker := Blocker{Kind: "executorFailed", Message: "Go session runner reported failure.", StepID: failedStepID}
			record.Blockers = append(record.Blockers, blocker)
			for i := range record.Steps {
				if record.Steps[i].Status == "running" {
					record.Steps[i].Status = "failed"
					record.Steps[i].CompletedAt = now
					record.Steps[i].UpdatedAt = now
					record.Steps[i].Blocker = &blocker
				}
			}
			record.Completion = Completion{State: "completed_with_issues", Verified: false, Confidence: "medium", Reason: "Runner failed before producing verified output.", UpdatedAt: now}
		case "cancelled":
			record = cancelTaskRunRecord(record, now, "Go session runner was cancelled before completion.")
		case "running", "waiting", "blocked":
			if runnerStatus == "blocked" {
				record.Status = "blocked"
				record.Blockers = append(record.Blockers, Blocker{Kind: "executorFailed", Message: "Runner checkpoint is blocked.", StepID: firstStepID(record)})
			} else {
				record.Status = "running"
			}
			record.NextActions = []string{"Wait for the Go session runner or inspect the taskrun session event stream."}
		}
		record.UpdatedAt = now
		record.Quality = calculateQuality(record.Steps, record.Acceptance, record.Artifacts, now)
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "reconciled", Message: "Runner status: " + runnerStatus})
		record.History = append(record.History, HistoryEvent{At: now, Event: "reconciled", Message: runnerStatus})
		return record, nil
	})
}

func SessionID(runID string) string {
	return "taskrun:" + sanitizeRunID(runID)
}

func (s *Store) outputPath(runID string) string {
	return filepath.Join(s.root, sanitizeRunID(runID)+".md")
}

func (s *Store) jsonPath(runID string) string {
	return filepath.Join(s.root, sanitizeRunID(runID)+".json")
}

func (s *Store) writeLocked(record Record) (Record, error) {
	if record.RunID == "" {
		return Record{}, errors.New("TaskRun.run_id is required")
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = SchemaVersion
	}
	if record.OutputPath == "" {
		record.OutputPath = s.outputPath(record.RunID)
	}
	if record.SessionID == "" {
		record.SessionID = SessionID(record.RunID)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Record{}, err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return Record{}, err
	}
	tmp := s.jsonPath(record.RunID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return Record{}, err
	}
	if err := os.Rename(tmp, s.jsonPath(record.RunID)); err != nil {
		return Record{}, err
	}
	if err := os.WriteFile(record.OutputPath, []byte(formatMarkdown(record)), 0o600); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) readLocked(runID string) (Record, error) {
	raw, err := os.ReadFile(s.jsonPath(runID))
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) nowMillis() int64 {
	return s.now().UTC().UnixMilli()
}

func normalizeAction(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return "start"
	}
	return action
}
