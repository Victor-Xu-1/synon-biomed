package taskruns

import (
	"fmt"
)

func initialCompletion(status string, now int64) Completion {
	if status == "planning" {
		return Completion{State: "not_started", Verified: false, Confidence: "low", Reason: "TaskRun is planned but not queued.", NextAction: "Call TaskRun action=start with the objective.", UpdatedAt: now}
	}
	return Completion{State: "in_progress", Verified: false, Confidence: "medium", Reason: "TaskRun was created for execution.", UpdatedAt: now}
}

func initialSelfCheck(now int64) SelfCheck {
	return SelfCheck{
		Status:           "skipped",
		Rounds:           0,
		Issues:           []Issue{},
		RepairsAttempted: 0,
		RemainingIssues:  []Issue{},
		Recommendations:  []string{},
		StopReason:       "not_applicable",
		LastCheckedAt:    now,
	}
}

func initialRepairPolicy(now int64) RepairPolicy {
	return RepairPolicy{
		Status:          "idle",
		Mode:            "bounded_auto",
		MaxRounds:       3,
		RoundsAttempted: 0,
		Queueable:       false,
		Blocked:         false,
		Reason:          "No repair policy has been evaluated.",
		Decisions:       []RepairDecision{},
		LastEvaluatedAt: now,
	}
}

func initialAcceptance(criteria []string, steps []Step, now int64) []Acceptance {
	out := []Acceptance{}
	for index, criterion := range compactStrings(criteria) {
		out = append(out, Acceptance{ID: fmt.Sprintf("criteria-%d", index+1), Description: criterion, Status: "pending", UpdatedAt: now})
	}
	for _, step := range steps {
		out = append(out, Acceptance{ID: "step-" + step.ID, StepID: step.ID, Description: step.AcceptanceCheck, Status: "pending", UpdatedAt: now})
	}
	return out
}

func classifyTaskRunIssues(record Record) []Issue {
	issues := []Issue{}
	blockerSteps := map[string]struct{}{}
	for _, blocker := range record.Blockers {
		if blocker.StepID != "" {
			blockerSteps[blocker.StepID] = struct{}{}
		}
		issues = append(issues, issueFromBlocker(blocker))
	}
	for _, step := range record.Steps {
		if step.Status == "failed" {
			if _, ok := blockerSteps[step.ID]; ok {
				continue
			}
			issues = append(issues, Issue{
				Severity:   "critical",
				Kind:       "failedStep",
				Message:    firstNonEmpty(step.Error, "Step "+step.ID+" failed."),
				StepID:     step.ID,
				Repairable: step.RecoveryCount < step.MaxRecoveries,
			})
		}
		if step.Status == "pending" && record.Status != "planning" {
			issues = append(issues, Issue{
				Severity:   "major",
				Kind:       "pendingStep",
				Message:    "Step " + step.ID + " has not run yet.",
				StepID:     step.ID,
				Repairable: true,
			})
		}
	}
	if record.Status == "failed" && !anyIssueKind(issues, "failedStep") {
		issues = append(issues, Issue{
			Severity:   "critical",
			Kind:       "runFailed",
			Message:    "TaskRun reached failed status without a completed verification path.",
			Repairable: len(record.Blockers) == 0,
		})
	}
	if record.Status == "completed" && len(record.EvidenceIndex) == 0 && len(record.Artifacts) == 0 {
		issues = append(issues, Issue{
			Severity:   "major",
			Kind:       "insufficientEvidence",
			Message:    "TaskRun completed without durable artifacts or evidence_index entries.",
			Repairable: true,
		})
	}
	if record.Status == "completed" && anyRecoveredStep(record.Steps) {
		issues = append(issues, Issue{
			Severity:   "minor",
			Kind:       "recovered",
			Message:    "TaskRun completed after one or more recovery attempts.",
			Repairable: false,
		})
	}
	return dedupeIssues(issues)
}

