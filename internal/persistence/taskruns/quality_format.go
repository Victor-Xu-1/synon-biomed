package taskruns

import (
	"fmt"
	"strings"
)

func calculateQuality(steps []Step, acceptance []Acceptance, artifacts []Artifact, now int64) Quality {
	completed := 0
	for _, step := range steps {
		if step.Status == "completed" {
			completed++
		}
	}
	stepRate := ratio(completed, len(steps))
	passed := 0
	for _, check := range acceptance {
		if check.Status == "passed" {
			passed++
		}
	}
	acceptanceRate := ratio(passed, len(acceptance))
	artifactRate := ratio(len(artifacts), len(steps))
	score := int((stepRate*0.45 + acceptanceRate*0.4 + minFloat(artifactRate, 1)*0.15) * 100)
	return Quality{
		Score:                    score,
		StepCompletionRate:       stepRate,
		AcceptancePassRate:       acceptanceRate,
		ArtifactCoverageRate:     minFloat(artifactRate, 1),
		RecoveryPenalty:          0,
		IssueResolutionRate:      acceptanceRate,
		VerificationCoverageRate: acceptanceRate,
		Freshness:                1,
		UpdatedAt:                now,
	}
}

func artifactsFromGraph(graph *Graph) []Artifact {
	if graph == nil || len(graph.Artifacts) == 0 {
		return []Artifact{}
	}
	out := []Artifact{}
	for _, raw := range graph.Artifacts {
		artifact := Artifact{
			StepID:      stringFromMap(raw, "step_id"),
			Kind:        firstNonEmpty(stringFromMap(raw, "kind"), "file"),
			Path:        stringFromMap(raw, "path"),
			Description: firstNonEmpty(stringFromMap(raw, "description"), "TaskRun graph artifact."),
			Metadata:    raw,
		}
		out = append(out, artifact)
	}
	return out
}

func formatMarkdown(record Record) string {
	lines := []string{
		"# TaskRun " + record.RunID,
		"",
		"Status: " + record.Status,
		"Objective: " + record.Objective,
		"",
		"## Completion",
		"- State: " + record.Completion.State,
		"- Verified: " + boolWord(record.Completion.Verified),
		"- Confidence: " + record.Completion.Confidence,
		"- Reason: " + record.Completion.Reason,
		"",
		"## Steps",
	}
	for _, step := range record.Steps {
		line := fmt.Sprintf("- [%s] %s: %s", step.Status, step.ID, step.Title)
		if step.ChildTaskID != "" {
			line += " child=" + step.ChildTaskID
		}
		if step.OutputPath != "" {
			line += " output=" + step.OutputPath
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "## Next Actions")
	if len(record.NextActions) == 0 {
		lines = append(lines, "- None")
	} else {
		for _, action := range record.NextActions {
			lines = append(lines, "- "+action)
		}
	}
	if len(record.Blockers) > 0 {
		lines = append(lines, "", "## Blockers")
		for _, blocker := range record.Blockers {
			lines = append(lines, "- "+blocker.Kind+": "+blocker.Message)
		}
	}
	if len(record.Acceptance) > 0 {
		lines = append(lines, "", "## Acceptance")
		for _, check := range record.Acceptance {
			lines = append(lines, fmt.Sprintf("- [%s] %s: %s", check.Status, check.ID, check.Description))
		}
	}
	if record.RepairPolicy.Status != "" {
		lines = append(lines, "", "## Repair Policy")
		lines = append(lines, "- Status: "+record.RepairPolicy.Status)
		lines = append(lines, "- Mode: "+record.RepairPolicy.Mode)
		lines = append(lines, fmt.Sprintf("- Rounds: %d/%d", record.RepairPolicy.RoundsAttempted, record.RepairPolicy.MaxRounds))
		lines = append(lines, "- Reason: "+record.RepairPolicy.Reason)
	}
	lines = append(lines, "", "## Quality", fmt.Sprintf("- Score: %d", record.Quality.Score))
	return strings.Join(lines, "\n") + "\n"
}
