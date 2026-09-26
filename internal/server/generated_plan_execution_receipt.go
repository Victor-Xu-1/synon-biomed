package server

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type generatedPlanExecutionBinding struct {
	CallID  string `json:"call_id"`
	Tool    string `json:"tool"`
	EventID int64  `json:"event_id"`
	PackID  string `json:"execution_pack_id,omitempty"`
}

// One fenced Transcript scan resolves the execution binding. Autonomous plan
// bookkeeping is not a prerequisite for recognizing work, but an explicit
// restart of a step creates a new window. A receipt already claimed by another
// step never becomes evidence for this one.
func (s *Server) resolveGeneratedPlanExecutionReceipt(
	ctx context.Context,
	run *sessionRunnerChatRun,
	step generatedPlanStepIdentity,
	callID string,
	plan map[string]any,
) (generatedPlanExecutionBinding, []generatedPlanExecutionBinding, error) {
	if run == nil || run.Transcript == nil {
		return generatedPlanExecutionBinding{}, nil, nil
	}
	current := mapValue(mapValue(plan["_step_statuses"])[step.ID])
	if callID == "" && stringValue(current["status"]) == "completed" {
		callID = stringValue(current["execution_ref"])
	}
	claimed := map[string]string{}
	for id, raw := range mapValue(plan["_step_statuses"]) {
		state := mapValue(raw)
		if ref := stringValue(state["execution_ref"]); ref != "" && stringValue(state["status"]) == "completed" && id != step.ID {
			claimed[ref] = id
		}
	}
	authority := run.Transcript
	boundary, required, err := s.sessionRunnerDurableTaskBoundary(ctx, run)
	if err != nil {
		return generatedPlanExecutionBinding{}, nil, err
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return generatedPlanExecutionBinding{}, nil, err
	}
	boundaryReached := !required
	started := autonomousGeneratedPlan(plan)
	planStart := int64(numberValue(plan["_plan_source_event_id"]))
	knownTool := s.generatedPlanExecutionToolKnown(run, step.ExecutionTool)
	candidates := map[string]generatedPlanExecutionBinding{}
	duplicates := map[string]bool{}
	overflow := false
	after := int64(0)
	for {
		page, listErr := s.transcriptStore.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence: after, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
			Limit: sessionRunnerDurableEvidencePageSize,
		})
		if listErr != nil {
			return generatedPlanExecutionBinding{}, nil, listErr
		}
		if len(page) == 0 && after < snapshot.ThroughPublicationSequence {
			return generatedPlanExecutionBinding{}, nil, errors.New("plan execution receipt projection ended before its accepted fence")
		}
		for _, projected := range page {
			if projected.Event.PublicationSeq <= after || projected.Event.PublicationSeq > snapshot.ThroughPublicationSequence {
				return generatedPlanExecutionBinding{}, nil, errors.New("plan execution receipt projection cursor is invalid")
			}
			after = projected.Event.PublicationSeq
			if !boundaryReached && sessionRunnerProjectedEventMatchesTaskBoundary(projected.Event, boundary) {
				boundaryReached = true
			}
			if !boundaryReached || projected.Event.Type != "runner_checkpoint" {
				continue
			}
			payload := projected.ResolvedPayloadJSON
			if len(payload) == 0 {
				payload = projected.Event.PayloadJSON
			}
			var checkpoint sessionRunnerDurableToolCheckpoint
			if json.Unmarshal(payload, &checkpoint) != nil ||
				!s.sessionRunnerDurableCheckpointExplicitTool(checkpoint) ||
				!strings.EqualFold(strings.TrimSpace(checkpoint.ToolPhase), "completed") {
				continue
			}
			name := strings.TrimSpace(checkpoint.ToolName)
			if normalizeAgentToolName(name) == normalizeAgentToolName(updateStepStatusToolName) {
				value, valid, valueErr := s.generatedPlanCompletedToolValue(ctx, authority.Stream, checkpoint, projected.Event.EventID)
				if valueErr != nil {
					return generatedPlanExecutionBinding{}, nil, valueErr
				}
				if valid && stringValue(value["step"]) == step.ID && value["applied"] != false {
					switch stringValue(value["status"]) {
					case "in_progress":
						if value["idempotent"] != true {
							started = true
							candidates, duplicates, overflow = map[string]generatedPlanExecutionBinding{}, map[string]bool{}, false
						}
					case "blocked", "skipped":
						started = false
					}
				}
				continue
			}
			ref := strings.TrimSpace(checkpoint.ToolCallID)
			if !started || ref == "" || claimed[ref] != "" || checkpoint.RejectedBeforeExecution ||
				(projected.Event.EventID < planStart && ref != stringValue(current["execution_ref"])) ||
				(callID != "" && ref != callID) {
				continue
			}
			if knownTool && normalizeAgentToolName(name) != normalizeAgentToolName(step.ExecutionTool) {
				continue
			}
			value, valid, valueErr := s.generatedPlanCompletedToolValue(ctx, authority.Stream, checkpoint, projected.Event.EventID)
			if valueErr != nil {
				return generatedPlanExecutionBinding{}, nil, valueErr
			}
			if !valid || !generatedPlanSuccessfulExecutionValue(value) {
				continue
			}
			binding := generatedPlanExecutionBinding{CallID: ref, Tool: name, EventID: projected.Event.EventID}
			if !knownTool {
				packID, packErr := s.generatedPlanReceiptExecutionPack(ctx, run, checkpoint, value)
				if packErr != nil {
					return generatedPlanExecutionBinding{}, nil, packErr
				}
				if packID == "" {
					continue
				}
				binding.PackID = packID
			}
			if _, exists := candidates[ref]; exists {
				duplicates[ref] = true
			} else if len(candidates) < 16 || callID != "" {
				candidates[ref] = binding
			} else {
				overflow = true
			}
		}
		if len(page) < sessionRunnerDurableEvidencePageSize {
			break
		}
	}
	if !boundaryReached {
		return generatedPlanExecutionBinding{}, nil, errors.New("plan execution receipt task boundary is unavailable")
	}
	choices := make([]generatedPlanExecutionBinding, 0, len(candidates))
	for ref, candidate := range candidates {
		if !duplicates[ref] {
			choices = append(choices, candidate)
		}
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].EventID < choices[j].EventID })
	// An invalid old tool proposal cannot silently choose a computation. The
	// model receives exact verified candidates and explicitly binds one through
	// the existing execution_ref field; no scientific work is repeated.
	if len(choices) == 1 && !overflow && (knownTool || callID != "") {
		return choices[0], choices, nil
	}
	return generatedPlanExecutionBinding{}, choices, nil
}

