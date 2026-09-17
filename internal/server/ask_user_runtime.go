package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

func (s *Server) executeAgentAskUserQuestion(ctx context.Context, sessionID, toolCallID, toolName string, input map[string]any) (any, error) {
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil || canonical != toolcontract.AskUser {
		return nil, errors.New("ask_user tool name is invalid")
	}
	toolName = canonical
	result, err := s.executeAskUserQuestionTool(toolName, input)
	if err != nil {
		return nil, err
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run != nil && run.Transcript != nil {
		resultMap := result.(map[string]any)
		if err := s.normalizeAgentAskUserDecisionEvidence(ctx, run, resultMap); err != nil {
			return nil, err
		}
		if resolution, resolved := s.resolveUniqueRegisteredImplementationAskUser(ctx, run, resultMap); resolved {
			return resolution, nil
		}
		if correction := askUserImplementationCapabilityContractCorrection(s.skillCatalog, run, resultMap); correction != nil {
			return correction, nil
		}
		if correction := askUserManagedExecutionParameterEvidenceCorrection(
			s.skillCatalog, s.scienceCapabilities, run, resultMap,
		); correction != nil {
			return correction, nil
		}
		if correction := s.askUserSelectedEvidenceResolverContinuityCorrection(run, resultMap); correction != nil {
			return correction, nil
		}
		if correction := askUserImplementationSelectionContractCorrection(run, resultMap); correction != nil {
			return correction, nil
		}
	}
	answers, _ := result.(map[string]any)["answers"].(map[string]any)
	if len(answers) > 0 || s.workspaceStore == nil {
		return result, nil
	}
	if _, found, err := s.workspaceStore.GetFrame(strings.TrimSpace(sessionID)); err != nil {
		return nil, err
	} else if !found {
		return result, nil
	}
	questions, err := askUserQuestionsForPersistence(result.(map[string]any)["questions"])
	if err != nil {
		return nil, err
	}
	if run != nil && run.Transcript != nil {
		return nil, transcriptAskUserPause(sessionID, toolCallID, toolName, questions)
	}
	return nil, errors.New("transcript AskUser authority is required for a Frame")
}

func transcriptAskUserPause(sessionID, toolCallID, toolName string, questions []any) *agentruntime.PauseError {
	return &agentruntime.PauseError{
		Status:  "awaiting_user_response",
		Message: "waiting for the user to answer the requested clarification",
		Data: map[string]any{
			"session_id": strings.TrimSpace(sessionID),
			"tool_id":    strings.TrimSpace(toolCallID),
			"tool_name":  strings.TrimSpace(toolName),
			"questions":  questions,
		},
	}
}

func askUserQuestionsForPersistence(value any) ([]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode ask-user questions: %w", err)
	}
	questions := []any{}
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, fmt.Errorf("normalize ask-user questions: %w", err)
	}
	if len(questions) == 0 {
		return nil, errors.New("ask-user questions are empty after validation")
	}
	return questions, nil
}

func askUserQuestionsForPersistenceFromToolInput(input map[string]any) ([]any, error) {
	normalized, err := normalizeAskUserToolInput(input)
	if err != nil {
		return nil, err
	}
	questions, err := askUserQuestionValue(normalized)
	if err != nil {
		return nil, err
	}
	return askUserQuestionsForPersistence(questions)
}

func (s *Server) parkTranscriptAskUser(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	pause *agentruntime.PauseError,
) (workspace.ParkAskUserResult, transcriptstore.Event, error) {
	if s == nil || s.workspaceStore == nil || authority == nil || pause == nil {
		return workspace.ParkAskUserResult{}, transcriptstore.Event{}, errors.New("transcript AskUser authority is required")
	}
	sessionID := strings.TrimSpace(stringValue(pause.Data["session_id"]))
	toolID := strings.TrimSpace(stringValue(pause.Data["tool_id"]))
	toolName, toolNameErr := canonicalRuntimeToolName(stringValue(pause.Data["tool_name"]))
	if sessionID == "" || sessionID != authority.Stream.FrameID || sessionID != authority.Stream.SessionID || toolID == "" {
		return workspace.ParkAskUserResult{}, transcriptstore.Event{}, errors.New("transcript AskUser target does not match the runner authority")
	}
	if toolNameErr != nil || toolName != toolcontract.AskUser {
		return workspace.ParkAskUserResult{}, transcriptstore.Event{}, errors.New("transcript AskUser tool name is invalid")
	}
	questions, err := askUserQuestionsForPersistence(pause.Data["questions"])
	if err != nil {
		return workspace.ParkAskUserResult{}, transcriptstore.Event{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"status": strings.TrimSpace(pause.Status), "detail": strings.TrimSpace(pause.Error()),
	})
	if err != nil {
		return workspace.ParkAskUserResult{}, transcriptstore.Event{}, err
	}
	return s.workspaceStore.ParkAskUserWithTranscript(ctx, workspace.ParkAskUserInput{
		FrameID: sessionID, ToolID: toolID, ToolName: toolName, Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim:           authority.Claim,
		ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, "runner-paused"),
		Phase:           transcriptstore.RunnerPhaseWaitingUser, Resumable: true, PayloadJSON: payload,
		Destinations: transcriptRunnerDestinations(authority),
	})
}

func canonicalRuntimeToolName(name string) (string, error) {
	canonical, ok := toolcontract.NormalizeRuntimeName(name)
	if !ok {
		return "", errors.New("tool name is invalid")
	}
	return canonical, nil
}