func issueFromBlocker(blocker Blocker) Issue {
	severity := "critical"
	repairable := true
	switch blocker.Kind {
	case "needsClient", "missingCapability", "needsUserAction":
		severity = "external"
		repairable = false
	case "recoveryExhausted":
		severity = "critical"
		repairable = false
	case "noProgress", "failedVerification":
		severity = "major"
	case "insufficientEvidence", "dependencyFailed", "executorFailed":
		severity = "critical"
	}
	return Issue{
		Severity:   severity,
		Kind:       firstNonEmpty(blocker.Kind, "blocker"),
		Message:    firstNonEmpty(blocker.Message, "TaskRun blocker."),
		StepID:     blocker.StepID,
		Repairable: repairable,
	}
}

func finalSelfCheckStatus(record Record, issues []Issue, allDone bool) string {
	if record.Status == "planning" || record.Status == "cancelled" {
		return "skipped"
	}
	if hasActiveOrPendingWork(record) {
		return "skipped"
	}
	if len(issues) == 0 {
		return "passed"
	}
	if anyIssueSeverity(issues, "external") {
		return "waiting_user"
	}
	if anyIssueSeverity(issues, "critical") {
		return "needs_attention"
	}
	if anyIssueSeverity(issues, "major") {
		return "completed_with_issues"
	}
	return "passed_with_warnings"
}

func completionStateForSelfCheck(record Record, selfCheckStatus string, allDone bool) string {
	if hasActiveOrPendingWork(record) {
		return "in_progress"
	}
	switch selfCheckStatus {
	case "passed":
		return "verified"
	case "passed_with_warnings":
		return "verified_with_warnings"
	case "waiting_user":
		return "waiting_user"
	case "completed_with_issues":
		return "completed_with_issues"
	case "needs_attention":
		return "needs_attention"
	default:
		return "needs_attention"
	}
}

func completionConfidence(selfCheckStatus string, verified bool) string {
	if verified && selfCheckStatus == "passed" {
		return "high"
	}
	if verified {
		return "medium"
	}
	if selfCheckStatus == "needs_attention" {
		return "low"
	}
	return "medium"
}

func completionReason(selfCheckStatus string, allDone bool, issues []Issue) string {
	if !allDone && selfCheckStatus == "skipped" {
		return "TaskRun still has incomplete, pending, or failed steps."
	}
	switch selfCheckStatus {
	case "passed":
		return "TaskRun self-check passed."
	case "passed_with_warnings":
		return "TaskRun self-check passed with warnings."
	case "waiting_user":
		return "TaskRun self-check is waiting on an external dependency or user action."
	case "completed_with_issues":
		return "TaskRun completed but self-check found repairable issues."
	case "needs_attention":
		return "TaskRun self-check found issues that need attention."
	default:
		return fmt.Sprintf("TaskRun self-check found %d issue(s).", len(issues))
	}
}

func stopReasonForSelfCheck(status string, record Record) string {
	switch status {
	case "passed", "passed_with_warnings":
		return "passed"
	case "waiting_user":
		return "external_dependency"
	case "skipped":
		if record.Status == "running" {
			return "in_progress"
		}
		return "not_applicable"
	default:
		return "manual_action_required"
	}
}

func recommendationsForIssues(issues []Issue) []string {
	if len(issues) == 0 {
		return []string{"Report the completed TaskRun artifacts to the user."}
	}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		prefix := ""
		if issue.StepID != "" {
			prefix = issue.StepID + ": "
		}
		if issue.Severity == "external" {
			out = append(out, prefix+issue.Message)
			continue
		}
		if issue.Repairable {
			out = append(out, prefix+"Resume or repair this TaskRun issue: "+issue.Message)
			continue
		}
		out = append(out, prefix+issue.Message)
	}
	return out
}

