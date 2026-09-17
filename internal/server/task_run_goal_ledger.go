package server

import (
	"errors"
	"fmt"
	"strings"
	"time"

	taskruns "synon-go/internal/persistence/taskruns"
)

const goalRunLedgerRuntimeNamespace = "goal-runs"
const goalRunActiveRuntimeNamespace = "goal-runs-active"

func (s *Server) syncTaskRunGoalLedger(run taskruns.Record) error {
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(run.RunID) == "" || strings.TrimSpace(run.Objective) == "" {
		return nil
	}
	ledgerRunID := firstNonEmpty(run.GoalLedgerRunID, run.RunID)
	if taskRunGoalStatus(run.Status) != "active" {
		_, activeErr := s.runtimeStore.Delete(goalRunActiveRuntimeNamespace, taskRunGoalActiveKey(run))
		_, ledgerErr := s.runtimeStore.Delete(goalRunLedgerRuntimeNamespace, ledgerRunID)
		return errors.Join(activeErr, ledgerErr)
	}
	ledger, err := s.buildTaskRunGoalLedger(run, ledgerRunID)
	if err != nil {
		return err
	}
	if _, err := s.runtimeStore.Set(goalRunLedgerRuntimeNamespace, ledgerRunID, ledger); err != nil {
		return err
	}
	_, err = s.runtimeStore.Set(goalRunActiveRuntimeNamespace, taskRunGoalActiveKey(run), ledgerRunID)
	return err
}

func (s *Server) buildTaskRunGoalLedger(run taskruns.Record, ledgerRunID string) (map[string]any, error) {
	now := time.Now().UTC().UnixMilli()
	createdAt := run.CreatedAt
	turnsStarted := int64(0)
	recoveryCount := taskRunGoalRecoveryCount(run)
	if entry, ok, err := s.runtimeStore.Get(goalRunLedgerRuntimeNamespace, ledgerRunID); err != nil {
		return nil, err
	} else if ok {
		existing := objectMapValue(entry.Value)
		if existingCreated := numberValue(existing["createdAt"]); existingCreated > 0 {
			createdAt = existingCreated
		}
		turnsStarted = numberValue(existing["turnsStarted"])
		if existingRecovery := numberValue(existing["recoveryCount"]); existingRecovery > recoveryCount {
			recoveryCount = existingRecovery
		}
	}
	if createdAt == 0 {
		createdAt = now
	}
	subagents := map[string]any{}
	for _, step := range run.Steps {
		childID := strings.TrimSpace(step.ChildTaskID)
		if childID == "" {
			continue
		}
		subagents[childID] = map[string]any{
			"agentId":     childID,
			"role":        stringValue(step.Executor["kind"]),
			"description": step.Title,
			"status":      taskRunGoalSubagentStatus(step.Status),
			"outputFile":  step.OutputPath,
			"retryCount":  step.RecoveryCount,
			"updatedAt":   firstNonZero(step.UpdatedAt, run.UpdatedAt, now),
		}
	}
	acceptance := map[string]any{}
	for _, check := range run.Acceptance {
		acceptance[check.ID] = map[string]any{
			"id":           check.ID,
			"description":  check.Description,
			"status":       taskRunGoalAcceptanceStatus(check.Status),
			"evidencePath": check.EvidencePath,
			"details":      check.Details,
			"updatedAt":    firstNonZero(check.UpdatedAt, run.UpdatedAt, now),
		}
	}
	phase := taskRunGoalPhase(run.Status)
	checkpoints := []any{map[string]any{
		"at":         firstNonZero(run.UpdatedAt, now),
		"phase":      phase,
		"summary":    fmt.Sprintf("%s: %s", run.Status, run.Objective),
		"nextAction": firstString(run.NextActions),
	}}
	events := taskRunGoalEvents(run, now)
	quality := taskRunGoalQuality(acceptance, subagents, recoveryCount, now)
	return map[string]any{
		"schemaVersion": 1,
		"runId":         ledgerRunID,
		"goalId":        "taskrun:" + run.RunID,
		"goalText":      run.Objective,
		"scope":         "session",
		"scopeKey":      "taskrun:" + run.RunID,
		"scopeLabel":    "TaskRun " + run.RunID,
		"phase":         phase,
		"status":        taskRunGoalStatus(run.Status),
		"createdAt":     createdAt,
		"updatedAt":     firstNonZero(run.UpdatedAt, now),
		"turnsStarted":  turnsStarted,
		"recoveryCount": recoveryCount,
		"lastProgressAt": firstNonZero(
			taskRunGoalLastProgressAt(run),
			run.UpdatedAt,
			now,
		),
		"subagents":   subagents,
		"acceptance":  acceptance,
		"checkpoints": checkpoints,
		"quality":     quality,
		"events":      events,
	}, nil
}

