package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	taskruns "synon-go/internal/persistence/taskruns"
	"time"
)

func (s *Server) executeAgentTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.taskRunStore == nil {
		return nil, errors.New("TaskRun store is not configured")
	}
	prompt := strings.TrimSpace(stringValue(input["prompt"]))
	description := strings.TrimSpace(stringValue(input["description"]))
	name := strings.TrimSpace(stringValue(input["name"]))
	parentSessionID := firstNonEmpty(agentRuntimeParentSessionID(ctx), stringValue(input["parent_session_id"]), stringValue(input["parentSessionId"]))
	subagentType := firstNonEmpty(stringValue(input["subagent_type"]), stringValue(input["subagentType"]), "general-purpose")
	model := strings.TrimSpace(stringValue(input["model"]))
	toolPolicy := normalizeAgentToolPolicy(stringValue(input["tool_policy"]))
	orchestration := agentDelegationOrchestration(input, toolName, name, subagentType, model, toolPolicy, parentSessionID)
	objective := firstNonEmpty(description, name, compactText(prompt, 120))
	constraints := []string{
		"Run as an original-compatible Agent/Task delegation through the Go durable session runner.",
		"Do not use Web UI or excluded pharma/knowledge modules.",
	}
	switch toolPolicy {
	case "read_only":
		constraints = append(constraints, "Respect read_only tool_policy: inspect and report without editing files or running mutating commands.")
	case "restricted":
		constraints = append(constraints, "Respect restricted tool_policy: inherit the parent runner boundary and use only low-risk inspection/search/question/sleep/read-only runtime tools.")
	case "full_access":
		constraints = append(constraints, "Respect full_access tool_policy: inherit the parent runner boundary; mutating, external, MCP, Synon Link, and shell tools still require runtime gates.")
	}
	if cwd := strings.TrimSpace(stringValue(input["cwd"])); cwd != "" {
		constraints = append(constraints, "Requested cwd: "+cwd)
	}
	if isolation := strings.TrimSpace(stringValue(input["isolation"])); isolation != "" {
		constraints = append(constraints, "Requested isolation: "+isolation)
	}
	run, err := s.taskRunStore.Create(taskruns.Input{
		Action:    "start",
		Objective: objective,
		Message:   prompt,
		SuccessCriteria: []string{
			"Agent prompt is completed or a concrete blocker is recorded.",
			"Agent output is available through the returned outputFile/output_path.",
		},
		Constraints: constraints,
		ExecutorScope: []string{
			"agent:" + subagentType,
		},
		Orchestration: orchestration,
		TaskGraph: &taskruns.Graph{Steps: []taskruns.StepInput{
			{
				ID:              "agent",
				Title:           "Run delegated agent",
				Description:     prompt,
				Intent:          "delegate",
				ExpectedOutput:  "Concise agent result with files, commands, evidence, and blockers if any.",
				AcceptanceCheck: "The result satisfies the delegated prompt or states an explicit blocker.",
				RiskLevel:       "medium",
				MaxRecoveries:   1,
				Executor: map[string]any{
					"kind":          "agent",
					"agent_type":    subagentType,
					"name":          name,
					"model":         model,
					"tool_policy":   toolPolicy,
					"source_tool":   toolName,
					"original_tool": "Agent",
				},
			},
		}},
	})
	if err != nil {
		return nil, err
	}
	queued, err := s.queueTaskRunSession(run, "Agent delegation started")
	if err != nil {
		return nil, err
	}
	runInBackground := boolValue(input["run_in_background"], false)
	result := map[string]any{
		"agentId":           queued.SessionID,
		"task_id":           queued.RunID,
		"run_id":            queued.RunID,
		"sessionId":         queued.SessionID,
		"description":       description,
		"prompt":            prompt,
		"subagent_type":     subagentType,
		"outputFile":        queued.OutputPath,
		"output_path":       queued.OutputPath,
		"canReadOutputFile": true,
		"orchestration":     queued.Orchestration,
		"taskRun":           queued,
	}
	if name != "" {
		result["name"] = name
	}
	if model != "" {
		result["model"] = model
	}
	if toolPolicy != "" {
		result["tool_policy"] = toolPolicy
	}
	if !runInBackground {
		completed, output, err := s.runAgentDelegationSynchronously(ctx, queued)
		if err != nil {
			return nil, err
		}
		result["status"] = "completed"
		result["output"] = output
		result["taskRun"] = completed
		result["completion"] = completed.Completion
		result["self_check"] = completed.SelfCheck
		return result, nil
	}
	result["status"] = "async_launched"
	return result, nil
}
func (s *Server) waitForAgentDelegationRun(ctx context.Context, runID string) (taskruns.Record, error) {
	if ctx == nil {
		return taskruns.Record{}, errors.New("delegated Agent wait context is required")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		reconciled, err := s.reconcileTaskRun(runID)
		if err != nil {
			return taskruns.Record{}, err
		}
		if isTerminalTaskRunStatus(reconciled.Status) {
			return reconciled, nil
		}
		select {
		case <-waitCtx.Done():
			return taskruns.Record{}, fmt.Errorf("waiting for delegated Agent runner: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) runAgentDelegationSynchronously(ctx context.Context, run taskruns.Record) (taskruns.Record, string, error) {
	if s == nil || s.sessionStore == nil || s.eventJournal == nil || s.taskRunStore == nil {
		return taskruns.Record{}, "", errors.New("session runner store is not configured")
	}
	if ctx == nil {
		return taskruns.Record{}, "", errors.New("sync Agent runner context is required")
	}
	runnerID := "synon-go-sync-agent-" + run.RunID
	cycle, err := s.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
		SessionID:        run.SessionID,
		RunnerID:         runnerID,
		Endpoint:         BuiltinSessionRunnerChatEndpoint,
		Model:            BuiltinSessionRunnerChatModel,
		ReplayLimit:      defaultSessionRunnerReplayLimit,
		OutputLimitBytes: defaultSessionRunnerOutputLimitBytes,
	})
	if err != nil {
		return taskruns.Record{}, "", err
	}
	if !cycle.Claimed {
		reconciled, waitErr := s.waitForAgentDelegationRun(ctx, run.RunID)
		if waitErr != nil {
			return taskruns.Record{}, "", waitErr
		}
		if reconciled.Status != "completed" && reconciled.Status != "verified" {
			return reconciled, "", fmt.Errorf("sync Agent runner finished with status %s", firstNonEmpty(reconciled.Status, "unknown"))
		}
		cycle.Status = "completed"
	}
	if cycle.Status != "completed" {
		reconciled, reconcileErr := s.reconcileTaskRun(run.RunID)
		if reconcileErr != nil {
			return taskruns.Record{}, "", reconcileErr
		}
		if contextErr := agentRuntimeContextError(ctx); contextErr != nil {
			return reconciled, "", contextErr
		}
		return reconciled, "", fmt.Errorf("sync Agent runner finished with status %s", firstNonEmpty(cycle.Status, "unknown"))
	}
	output, err := s.latestAssistantOutput(run.SessionID)
	if err != nil {
		return taskruns.Record{}, "", err
	}
	verified, err := s.taskRunStore.Verify(run.RunID)
	if err != nil {
		return taskruns.Record{}, "", err
	}
	if err := s.syncTaskRunGoalLedger(verified); err != nil {
		return taskruns.Record{}, "", err
	}
	return verified, output, nil
}

func (s *Server) latestAssistantOutput(sessionID string) (string, error) {
	if s == nil || s.eventJournal == nil {
		return "", errors.New("session event journal is not configured")
	}
	entries, err := s.eventJournal.ReadAfter(sessionID, 0, int(defaultSessionRunnerReplayLimit))
	if err != nil {
		return "", err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.TrimSpace(stringValue(entries[i].Message["role"])) != "assistant" {
			continue
		}
		text := strings.TrimSpace(runnerMessageText(entries[i].Message))
		if text != "" {
			return text, nil
		}
	}
	return "", errors.New("sync Agent runner completed without assistant output")
}

func (s *Server) executeSendMessageTool(ctx context.Context, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("SendMessage", input); err != nil {
		return nil, err
	}
	if s.taskRunStore == nil || s.sessionStore == nil || s.eventJournal == nil {
		return nil, errors.New("session runner store is not configured")
	}
	to := strings.TrimSpace(stringValue(input["to"]))
	if to == "" {
		return nil, errors.New("SendMessage.to must not be empty")
	}
	message, messageText, err := sendMessagePayload(input["message"])
	if err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(stringValue(input["summary"]))
	if isAgentRuntimeApprovalMessage(to, message) {
		return s.resolveAgentRuntimeApprovalMessage(ctx, to, message)
	}
	if messageText != "" && summary == "" {
		return nil, errors.New("SendMessage.summary is required when message is a string")
	}
	if to == "*" {
		return s.broadcastSendMessage(messageText, summary)
	}
	target, err := s.resolveAgentMessageTarget(to)
	if err != nil {
		return nil, err
	}
	if target.RunID != "" && messageText != "" {
		resumed, err := s.taskRunStore.Resume(target.RunID, messageText)
		if err != nil {
			return nil, err
		}
		queued, err := s.queueTaskRunSession(resumed, messageText)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"success": true,
			"message": fmt.Sprintf("Message queued for delivery to %s at its next runner turn.", to),
			"routing": map[string]any{
				"sender":  "synon-go",
				"target":  to,
				"summary": summary,
				"content": messageText,
			},
			"agentId":     queued.SessionID,
			"task_id":     queued.RunID,
			"run_id":      queued.RunID,
			"sessionId":   queued.SessionID,
			"outputFile":  queued.OutputPath,
			"output_path": queued.OutputPath,
		}, nil
	}
	event, err := s.appendSessionToolEvent(map[string]any{
		"sessionId": target.SessionID,
		"role":      "user",
		"runId":     target.RunID,
		"message": map[string]any{
			"type":         "send_message",
			"to":           to,
			"summary":      summary,
			"payload":      message,
			"message_text": messageText,
		},
	})
	if err != nil {
		return nil, err
	}
	delivered := s.publishSessionEntry(event)
	output := map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("Structured message delivered to %s.", to),
		"sessionId": target.SessionID,
		"delivered": delivered,
		"event":     event,
		"routing": map[string]any{
			"sender":  "synon-go",
			"target":  to,
			"summary": summary,
		},
	}
	if requestID := stringValue(message["request_id"]); requestID != "" {
		output["request_id"] = requestID
	}
	return output, nil
}

