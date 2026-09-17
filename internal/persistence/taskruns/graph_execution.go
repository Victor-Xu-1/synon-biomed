package taskruns

import (
	"errors"
	"fmt"
	"strings"
)

func normalizeSteps(inputs []StepInput, now int64) ([]Step, error) {
	if len(inputs) == 0 {
		return nil, errors.New("TaskRun.task_graph.steps must contain at least one step")
	}
	seen := map[string]struct{}{}
	steps := make([]Step, 0, len(inputs))
	for index, input := range inputs {
		id := sanitizeStepID(input.ID)
		if id == "" {
			id = fmt.Sprintf("step-%d", index+1)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate TaskRun step id: %s", id)
		}
		seen[id] = struct{}{}
		title := strings.TrimSpace(input.Title)
		if title == "" {
			title = id
		}
		description := strings.TrimSpace(input.Description)
		if description == "" {
			description = title
		}
		executor := input.Executor
		if len(executor) == 0 {
			executor = map[string]any{"kind": "agent"}
		}
		kind, _ := executor["kind"].(string)
		if !validExecutorKind(kind) {
			return nil, fmt.Errorf("unsupported TaskRun executor kind: %s", kind)
		}
		maxRecoveries := input.MaxRecoveries
		if maxRecoveries <= 0 {
			maxRecoveries = 3
		}
		steps = append(steps, Step{
			ID:              id,
			Title:           title,
			Description:     description,
			Intent:          firstNonEmpty(input.Intent, id),
			ExpectedOutput:  firstNonEmpty(input.ExpectedOutput, "Durable evidence for "+title+"."),
			AcceptanceCheck: firstNonEmpty(input.AcceptanceCheck, "Step output satisfies its expected output."),
			RiskLevel:       firstNonEmpty(input.RiskLevel, "read"),
			MaxRecoveries:   maxRecoveries,
			DependsOn:       compactStrings(input.DependsOn),
			Executor:        executor,
			Status:          "pending",
			UpdatedAt:       now,
		})
	}
	return steps, nil
}

func markQueued(record Record, now int64) Record {
	index := nextRunnableStepIndex(record)
	record.ActiveChildren = []ActiveChild{}
	if index >= 0 {
		record.Status = "running"
		if record.Steps[index].Status == "failed" {
			record.Steps[index].RecoveryCount++
			record.Steps[index].Blocker = nil
			record.Steps[index].Error = ""
		}
		record.Steps[index].Status = "running"
		if record.Steps[index].StartedAt == 0 {
			record.Steps[index].StartedAt = now
		}
		record.Steps[index].UpdatedAt = now
		record.Steps[index].ChildTaskID = record.SessionID
		record.Steps[index].OutputPath = record.OutputPath
		record.ActiveChildren = []ActiveChild{{
			TaskID:     record.SessionID,
			StepID:     record.Steps[index].ID,
			Executor:   "agent",
			OutputPath: record.OutputPath,
		}}
		record.Completion = Completion{State: "in_progress", Verified: false, Confidence: "medium", Reason: "TaskRun is queued for the Go session runner.", NextAction: "Run the session runner or poll TaskRun status.", UpdatedAt: now}
	} else if allSteps(record.Steps, "completed") {
		record.Status = "completed"
		record.Completion = Completion{State: "pending_self_check", Verified: false, Confidence: "medium", Reason: "TaskRun has no remaining runnable steps; final verification is pending.", NextAction: "Call TaskRun action=verify.", UpdatedAt: now}
	} else {
		record.Status = "blocked"
		record.Completion = Completion{State: "needs_attention", Verified: false, Confidence: "low", Reason: "TaskRun has remaining steps but none are runnable.", NextAction: "Inspect dependencies or blockers.", UpdatedAt: now}
	}
	record.Quality = calculateQuality(record.Steps, record.Acceptance, record.Artifacts, now)
	return record
}

func nextRunnableStepIndex(record Record) int {
	for index, step := range record.Steps {
		if step.Status == "running" {
			return index
		}
	}
	for index, step := range record.Steps {
		if step.Status == "failed" && step.RecoveryCount < step.MaxRecoveries && dependenciesCompleted(record.Steps, step.DependsOn) {
			return index
		}
	}
	for index, step := range record.Steps {
		if step.Status == "pending" && dependenciesCompleted(record.Steps, step.DependsOn) {
			return index
		}
	}
	return -1
}