func evaluateRepairPolicy(record Record, issues []Issue, now int64) RepairPolicy {
	maxRounds := record.RepairPolicy.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}
	policy := RepairPolicy{
		Status:          "not_needed",
		Mode:            firstNonEmpty(record.RepairPolicy.Mode, "bounded_auto"),
		MaxRounds:       maxRounds,
		RoundsAttempted: record.SelfCheck.RepairsAttempted,
		Queueable:       false,
		Blocked:         false,
		Reason:          "No repairable TaskRun issues were found.",
		Decisions:       []RepairDecision{},
		LastEvaluatedAt: now,
	}
	if hasActiveOrPendingWork(record) {
		policy.Status = "in_progress"
		policy.Reason = "TaskRun still has active or pending work; repair evaluation is deferred."
		return policy
	}
	if len(issues) == 0 {
		return policy
	}
	repairable := 0
	blocked := 0
	for _, issue := range issues {
		decision := RepairDecision{
			IssueKind:  issue.Kind,
			Severity:   issue.Severity,
			StepID:     issue.StepID,
			Repairable: issue.Repairable,
		}
		switch {
		case !issue.Repairable:
			decision.Action = "manual_attention"
			decision.Reason = "Issue is not repairable by the autonomous runner."
			blocked++
		case issue.Severity == "external":
			decision.Action = "wait_external"
			decision.Reason = "Issue depends on user action or external capability."
			blocked++
		case record.SelfCheck.RepairsAttempted >= maxRounds:
			decision.Action = "stop_max_rounds"
			decision.Reason = fmt.Sprintf("Repair policy already used %d/%d rounds.", record.SelfCheck.RepairsAttempted, maxRounds)
			blocked++
		default:
			decision.Action = "queue_repair"
			decision.Reason = "Issue is repairable within the bounded autonomous repair policy."
			repairable++
		}
		policy.Decisions = append(policy.Decisions, decision)
	}
	switch {
	case repairable > 0:
		policy.Status = "ready"
		policy.Queueable = true
		policy.Reason = fmt.Sprintf("%d repairable TaskRun issue(s) can be queued automatically.", repairable)
	case blocked > 0:
		policy.Status = "blocked"
		policy.Blocked = true
		policy.Reason = "TaskRun repair policy requires manual or external attention."
	default:
		policy.Status = "not_needed"
	}
	return policy
}

func selfCheckMetrics(record Record, issues []Issue) map[string]any {
	passed := 0
	for _, check := range record.Acceptance {
		if check.Status == "passed" {
			passed++
		}
	}
	outputCount := 0
	for _, step := range record.Steps {
		if step.OutputPath != "" || len(step.Artifacts) > 0 {
			outputCount++
		}
	}
	return map[string]any{
		"issue_count":         len(issues),
		"acceptance_passed":   passed,
		"artifact_count":      len(record.Artifacts),
		"evidence_count":      len(record.EvidenceIndex),
		"child_output_growth": outputCount,
		"quality_improved":    len(issues) == 0 || passed > 0 || len(record.Artifacts) > 0 || len(record.EvidenceIndex) > 0,
	}
}

func anyRecoveredStep(steps []Step) bool {
	for _, step := range steps {
		if step.RecoveryCount > 0 {
			return true
		}
	}
	return false
}

func hasActiveOrPendingWork(record Record) bool {
	if record.Status == "running" {
		return true
	}
	if len(record.ActiveChildren) > 0 {
		return true
	}
	for _, step := range record.Steps {
		if step.Status == "running" || step.Status == "pending" {
			return true
		}
	}
	return false
}

func anyIssueSeverity(issues []Issue, severity string) bool {
	for _, issue := range issues {
		if issue.Severity == severity {
			return true
		}
	}
	return false
}

func anyIssueKind(issues []Issue, kind string) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}

func dedupeIssues(issues []Issue) []Issue {
	seen := map[string]struct{}{}
	out := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		key := issue.Severity + ":" + issue.Kind + ":" + issue.StepID + ":" + issue.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, issue)
	}
	return out
}