type agentMessageTarget struct {
	RunID     string
	SessionID string
}

func (s *Server) resolveAgentMessageTarget(to string) (agentMessageTarget, error) {
	to = strings.TrimSpace(to)
	if strings.HasPrefix(to, "taskrun:") {
		sessionID := to
		runID := strings.TrimPrefix(to, "taskrun:")
		if run, found, err := s.taskRunStore.Get(runID); err != nil {
			return agentMessageTarget{}, err
		} else if found {
			return agentMessageTarget{RunID: run.RunID, SessionID: run.SessionID}, nil
		}
		if _, ok, err := s.sessionStore.Get(sessionID); err != nil {
			return agentMessageTarget{}, err
		} else if ok {
			return agentMessageTarget{SessionID: sessionID}, nil
		}
	}
	if run, found, err := s.taskRunStore.Get(to); err != nil {
		return agentMessageTarget{}, err
	} else if found {
		return agentMessageTarget{RunID: run.RunID, SessionID: run.SessionID}, nil
	}
	runs, err := s.taskRunStore.List()
	if err != nil {
		return agentMessageTarget{}, err
	}
	for _, run := range runs {
		if strings.EqualFold(stringValue(run.Orchestration["agentName"]), to) {
			return agentMessageTarget{RunID: run.RunID, SessionID: run.SessionID}, nil
		}
	}
	if _, ok, err := s.sessionStore.Get(to); err != nil {
		return agentMessageTarget{}, err
	} else if ok {
		return agentMessageTarget{SessionID: to}, nil
	}
	return agentMessageTarget{}, fmt.Errorf("SendMessage target not found: %s", to)
}

