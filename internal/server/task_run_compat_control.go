package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	taskruns "synon-go/internal/persistence/taskruns"
	"synon-go/internal/synonlink"
	"time"
)

func (s *Server) executeTaskRunTool(input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("TaskRun", input); err != nil {
		return nil, err
	}
	if _, present := input["playbook"]; present {
		return nil, errors.New("TaskRun.playbook is retired; provide an explicit task_graph produced by generate_plan")
	}
	if _, present := input["playbook_options"]; present {
		return nil, errors.New("TaskRun.playbook_options is retired; provide an explicit task_graph produced by generate_plan")
	}
	if s.taskRunStore == nil {
		return nil, errors.New("TaskRun store is not configured")
	}
	parsed, err := taskRunInput(input)
	if err != nil {
		return nil, err
	}
	action := strings.TrimSpace(parsed.Action)
	if action == "" {
		action = "start"
	}
	switch action {
	case "list":
		runs, err := s.taskRunStore.List()
		if err != nil {
			return nil, err
		}
		return map[string]any{"runs": runs}, nil
	case "plan":
		run, err := s.taskRunStore.Create(parsed)
		if err != nil {
			return nil, err
		}
		if err := s.syncTaskRunGoalLedger(run); err != nil {
			return nil, err
		}
		return run, nil
	case "start":
		run, err := s.taskRunStore.Create(parsed)
		if err != nil {
			return nil, err
		}
		if executed, completed, err := s.executeTaskRunSystemSteps(run); err != nil {
			return nil, err
		} else if executed {
			if err := s.syncTaskRunGoalLedger(completed); err != nil {
				return nil, err
			}
			return completed, nil
		}
		return s.dispatchTaskRunExternalOrQueue(run, "TaskRun started")
	case "status", "reconcile":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=" + action + " requires run_id")
		}
		return s.reconcileTaskRun(parsed.RunID)
	case "resume":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=resume requires run_id")
		}
		run, err := s.taskRunStore.Resume(parsed.RunID, parsed.Message)
		if err != nil {
			return nil, err
		}
		return s.dispatchTaskRunExternalOrQueue(run, firstNonEmpty(parsed.Message, "TaskRun resumed"))
	case "advance", "continue", "next":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=" + action + " requires run_id")
		}
		return s.advanceTaskRun(parsed.RunID, parsed.Message)
	case "verify":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=verify requires run_id")
		}
		if _, err := s.reconcileTaskRun(parsed.RunID); err != nil {
			return nil, err
		}
		run, err := s.taskRunStore.Verify(parsed.RunID)
		if err != nil {
			return nil, err
		}
		if err := s.syncTaskRunGoalLedger(run); err != nil {
			return nil, err
		}
		return run, nil
	case "monitor", "tick":
		return s.monitorTaskRuns(parsed.RunID)
	case "auto_repair", "repair":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=" + action + " requires run_id")
		}
		run, _, err := s.autoRepairTaskRun(parsed.RunID, parsed.Message)
		return run, err
	case "cancel":
		if strings.TrimSpace(parsed.RunID) == "" {
			return nil, errors.New("TaskRun action=cancel requires run_id")
		}
		run, err := s.taskRunStore.Cancel(parsed.RunID)
		if err != nil {
			return nil, err
		}
		_ = s.appendTaskRunSystemEvent(run, "TaskRun cancelled")
		if err := s.syncTaskRunGoalLedger(run); err != nil {
			return nil, err
		}
		return run, nil
	default:
		return nil, fmt.Errorf("unsupported TaskRun action: %s", action)
	}
}

func (s *Server) dispatchTaskRunExternalOrQueue(run taskruns.Record, message string) (taskruns.Record, error) {
	if blocked, handled, err := s.blockTaskRunMissingSynonLinkClient(run); err != nil {
		return taskruns.Record{}, err
	} else if handled {
		if err := s.syncTaskRunGoalLedger(blocked); err != nil {
			return taskruns.Record{}, err
		}
		return blocked, nil
	}
	if executed, completed, err := s.executeTaskRunSynonLinkSteps(run); err != nil {
		return taskruns.Record{}, err
	} else if executed {
		if err := s.syncTaskRunGoalLedger(completed); err != nil {
			return taskruns.Record{}, err
		}
		return completed, nil
	}
	return s.queueTaskRunSession(run, message)
}