func (s *Server) generatedPlanCompletedToolValue(
	ctx context.Context,
	stream transcriptstore.Stream,
	checkpoint sessionRunnerDurableToolCheckpoint,
	eventID int64,
) (map[string]any, bool, error) {
	result, valid := sessionRunnerDurableToolResult(checkpoint.ToolResult)
	if !valid {
		return nil, false, nil
	}
	result, err := s.restoreDurableEvidencePayload(ctx, stream, checkpoint, eventID, result)
	if err != nil {
		return nil, false, err
	}
	var value map[string]any
	if json.Unmarshal([]byte(result), &value) != nil || value == nil {
		return nil, false, nil
	}
	return value, true, nil
}

func generatedPlanSuccessfulExecutionValue(value map[string]any) bool {
	if value["ok"] != true || value["executed"] == false ||
		agentruntime.ClassifyToolResult(value) != agentruntime.ToolResultSucceeded ||
		agentruntime.IsNonExecutingPreflight(value) {
		return false
	}
	for _, record := range []map[string]any{value, mapValue(value["result"])} {
		if record == nil || record["terminal"] == false {
			if record != nil {
				return false
			}
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(record["status"]))) {
		case "pending", "queued", "submitted", "accepted", "starting", "running", "processing", "in_progress", "background":
			return false
		}
		for _, key := range []string{"exit_code", "exitCode"} {
			if exit, present := record[key]; present {
				code, valid := exit.(float64)
				if !valid || code != 0 {
					return false
				}
			}
		}
		if status := strings.TrimSpace(stringValue(record["exit_status"])); status != "" &&
			!strings.EqualFold(status, "ok") && !strings.EqualFold(status, "completed") {
			return false
		}
	}
	return true
}
