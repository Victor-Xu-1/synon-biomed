package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	maxSessionRunnerPlanModeUnitDenials         = 3
	planModeDenialToolPhase                     = "plan_mode_denial"
	sessionRunnerPlanApprovalRequiredReasonCode = "plan_approval_required"
)

// The execution unit is bounded; the plan obligation is not. This uses the
// same correction checkpoint and recovery admission as other completion gates.
type sessionRunnerPlanApprovalRequired struct{}

func (sessionRunnerPlanApprovalRequired) Error() string {
	return "plan mode requires a durable plan and approval for the current task input before completion"
}

func (err sessionRunnerPlanApprovalRequired) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	return newRunnerTextCorrection(sessionRunnerPlanApprovalRequiredReasonCode, err.Error())
}

// sessionRunnerPlanModeEnabled reads the existing conversation control. Plan
// mode is an explicit user-selected execution state; task wording is never
// classified to manufacture the state inside the runner.
func sessionRunnerPlanModeEnabled(session sessionstore.Session) bool {
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if config == nil {
		config, _ = session.Orchestration["session_config"].(map[string]any)
	}
	return boolValue(config["planMode"], false) || boolValue(config["plan_mode"], false)
}

// restoreSessionRunnerPlanControl reads one authoritative snapshot. Tracking
// an existing plan and requiring user review are independent runtime states.
func (s *Server) restoreSessionRunnerPlanControl(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
) (tracking, approved bool, err error) {
	explicitReview := sessionRunnerPlanModeEnabled(session)
	tracking = explicitReview
	if run != nil {
		run.AutonomousPlanning = !explicitReview
	}
	if s == nil || s.workspaceStore == nil {
		return tracking, false, nil
	}
	frameID := strings.TrimSpace(session.ID)
	if run != nil && run.Transcript != nil {
		frameID = run.Transcript.Stream.FrameID
	}
	if frameID == "" {
		return tracking, false, nil
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		return tracking, false, err
	}
	hasPlan := strings.TrimSpace(stringValue(metadata.ContextData["_plan_artifact_id"])) != ""
	hasPlan = hasPlan && sessionRunnerPlanMatchesTask(metadata.ContextData, run)
	approved = hasPlan && boolValue(metadata.ContextData["_plan_approved"], false)
	if run != nil {
		run.AutonomousPlanning = !explicitReview && !approved && (!hasPlan || autonomousGeneratedPlan(metadata.ContextData))
		run.planProgressAvailable.Store(hasPlan && (approved || autonomousGeneratedPlan(metadata.ContextData)))
	}
	return tracking || hasPlan, approved, nil
}

func filterSessionRunnerPlanningTools(
	schemas []agentruntime.ToolSchema,
	planModeEnabled bool,
) []agentruntime.ToolSchema {
	return filterSessionRunnerPlanningToolsForState(schemas, planModeEnabled, false)
}

// filterSessionRunnerPlanningToolsForState keeps plan creation available only
// until the durable plan has been approved. Once a plan artifact is approved,
// exposing generate_plan again invites a resumed provider to recreate the same
// plan instead of continuing the execution that the plan authorized.
func filterSessionRunnerPlanningToolsForState(
	schemas []agentruntime.ToolSchema,
	planModeEnabled bool,
	planApproved bool,
) []agentruntime.ToolSchema {
	filtered := make([]agentruntime.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if !planModeEnabled && strings.EqualFold(name, updateStepStatusToolName) {
			continue
		}
		if planApproved && strings.EqualFold(name, generatePlanToolName) {
			continue
		}
		filtered = append(filtered, schema)
	}
	return filtered
}

func sessionRunnerPlanModeRules() string {
	return "Plan mode is active for this request. Use the available tools and ask_user according to the task and evidence, assess feasibility, and call generate_plan when the plan is ready for review. Wait for approval before beginning the planned execution. Do not finish with a prose plan in place of the durable plan artifact."
}

func sessionRunnerPlanModeCorrection(language string, denial int) string {
	if sessionRunnerRequiresChinese(language) {
		return fmt.Sprintf("计划模式完成检查 %d：本请求仍在等待可审批的结构化计划。不要用普通文本计划结束；如有实质性歧义，可先调用 ask_user，否则调用 generate_plan，并等待用户审批后再执行。", denial)
	}
	return fmt.Sprintf("Plan-mode completion gate %d: this request is still waiting for an approvable structured plan. Do not finish with a prose plan. If a material ambiguity remains, call ask_user; otherwise call generate_plan and wait for approval before execution.", denial)
}