func (s *Server) blockTaskRunMissingSynonLinkClient(run taskruns.Record) (taskruns.Record, bool, error) {
	if s == nil || s.taskRunStore == nil {
		return taskruns.Record{}, false, nil
	}
	stepIndex := -1
	clientKind := ""
	for index, step := range run.Steps {
		if step.Status != "pending" && step.Status != "running" {
			continue
		}
		executor := mapValue(step.Executor)
		kind := strings.TrimSpace(stringValue(executor["kind"]))
		if kind == "synonlink-browser" {
			stepIndex = index
			clientKind = kind
		}
		break
	}
	if stepIndex < 0 {
		return taskruns.Record{}, false, nil
	}
	gateKind, gateMessage, missingCapabilities, blocked := s.taskRunSynonLinkGateIssue(run.Steps[stepIndex])
	if !blocked {
		return taskruns.Record{}, false, nil
	}
	updated, err := s.taskRunStore.Update(run.RunID, func(record taskruns.Record) (taskruns.Record, error) {
		now := time.Now().UTC().UnixMilli()
		if stepIndex >= len(record.Steps) {
			return record, nil
		}
		step := &record.Steps[stepIndex]
		executor := mapValue(step.Executor)
		action := strings.TrimSpace(stringValue(executor["action"]))
		message := gateMessage
		blocker := taskruns.Blocker{Kind: gateKind, Message: message, StepID: step.ID, ClientKind: clientKind, MissingCapabilities: missingCapabilities}
		gap := taskruns.CapabilityGap{Kind: gateKind, Message: message, StepID: step.ID, ClientKind: clientKind, MissingCapabilities: missingCapabilities}
		step.Status = "blocked"
		step.Blocker = &blocker
		step.ChildTaskID = ""
		step.OutputPath = ""
		step.UpdatedAt = now
		record.Status = "blocked"
		record.ActiveChildren = []taskruns.ActiveChild{}
		record.Blockers = append(record.Blockers, blocker)
		record.CapabilityGaps = append(record.CapabilityGaps, gap)
		record.NextActions = []string{"Connect or upgrade the Synon Link browser extension and retry TaskRun action=resume after it advertises the action and capabilities."}
		record.Completion = taskruns.Completion{State: "waiting_user", Verified: false, Confidence: "high", Reason: message, NextAction: "Connect or upgrade Synon Link, then resume the TaskRun.", UpdatedAt: now}
		record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "waiting_for_synonlink_client", StepID: step.ID, Message: message, Metadata: map[string]any{"clientKind": clientKind, "action": action}})
		record.UpdatedAt = now
		return record, nil
	})
	if err != nil {
		return taskruns.Record{}, true, err
	}
	return updated, true, nil
}

func (s *Server) taskRunSynonLinkGateIssue(step taskruns.Step) (string, string, []string, bool) {
	executor := mapValue(step.Executor)
	kind := strings.TrimSpace(stringValue(executor["kind"]))
	action := strings.TrimSpace(stringValue(executor["action"]))
	input := objectMapValue(executor["input"])
	userID := firstNonEmpty(stringValue(input["userId"]), stringValue(input["user_id"]))
	clientID := firstNonEmpty(stringValue(input["clientId"]), stringValue(input["client_id"]))
	clients := s.synonLink.ListAllClients()
	if len(clients) == 0 {
		message := "TaskRun requires a connected " + kind + " client before executing Synon Link action"
		if action != "" {
			message += ": " + action
		}
		return "needsClient", message, nil, true
	}
	if userID == "" || clientID == "" {
		if _, ok := s.selectTaskRunSynonLinkClient(kind, action); ok {
			return "", "", nil, false
		}
		return "missingCapability", "No connected " + kind + " client advertises Synon Link action " + action + ".", []string{"action:" + action}, true
	}
	for _, client := range clients {
		if client.ID != clientID || client.UserID != userID {
			continue
		}
		if client.Status != "" && client.Status != "online" {
			return "needsClient", "TaskRun requires online Synon Link client " + clientID + " before executing action " + action + ".", nil, true
		}
		wantKind := strings.TrimPrefix(kind, "synonlink-")
		if wantKind != "" && client.Kind != wantKind {
			return "missingCapability", "Synon Link client " + clientID + " is " + client.Kind + " but action " + action + " requires " + wantKind + ".", []string{"clientKind:" + wantKind}, true
		}
		if err := synonlink.ValidateCommandPolicyWithDefaults(client, action, taskRunSynonLinkPayload(input), s.synonLink.PolicyDefaults()); err != nil {
			return "missingCapability", err.Error(), taskRunSynonLinkMissingCapabilities(err.Error(), action), true
		}
		return "", "", nil, false
	}
	return "needsClient", "TaskRun requires connected Synon Link client " + clientID + " for user " + userID + ".", nil, true
}