func dependenciesCompleted(steps []Step, dependsOn []string) bool {
	for _, dependency := range compactStrings(dependsOn) {
		found := false
		for _, step := range steps {
			if step.ID != dependency {
				continue
			}
			found = true
			if step.Status != "completed" {
				return false
			}
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func activeStepIDs(record Record) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, child := range record.ActiveChildren {
		if child.StepID != "" {
			ids[child.StepID] = struct{}{}
		}
	}
	return ids
}

func runningStepIDs(record Record) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, step := range record.Steps {
		if step.Status == "running" {
			ids[step.ID] = struct{}{}
		}
	}
	return ids
}

func firstStepIDFromSet(ids map[string]struct{}, record Record) string {
	for _, step := range record.Steps {
		if _, ok := ids[step.ID]; ok {
			return step.ID
		}
	}
	return firstStepID(record)
}

func firstActiveOrRunningStepID(record Record) string {
	for _, child := range record.ActiveChildren {
		if strings.TrimSpace(child.StepID) != "" {
			return child.StepID
		}
	}
	for _, step := range record.Steps {
		if step.Status == "running" {
			return step.ID
		}
	}
	return firstStepID(record)
}

func completeTaskRunSteps(record Record, ids map[string]struct{}, now int64) Record {
	for i := range record.Steps {
		if _, ok := ids[record.Steps[i].ID]; !ok {
			continue
		}
		record.Steps[i].Status = "completed"
		record.Steps[i].CompletedAt = now
		record.Steps[i].UpdatedAt = now
		record.Steps[i].OutputPath = record.OutputPath
		record.Steps[i].Blocker = nil
		record.Steps[i].Error = ""
	}
	return record
}

func prepareSelfCheckRepair(record Record, now int64, message string) Record {
	if record.Completion.Verified || len(record.SelfCheck.RemainingIssues) == 0 {
		return record
	}
	policyRecord := record
	policyRecord.Status = "completed"
	policyRecord.ActiveChildren = nil
	record.RepairPolicy = evaluateRepairPolicy(policyRecord, record.SelfCheck.RemainingIssues, now)
	if !record.RepairPolicy.Queueable {
		record.NextActions = []string{record.RepairPolicy.Reason}
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "self_check_repair_blocked", Message: record.RepairPolicy.Reason})
		return record
	}
	queuedRepair := false
	for _, issue := range record.SelfCheck.RemainingIssues {
		if !issue.Repairable {
			continue
		}
		if issue.StepID != "" {
			for i := range record.Steps {
				if record.Steps[i].ID != issue.StepID {
					continue
				}
				if record.Steps[i].RecoveryCount >= record.Steps[i].MaxRecoveries {
					break
				}
				record.Steps[i].Status = "pending"
				record.Steps[i].RecoveryCount++
				record.Steps[i].Blocker = nil
				record.Steps[i].Error = ""
				record.Steps[i].UpdatedAt = now
				queuedRepair = true
				break
			}
			continue
		}
		record.Steps = append(record.Steps, Step{
			ID:              uniqueStepID(record.Steps, fmt.Sprintf("repair-%d", record.SelfCheck.RepairsAttempted+1)),
			Title:           "Repair TaskRun self-check issue",
			Description:     repairStepDescription(issue, message),
			Intent:          "repair",
			ExpectedOutput:  "Durable evidence resolving the TaskRun self-check issue.",
			AcceptanceCheck: "The self-check issue is resolved and evidence is recorded.",
			RiskLevel:       "medium",
			MaxRecoveries:   1,
			Executor:        map[string]any{"kind": "agent", "source": "self_check_repair", "issue_kind": issue.Kind},
			Status:          "pending",
			UpdatedAt:       now,
		})
		record.Acceptance = append(record.Acceptance, Acceptance{
			ID:          "step-" + record.Steps[len(record.Steps)-1].ID,
			StepID:      record.Steps[len(record.Steps)-1].ID,
			Description: "The self-check repair step resolves: " + issue.Message,
			Status:      "pending",
			UpdatedAt:   now,
		})
		queuedRepair = true
	}
	if queuedRepair {
		record.SelfCheck.RepairsAttempted++
		record.SelfCheck.StopReason = "repair_queued"
		record.RepairPolicy.Status = "queued"
		record.RepairPolicy.Queueable = false
		record.RepairPolicy.RoundsAttempted = record.SelfCheck.RepairsAttempted
		record.RepairPolicy.Reason = "Repair work was queued for the next runner turn."
		record.RepairPolicy.LastEvaluatedAt = now
		record.NextActions = []string{"TaskRun self-check repair was queued for the next runner turn."}
		record.ExecutionTrace = append(record.ExecutionTrace, TraceEvent{At: now, Event: "self_check_repair_queued", Message: "Queued repair work from TaskRun self-check issues.", Metadata: map[string]any{"issueCount": len(record.SelfCheck.RemainingIssues), "round": record.SelfCheck.RepairsAttempted}})
	}
	return record
}

func repairStepDescription(issue Issue, message string) string {
	parts := []string{"Repair TaskRun self-check issue: " + issue.Message}
	if strings.TrimSpace(message) != "" {
		parts = append(parts, "Resume message: "+strings.TrimSpace(message))
	}
	return strings.Join(parts, "\n")
}

func uniqueStepID(steps []Step, base string) string {
	base = sanitizeStepID(base)
	if base == "" {
		base = "repair"
	}
	used := map[string]struct{}{}
	for _, step := range steps {
		used[step.ID] = struct{}{}
	}
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
}
