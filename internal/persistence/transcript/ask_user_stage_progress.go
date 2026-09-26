package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// AskUserStageProgressV1 is optional server-derived context on the existing
// durable AskUser question. It reports plan state, not verified scientific
// quality or permission to settle the whole task.
type AskUserStageStepV1 struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type AskUserStageProgressV1 struct {
	Schema         string               `json:"schema"`
	PlanArtifactID string               `json:"plan_artifact_id"`
	PlanVersionID  string               `json:"plan_version_id"`
	CompletedCount int                  `json:"completed_count"`
	RemainingCount int                  `json:"remaining_count"`
	CompletedSteps []AskUserStageStepV1 `json:"completed_steps"`
	RemainingSteps []AskUserStageStepV1 `json:"remaining_steps"`
}

const AskUserStageProgressLimit = 12

func normalizeAskUserStageProgress(value any) (*AskUserStageProgressV1, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 16<<10 {
		return nil, errors.New("AskUser stage progress is invalid")
	}
	var progress AskUserStageProgressV1
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return nil, errors.New("AskUser stage progress is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("AskUser stage progress is invalid")
	}
	if progress.Schema != "synon.plan_stage_progress.v1" ||
		progress.PlanArtifactID == "" || len(progress.PlanArtifactID) > 128 ||
		strings.TrimSpace(progress.PlanArtifactID) != progress.PlanArtifactID ||
		progress.PlanVersionID == "" || len(progress.PlanVersionID) > 128 ||
		strings.TrimSpace(progress.PlanVersionID) != progress.PlanVersionID ||
		progress.CompletedCount < 1 || progress.RemainingCount < 1 ||
		progress.CompletedCount+progress.RemainingCount > 256 ||
		len(progress.CompletedSteps) != min(progress.CompletedCount, AskUserStageProgressLimit) ||
		len(progress.RemainingSteps) != min(progress.RemainingCount, AskUserStageProgressLimit) {
		return nil, errors.New("AskUser stage progress is invalid")
	}
	seen := map[string]bool{}
	for _, group := range [][]AskUserStageStepV1{progress.CompletedSteps, progress.RemainingSteps} {
		for _, step := range group {
			if step.ID == "" || len(step.ID) > 128 || strings.TrimSpace(step.ID) != step.ID ||
				step.Title == "" || len(step.Title) > 512 || strings.TrimSpace(step.Title) != step.Title ||
				seen[step.ID] {
				return nil, errors.New("AskUser stage progress is invalid")
			}
			seen[step.ID] = true
		}
	}
	for _, step := range progress.CompletedSteps {
		if step.Status != "completed" {
			return nil, errors.New("AskUser stage progress is invalid")
		}
	}
	for _, step := range progress.RemainingSteps {
		switch step.Status {
		case "pending", "in_progress", "blocked", "skipped":
		default:
			return nil, errors.New("AskUser stage progress is invalid")
		}
	}
	return &progress, nil
}
