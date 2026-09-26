package server

import (
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type generatedPlanStageStep = transcriptstore.AskUserStageStepV1
type generatedPlanStageProgress = transcriptstore.AskUserStageProgressV1

// AskUser remains the one durable pause/resume authority. Its optional stage
// snapshot is derived from the same current-task plan used for completion.
// It is progress metadata, not a scientific quality judgment.
func (s *Server) generatedPlanStageProgressSnapshot(frameID string, run *sessionRunnerChatRun) *generatedPlanStageProgress {
	snapshot, found := s.generatedPlanProcessSnapshot(frameID)
	if !found || (run != nil && !sessionRunnerPlanMatchesTask(snapshot.data, run)) {
		return nil
	}
	steps, err := generatedPlanStepIdentities(mapValue(snapshot.data["_plan_json"]))
	if err != nil {
		return nil
	}
	progress := &generatedPlanStageProgress{
		Schema:         "synon.plan_stage_progress.v1",
		PlanArtifactID: strings.TrimSpace(stringValue(snapshot.data["_plan_artifact_id"])),
		PlanVersionID:  strings.TrimSpace(stringValue(snapshot.data["_plan_version_id"])),
		CompletedSteps: make([]generatedPlanStageStep, 0),
		RemainingSteps: make([]generatedPlanStageStep, 0),
	}
	statuses := mapValue(snapshot.data["_step_statuses"])
	for _, step := range steps {
		status := strings.TrimSpace(stringValue(mapValue(statuses[step.ID])["status"]))
		if status == "" {
			status = "pending"
		}
		item := generatedPlanStageStep{ID: step.ID, Title: step.Title, Status: status}
		if status == "completed" {
			progress.CompletedCount++
			if len(progress.CompletedSteps) < transcriptstore.AskUserStageProgressLimit {
				progress.CompletedSteps = append(progress.CompletedSteps, item)
			}
			continue
		}
		progress.RemainingCount++
		if len(progress.RemainingSteps) < transcriptstore.AskUserStageProgressLimit {
			progress.RemainingSteps = append(progress.RemainingSteps, item)
		}
	}
	if progress.CompletedCount == 0 || progress.RemainingCount == 0 {
		return nil
	}
	return progress
}