func taskRunSynonLinkMissingCapabilities(message string, action string) []string {
	if marker := "missing capability "; strings.Contains(message, marker) {
		after := strings.TrimSpace(message[strings.Index(message, marker)+len(marker):])
		if fields := strings.Fields(after); len(fields) > 0 {
			return []string{fields[0]}
		}
	}
	if strings.Contains(message, "does not support action") && action != "" {
		return []string{"action:" + action}
	}
	return nil
}

func (s *Server) executeTaskRunSynonLinkSteps(run taskruns.Record) (bool, taskruns.Record, error) {
	if s == nil || s.taskRunStore == nil || s.synonLink == nil || len(run.Steps) == 0 {
		return false, taskruns.Record{}, nil
	}
	runnable := make([]taskruns.Step, 0, len(run.Steps))
	for _, step := range run.Steps {
		if step.Status != "pending" && step.Status != "running" {
			continue
		}
		executor := mapValue(step.Executor)
		kind := strings.TrimSpace(stringValue(executor["kind"]))
		if kind != "synonlink-browser" {
			return false, taskruns.Record{}, nil
		}
		runnable = append(runnable, step)
	}
	if len(runnable) == 0 {
		return false, taskruns.Record{}, nil
	}
	type stepResult struct {
		stepID   string
		artifact taskruns.Artifact
		evidence taskruns.Evidence
	}
	results := make([]stepResult, 0, len(runnable))
	for _, step := range runnable {
		artifact, evidence, err := s.executeTaskRunSynonLinkStep(run, step, time.Now().UTC().UnixMilli())
		if err != nil {
			failed, updateErr := s.failTaskRunDirectStep(run.RunID, step.ID, "synonlink_step_failed", err.Error())
			if updateErr != nil {
				return true, taskruns.Record{}, updateErr
			}
			return true, failed, nil
		}
		results = append(results, stepResult{stepID: step.ID, artifact: artifact, evidence: evidence})
	}
	updated, err := s.taskRunStore.Update(run.RunID, func(record taskruns.Record) (taskruns.Record, error) {
		now := time.Now().UTC().UnixMilli()
		record.ActiveChildren = []taskruns.ActiveChild{}
		for _, result := range results {
			for i := range record.Steps {
				step := &record.Steps[i]
				if step.ID != result.stepID {
					continue
				}
				step.Status = "completed"
				step.CompletedAt = now
				step.UpdatedAt = now
				step.OutputPath = result.artifact.Path
				step.ChildTaskID = ""
				step.Artifacts = append(step.Artifacts, result.artifact)
				record.Artifacts = append(record.Artifacts, result.artifact)
				record.EvidenceIndex = append(record.EvidenceIndex, result.evidence)
				record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "synonlink_step_completed", StepID: step.ID, Message: "TaskRun SynonLink executor completed through connected bridge client.", Metadata: map[string]any{"artifact_path": result.artifact.Path, "artifact_kind": result.artifact.Kind}})
			}
		}
		record.Status = "completed"
		record.NextActions = []string{"TaskRun SynonLink direct executor completed; verification evidence is recorded."}
		record.Completion = taskruns.Completion{State: "pending_self_check", Verified: false, Confidence: "medium", Reason: "SynonLink direct executor completed; final verification is pending.", NextAction: "Call TaskRun action=verify.", UpdatedAt: now}
		for i := range record.Acceptance {
			record.Acceptance[i].Status = "passed"
			record.Acceptance[i].UpdatedAt = now
			if record.Acceptance[i].EvidencePath == "" && len(record.Artifacts) > 0 {
				record.Acceptance[i].EvidencePath = record.Artifacts[0].Path
			}
		}
		record.UpdatedAt = now
		record.History = append(record.History, taskruns.HistoryEvent{At: now, Event: "synonlink_steps_completed", Message: "TaskRun SynonLink executor completed."})
		return record, nil
	})
	if err != nil {
		return true, taskruns.Record{}, err
	}
	verified, err := s.taskRunStore.Verify(updated.RunID)
	return true, verified, err
}

