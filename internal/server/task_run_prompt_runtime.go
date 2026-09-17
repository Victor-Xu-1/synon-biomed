package server

import (
	"fmt"
	"strings"
	taskruns "synon-go/internal/persistence/taskruns"
)

func buildTaskRunSessionPrompt(run taskruns.Record, message string) string {
	lines := []string{
		"TaskRun objective:",
		run.Objective,
		"",
		"Run ID: " + run.RunID,
		"Output path: " + run.OutputPath,
		"Runtime: Go session runner using internal/agentruntime for OpenAI-compatible chat execution.",
	}
	if strings.TrimSpace(message) != "" {
		lines = append(lines, "", "User message:", strings.TrimSpace(message))
	}
	if len(run.SuccessCriteria) > 0 {
		lines = append(lines, "", "Success criteria:")
		for _, criterion := range run.SuccessCriteria {
			lines = append(lines, "- "+criterion)
		}
	}
	if len(run.Constraints) > 0 {
		lines = append(lines, "", "Constraints:")
		for _, constraint := range run.Constraints {
			lines = append(lines, "- "+constraint)
		}
	}
	activeSteps := taskRunActiveSteps(run)
	if len(activeSteps) > 0 {
		lines = append(lines, "", "Current runner step:")
		for _, step := range activeSteps {
			lines = append(lines, fmt.Sprintf("- %s: %s; expected: %s", step.ID, step.Description, step.ExpectedOutput))
		}
	}
	if len(run.SelfCheck.RemainingIssues) > 0 {
		lines = append(lines, "", "Self-check issues to address:")
		for _, issue := range run.SelfCheck.RemainingIssues {
			line := "- " + issue.Kind + " [" + issue.Severity + "]: " + issue.Message
			if issue.StepID != "" {
				line += " (step " + issue.StepID + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(run.SelfCheck.Recommendations) > 0 {
		lines = append(lines, "", "Self-check recommendations:")
		for _, recommendation := range run.SelfCheck.Recommendations {
			lines = append(lines, "- "+recommendation)
		}
	}
	if run.RepairPolicy.Status != "" && run.RepairPolicy.Status != "idle" {
		lines = append(lines, "", "Repair policy:")
		lines = append(lines, "- Status: "+run.RepairPolicy.Status)
		lines = append(lines, fmt.Sprintf("- Rounds: %d/%d", run.RepairPolicy.RoundsAttempted, run.RepairPolicy.MaxRounds))
		lines = append(lines, "- Reason: "+run.RepairPolicy.Reason)
	}
	lines = append(lines, "", "Planned steps:")
	for _, step := range run.Steps {
		lines = append(lines, fmt.Sprintf("- %s: %s; expected: %s", step.ID, step.Description, step.ExpectedOutput))
	}
	lines = append(lines, "", "Work through the objective using available Go runtime tools. Leave concise verification evidence and state any blocker explicitly.")
	return strings.Join(lines, "\n")
}

func taskRunRunnerRuntimeMetadata(run taskruns.Record) map[string]any {
	return map[string]any{
		"protocol":           "session_runner",
		"queue":              "durable_session_journal",
		"engine":             "internal/agentruntime",
		"chatProvider":       "openai_chat",
		"commandProvider":    "external_command",
		"toolGateway":        "server_agent_runtime_tool_gateway",
		"sessionId":          run.SessionID,
		"runId":              run.RunID,
		"toolPolicy":         stringValue(run.Orchestration["toolPolicy"]),
		"requiresRunnerLoop": true,
	}
}

func taskRunActiveSteps(run taskruns.Record) []taskruns.Step {
	activeIDs := map[string]struct{}{}
	for _, child := range run.ActiveChildren {
		if child.StepID != "" {
			activeIDs[child.StepID] = struct{}{}
		}
	}
	steps := []taskruns.Step{}
	for _, step := range run.Steps {
		if _, ok := activeIDs[step.ID]; ok || step.Status == "running" {
			steps = append(steps, step)
		}
	}
	return steps
}