func agentDelegationOrchestration(input map[string]any, toolName string, name string, subagentType string, model string, toolPolicy string, parentSessionID string) map[string]any {
	orchestration := map[string]any{
		"sourceTool":       toolName,
		"subagentType":     subagentType,
		"toolPolicy":       toolPolicy,
		"runInBackground":  boolValue(input["run_in_background"], false),
		"coordinatorReady": true,
	}
	if strings.TrimSpace(name) != "" {
		orchestration["agentName"] = name
	}
	if teamName := strings.TrimSpace(firstNonEmpty(stringValue(input["team_name"]), stringValue(input["teamName"]))); teamName != "" {
		orchestration["teamName"] = teamName
	}
	if mode := strings.TrimSpace(stringValue(input["mode"])); mode != "" {
		orchestration["mode"] = mode
	}
	if strings.TrimSpace(model) != "" {
		orchestration["model"] = model
		orchestration["delegate_model"] = model
	}
	if strings.TrimSpace(parentSessionID) != "" {
		orchestration["parentSessionId"] = parentSessionID
	}
	if parentAllowedTools := stringArrayValue(input["_parent_allowed_tools"]); len(parentAllowedTools) > 0 {
		orchestration["parentAllowedTools"] = parentAllowedTools
	}
	return orchestration
}

func (s *Server) broadcastSendMessage(messageText string, summary string) (any, error) {
	if messageText == "" {
		return nil, errors.New("structured messages cannot be broadcast")
	}
	runs, err := s.taskRunStore.List()
	if err != nil {
		return nil, err
	}
	recipients := []string{}
	for _, run := range runs {
		if !isAgentDelegationTaskRun(run) || isTerminalTaskRunStatus(run.Status) {
			continue
		}
		resumed, err := s.taskRunStore.Resume(run.RunID, messageText)
		if err != nil {
			return nil, err
		}
		if _, err := s.queueTaskRunSession(resumed, messageText); err != nil {
			return nil, err
		}
		recipients = append(recipients, firstNonEmpty(stringValue(run.Orchestration["agentName"]), run.SessionID))
	}
	return map[string]any{
		"success":    true,
		"message":    fmt.Sprintf("Broadcast queued for %d agent(s).", len(recipients)),
		"recipients": recipients,
		"routing": map[string]any{
			"sender":  "synon-go",
			"target":  "*",
			"summary": summary,
			"content": messageText,
		},
	}, nil
}

func sendMessagePayload(value any) (map[string]any, string, error) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, "", errors.New("SendMessage.message must not be empty")
		}
		return map[string]any{"type": "message", "content": text}, text, nil
	case map[string]any:
		if typed == nil {
			return nil, "", errors.New("SendMessage.message must not be null")
		}
		copied := map[string]any{}
		for key, item := range typed {
			copied[key] = item
		}
		if _, ok := copied["type"].(string); !ok {
			return nil, "", errors.New("SendMessage.message.type is required for structured messages")
		}
		return copied, "", nil
	default:
		return nil, "", errors.New("SendMessage.message must be a string or object")
	}
}