func (s *Server) executeTaskRunSynonLinkStep(record taskruns.Record, step taskruns.Step, now int64) (taskruns.Artifact, taskruns.Evidence, error) {
	executor := mapValue(step.Executor)
	kind := strings.TrimSpace(stringValue(executor["kind"]))
	action := strings.TrimSpace(stringValue(executor["action"]))
	if action == "" {
		return taskruns.Artifact{}, taskruns.Evidence{}, errors.New("SynonLink TaskRun executor requires action")
	}
	input := objectMapValue(executor["input"])
	userID := firstNonEmpty(stringValue(input["userId"]), stringValue(input["user_id"]))
	clientID := firstNonEmpty(stringValue(input["clientId"]), stringValue(input["client_id"]))
	if userID == "" || clientID == "" {
		selected, ok := s.selectTaskRunSynonLinkClient(kind, action)
		if !ok {
			return taskruns.Artifact{}, taskruns.Evidence{}, fmt.Errorf("TaskRun requires a connected %s client that supports %s", kind, action)
		}
		userID = firstNonEmpty(userID, selected.UserID)
		clientID = firstNonEmpty(clientID, selected.ID)
	}
	payload := taskRunSynonLinkPayload(input)
	timeout := time.Duration(numberValue(input["timeout_ms"])) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(numberValue(input["timeoutSeconds"])) * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command, resultCh, err := s.synonLink.SendCommand(ctx, userID, clientID, action, payload)
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	var result synonlink.CommandResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		return taskruns.Artifact{}, taskruns.Evidence{}, fmt.Errorf("SynonLink TaskRun command %s timed out: %w", command.ID, ctx.Err())
	}
	if !result.OK {
		return taskruns.Artifact{}, taskruns.Evidence{}, fmt.Errorf("SynonLink TaskRun command %s failed: %s", command.ID, result.Error)
	}
	body, err := json.MarshalIndent(map[string]any{
		"executor":  kind,
		"action":    action,
		"userId":    userID,
		"clientId":  clientID,
		"commandId": command.ID,
		"payload":   payload,
		"result":    result.Value,
	}, "", "  ")
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	dir, err := s.storageDirectory("taskArtifacts", record.RunID)
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	path := filepath.Join(dir, safeTaskRunArtifactName(step.ID, kind)+".json")
	if err := ensurePathWithinRoot(s.fileRoot, path, "taskrun synonlink artifact"); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	artifact := taskruns.Artifact{StepID: step.ID, Kind: kind, Path: path, Description: "TaskRun SynonLink bridge result.", Metadata: map[string]any{"action": action, "clientId": clientID, "commandId": command.ID, "bytes": len(body)}}
	evidence := taskruns.Evidence{ID: fmt.Sprintf("evidence-%d-%s", now, step.ID), StepID: step.ID, Kind: kind, Path: path, Summary: "TaskRun executed SynonLink action " + action + " through client " + clientID + ".", ProducedAt: now, Metadata: map[string]any{"action": action, "clientId": clientID, "commandId": command.ID}}
	return artifact, evidence, nil
}

func (s *Server) selectTaskRunSynonLinkClient(kind string, action string) (synonlink.Client, bool) {
	wantKind := strings.TrimPrefix(kind, "synonlink-")
	for _, client := range s.synonLink.ListAllClients() {
		if client.Status != "" && client.Status != "online" {
			continue
		}
		if wantKind != "" && client.Kind != wantKind {
			continue
		}
		if err := synonlink.ValidateCommandPolicyWithDefaults(client, action, map[string]any{}, s.synonLink.PolicyDefaults()); err == nil {
			return client, true
		}
	}
	return synonlink.Client{}, false
}

func taskRunSynonLinkPayload(input map[string]any) map[string]any {
	payload := make(map[string]any, len(input))
	for key, value := range input {
		switch key {
		case "userId", "user_id", "clientId", "client_id", "timeout_ms", "timeoutSeconds":
			continue
		default:
			payload[key] = value
		}
	}
	return payload
}

func (s *Server) failTaskRunDirectStep(runID string, stepID string, kind string, message string) (taskruns.Record, error) {
	return s.taskRunStore.Update(runID, func(record taskruns.Record) (taskruns.Record, error) {
		now := time.Now().UTC().UnixMilli()
		blocker := taskruns.Blocker{Kind: kind, Message: message, StepID: stepID}
		for i := range record.Steps {
			if record.Steps[i].ID != stepID {
				continue
			}
			record.Steps[i].Status = "failed"
			record.Steps[i].Error = message
			record.Steps[i].Blocker = &blocker
			record.Steps[i].UpdatedAt = now
		}
		record.Status = "failed"
		record.ActiveChildren = []taskruns.ActiveChild{}
		record.Blockers = append(record.Blockers, blocker)
		record.NextActions = []string{"Inspect the failed direct TaskRun executor step and resume after fixing the bridge/tool input."}
		record.Completion = taskruns.Completion{State: "needs_attention", Verified: false, Confidence: "high", Reason: message, UpdatedAt: now}
		record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "direct_step_failed", StepID: stepID, Message: message, Metadata: map[string]any{"kind": kind}})
		record.UpdatedAt = now
		return record, nil
	})
}
