package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"synon-go/internal/agentruntime"
)

func autonomousGeneratedPlan(data map[string]any) bool {
	return data["_plan_control_mode"] == "autonomous" &&
		boolValue(data["_plan_execution_authorized"], false) && !boolValue(data["_plan_approved"], false)
}

// The interface accepts complete replacement content, not user-supplied IDs.
// Reconcile only exact scoped definitions; guessing semantic equivalence would
// risk transferring old completion evidence to a new investigation.
func reconcileGeneratedPlanRevision(current map[string]any, next generatedPlanDocument, callID string) (generatedPlanDocument, map[string]any, []byte, bool, error) {
	oldRaw, err := json.Marshal(current)
	if err != nil {
		return next, nil, nil, false, err
	}
	var previous generatedPlanDocument
	if err := json.Unmarshal(oldRaw, &previous); err != nil {
		return next, nil, nil, false, err
	}
	oldShape := generatedPlanContentShape(previous)
	newShape := generatedPlanContentShape(next)
	oldShapeJSON, _ := json.Marshal(oldShape)
	newShapeJSON, _ := json.Marshal(newShape)
	if bytes.Equal(oldShapeJSON, newShapeJSON) {
		return previous, current, oldRaw, true, nil
	}
	type key struct{ Phase, Track, Agent, Title, Description, Kind, ExecutionTool, Module, Question, Depth, Queries string }
	stepKey := func(phase generatedPlanPhase, track generatedPlanDelegation, step generatedPlanStep) key {
		queries, _ := json.Marshal(step.DiscoveryQueries)
		return key{
			Phase: phase.Name, Track: track.Name, Agent: track.AgentName, Title: step.Title,
			Description: step.Description, Kind: step.Kind, ExecutionTool: step.ExecutionTool, Module: step.OutputModule,
			Question: step.ResearchQuestion, Depth: step.ResearchDepth, Queries: string(queries),
		}
	}
	oldIDs := map[key][]string{}
	newCounts := map[key]int{}
	for _, phase := range previous.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				k := stepKey(phase, track, step)
				oldIDs[k] = append(oldIDs[k], step.ID)
			}
		}
	}
	for _, phase := range next.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				newCounts[stepKey(phase, track, step)]++
			}
		}
	}
	priorPhasesUnchanged := true
	for p := range next.Phases {
		phase := &next.Phases[p]
		for d := range phase.Delegations {
			track := &phase.Delegations[d]
			for i := range track.Steps {
				step := &track.Steps[i]
				k := stepKey(*phase, *track, *step)
				if ids := oldIDs[k]; priorPhasesUnchanged && len(ids) == 1 && newCounts[k] == 1 {
					step.ID = ids[0]
				} else {
					digest := sha256.Sum256([]byte(fmt.Sprintf("plan-step-revision\x00%s\x00%d\x00%d\x00%d", callID, p, d, i)))
					step.ID = "step-" + hex.EncodeToString(digest[:16])
				}
			}
		}
		// Phases are sequential in the existing contract. A changed preceding
		// phase invalidates downstream completion, but not independent tracks
		// in the current phase. Old notes remain keyed by their original IDs.
		if p >= len(oldShape.Phases) {
			priorPhasesUnchanged = false
		} else {
			before, _ := json.Marshal(oldShape.Phases[p])
			after, _ := json.Marshal(newShape.Phases[p])
			priorPhasesUnchanged = priorPhasesUnchanged && bytes.Equal(before, after)
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return next, nil, nil, false, err
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return next, nil, nil, false, err
	}
	document, normalized, encoded, err := normalizeGeneratedPlan(input)
	return document, normalized, encoded, false, err
}

func generatedPlanContentShape(document generatedPlanDocument) generatedPlanDocument {
	shape := document
	shape.Phases = append([]generatedPlanPhase(nil), document.Phases...)
	for p := range shape.Phases {
		phase := &shape.Phases[p]
		phase.ID = ""
		phase.Delegations = append([]generatedPlanDelegation(nil), phase.Delegations...)
		for d := range phase.Delegations {
			track := &phase.Delegations[d]
			track.ID = ""
			track.Steps = append([]generatedPlanStep(nil), track.Steps...)
			for i := range track.Steps {
				track.Steps[i].ID = ""
			}
		}
	}
	return shape
}

func generatedWorkingPlanReceipt(document generatedPlanDocument, data map[string]any, idempotent bool) map[string]any {
	navigation := generatedPlanResearchNavigation(document, data)
	steps := navigation["current_investigations"]
	return map[string]any{
		"ok": true, "status": "working_plan_ready", "artifact_id": data["_plan_artifact_id"], "version_id": data["_plan_version_id"],
		"task_summary": document.TaskSummary, "phases": len(document.Phases), "steps": steps,
		"retired_research": navigation["retired_research"], "research_navigation": navigation, "idempotent": idempotent,
		"effect": agentruntime.ToolEffectValue(toolEffectState(idempotent), "control-state", "plan"),
	}
}