func (s *Server) sessionRunnerPlanModePending(session sessionstore.Session, run *sessionRunnerChatRun) (bool, error) {
	if !sessionRunnerPlanModeEnabled(session) {
		return false, nil
	}
	if s == nil || s.workspaceStore == nil || run == nil {
		return true, nil
	}
	frameID := strings.TrimSpace(run.SessionID)
	if run.Transcript != nil && strings.TrimSpace(run.Transcript.Stream.FrameID) != "" {
		frameID = strings.TrimSpace(run.Transcript.Stream.FrameID)
	}
	if frameID == "" {
		return true, nil
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil {
		return false, fmt.Errorf("read plan-mode state: %w", err)
	}
	if !found || strings.TrimSpace(stringValue(metadata.ContextData["_plan_artifact_id"])) == "" || !boolValue(metadata.ContextData["_plan_approved"], false) {
		return true, nil
	}
	return !sessionRunnerPlanMatchesTask(metadata.ContextData, run), nil
}

// Tool availability and completion must consult the same input binding. An
// old approval cannot hide the only capability that can prepare the new plan.
func sessionRunnerPlanMatchesTask(data map[string]any, run *sessionRunnerChatRun) bool {
	if run == nil {
		return true
	}
	if run.TaskIntentID != "" && strings.TrimSpace(stringValue(data["_plan_task_intent_id"])) != strings.TrimSpace(run.TaskIntentID) {
		return false
	}
	if run.TaskIntentRevision > 0 && numberValue(data["_plan_task_intent_revision"]) != run.TaskIntentRevision {
		return false
	}
	return run.TaskIntent == "" || strings.TrimSpace(stringValue(data["_plan_task_intent_sha256"])) == generatedPlanTaskIntentSHA(run.TaskIntent)
}

func sessionRunnerPlanModeDenialCount(entries []eventjournal.Entry) int {
	latest := 0
	for _, entry := range entries {
		if runnerEntryStartsNewLogicalTask(entry) {
			latest = 0
		}
		message := entry.Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["toolPhase"]) != planModeDenialToolPhase {
			continue
		}
		if denial, ok := exactPositiveInt(message["planModeDenial"]); ok && denial > latest {
			latest = denial
		}
	}
	return latest
}

type sessionRunnerPlanModeCandidateRejected func(denial int, correction string) error

// runSessionAgentWithPlanMode never treats an unmet approval obligation as
// success. Each execution unit has a finite opportunity to change strategy;
// further work continues from a durable correction with closed-route admission.
func (s *Server) runSessionAgentWithPlanMode(
	ctx context.Context,
	session sessionstore.Session,
	engine agentruntime.Engine,
	request agentruntime.RunRequest,
	run *sessionRunnerChatRun,
	onRejected sessionRunnerPlanModeCandidateRejected,
) (agentruntime.RunResult, error) {
	denials := 0
	if run != nil {
		denials = run.PlanModeDenials
	}
	for unitDenials := 0; ; {
		if err := ctx.Err(); err != nil {
			return agentruntime.RunResult{}, err
		}
		result, err := engine.Run(ctx, request)
		if err != nil {
			var violation *agentruntime.InitialToolChoiceViolationError
			if errors.As(err, &violation) && ctx.Err() == nil {
				pending, stateErr := s.sessionRunnerPlanModePending(session, run)
				if stateErr != nil {
					return result, stateErr
				}
				if pending {
					// Protocol repair exhausted one provider strategy, not the
					// durable obligation to produce and obtain approval for a plan.
					return result, sessionRunnerPlanApprovalRequired{}
				}
			}
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pending, err := s.sessionRunnerPlanModePending(session, run)
		if err != nil {
			return result, err
		}
		if !pending {
			return result, nil
		}
		denials++
		unitDenials++
		language := ""
		if run != nil {
			language = run.TaskIntent
		}
		correction := sessionRunnerPlanModeCorrection(sessionRunnerResponseLanguage(language), denials)
		if run != nil {
			run.PlanModeDenials = denials
			if strings.TrimSpace(run.ResponseLanguage) != "" {
				correction = sessionRunnerPlanModeCorrection(run.ResponseLanguage, denials)
			}
		}
		if onRejected != nil {
			if err := onRejected(denials, correction); err != nil {
				return result, err
			}
		}
		if unitDenials >= maxSessionRunnerPlanModeUnitDenials {
			return result, sessionRunnerPlanApprovalRequired{}
		}
		request.Messages = append(append([]agentruntime.Message(nil), result.Messages...), agentruntime.Message{
			Role: "user", Content: correction,
		})
		// A prose-only route has been rejected. Require an advertised action,
		// leaving tool and arguments model-selected and all safety gates intact.
		if len(request.Tools) > 0 {
			request.InitialToolChoice = "required"
		}
	}
}

func (s *Server) checkpointSessionRunnerPlanModeDenial(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	denial int,
	correction string,
) error {
	if run == nil || denial <= 0 {
		return nil
	}
	payload := map[string]any{
		"status":           "running",
		"detail":           correction,
		"toolPhase":        planModeDenialToolPhase,
		"planModeDenial":   denial,
		"responseLanguage": strings.TrimSpace(run.ResponseLanguage),
	}
	if run.Transcript != nil {
		event, err := s.checkpointTranscriptRunnerEvent(
			ctx, run.Transcript, transcriptstore.RunnerPhasePlanning,
			fmt.Sprintf("plan-mode-denial-%02d", denial), payload, false,
		)
		if err == nil {
			run.AfterEventID = maxInt64(run.AfterEventID, event.EventID)
		}
		return err
	}
	checkpoint, err := s.checkpointSessionRunner(map[string]any{
		"sessionId": run.SessionID, "runnerId": options.RunnerID,
		"runnerAttempt": run.Attempt, "claimToken": run.ClaimToken,
		"status": "running", "message": correction, "afterEventId": run.AfterEventID,
		"toolPhase": planModeDenialToolPhase, "planModeDenial": denial,
		"clientMessageId": runnerCommandClientMessageID(
			options.RunnerID, run.SessionID, run.Attempt, run.ClaimToken,
			fmt.Sprintf("plan-mode-denial-%02d", denial),
		),
	})
	if err != nil {
		return err
	}
	if entry, ok := checkpoint["event"].(*eventjournal.Entry); ok && entry != nil {
		run.AfterEventID = maxInt64(run.AfterEventID, entry.EventID)
	}
	return nil
}