func taskRunGoalActiveKey(run taskruns.Record) string {
	return "session:taskrun:" + run.RunID + ":taskrun:" + run.RunID
}

func taskRunGoalPhase(status string) string {
	switch status {
	case "planning":
		return "planning"
	case "verifying":
		return "verifying"
	case "blocked", "failed":
		return "blocked"
	case "completed":
		return "completed"
	default:
		return "executing"
	}
}

func taskRunGoalStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "blocked", "failed":
		return "blocked"
	case "cancelled":
		return "stopped"
	default:
		return "active"
	}
}

func taskRunGoalSubagentStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "killed"
	default:
		return "running"
	}
}

func taskRunGoalAcceptanceStatus(status string) string {
	switch status {
	case "passed", "failed", "partial":
		return status
	default:
		return "pending"
	}
}

func taskRunGoalEvents(run taskruns.Record, now int64) []any {
	events := []any{map[string]any{"at": firstNonZero(run.CreatedAt, now), "type": "started", "message": "started " + run.Objective}}
	for _, trace := range run.ExecutionTrace {
		eventType := "checkpoint"
		if strings.Contains(trace.Event, "recovery") || strings.Contains(trace.Event, "repair") {
			eventType = "recovery"
		}
		message := firstNonEmpty(trace.Message, trace.Event)
		events = append(events, map[string]any{"at": firstNonZero(trace.At, now), "type": eventType, "message": message})
	}
	if len(events) > 200 {
		events = events[len(events)-200:]
	}
	return events
}

func taskRunGoalRecoveryCount(run taskruns.Record) int64 {
	total := int64(run.SelfCheck.RepairsAttempted)
	for _, step := range run.Steps {
		total += int64(step.RecoveryCount)
	}
	return total
}

func taskRunGoalLastProgressAt(run taskruns.Record) int64 {
	for i := len(run.ExecutionTrace) - 1; i >= 0; i-- {
		if run.ExecutionTrace[i].At > 0 {
			return run.ExecutionTrace[i].At
		}
	}
	return 0
}

func taskRunGoalQuality(acceptance map[string]any, subagents map[string]any, recoveryCount int64, now int64) map[string]any {
	acceptancePassRate := 0.0
	if len(acceptance) > 0 {
		passed := 0
		for _, raw := range acceptance {
			if objectMapValue(raw)["status"] == "passed" {
				passed++
			}
		}
		acceptancePassRate = float64(passed) / float64(len(acceptance))
	}
	subagentCompletionRate := 0.0
	if len(subagents) > 0 {
		completed := 0
		for _, raw := range subagents {
			if objectMapValue(raw)["status"] == "completed" {
				completed++
			}
		}
		subagentCompletionRate = float64(completed) / float64(len(subagents))
	}
	recoveryPenalty := minInt(30, int(recoveryCount)*5)
	score := clampInt(int(40+acceptancePassRate*35+subagentCompletionRate*20)-recoveryPenalty, 0, 100)
	return map[string]any{
		"score":                  score,
		"acceptancePassRate":     acceptancePassRate,
		"subagentCompletionRate": subagentCompletionRate,
		"recoveryPenalty":        recoveryPenalty,
		"updatedAt":              now,
	}
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func clampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
