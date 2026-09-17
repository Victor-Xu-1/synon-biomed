package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) ensureKernelChildSession(child workspace.KernelSupervisedChild) error {
	if s == nil || s.sessionStore == nil || s.eventJournal == nil {
		return errors.New("child session runtime is unavailable")
	}
	project, found, err := s.workspaceStore.GetProject(child.ProjectID)
	if err != nil || !found {
		return errors.New("child project is unavailable")
	}
	now := time.Now().UTC()
	session, found, err := s.sessionStore.Get(child.FrameID)
	if err != nil {
		return err
	}
	if !found {
		session = sessionstore.Session{ID: child.FrameID, CreatedAt: child.StartedAt}
	}
	session.Title = child.Name
	if strings.TrimSpace(session.Title) == "" {
		session.Title = child.AgentName
	}
	session.WorkDir = project.Path
	if strings.TrimSpace(session.WorkDir) == "" {
		session.WorkDir = s.fileRoot
	}
	session.UpdatedAt = now
	session.LastRole = "user"
	session.LastUserMessageAt = now
	session.MessageCount = 1
	session.Project = &sessionstore.Project{ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: now}
	session.Orchestration = map[string]any{
		"frame_id": child.FrameID, "root_frame_id": child.RootFrameID,
		"parent_frame_id": child.ParentFrameID, "delegate": true,
		"delegate_model": child.Model, "output_schema": child.OutputSchema,
		"sessionConfig": map[string]any{"agentName": child.AgentName, "model": child.Model},
	}
	if isSessionRunnerTerminal(session.Runner) {
		session.Runner = nil
	}
	if err := s.sessionStore.Save(session); err != nil {
		return err
	}
	clientID := "kernel-delegate-task:" + child.ToolUseID + ":" + child.FrameID
	has, err := s.eventJournal.HasClientMessage(child.FrameID, clientID)
	if err != nil {
		return err
	}
	if !has {
		text := child.Task
		if strings.TrimSpace(child.ContextSummary) != "" {
			text = "Context:\n" + child.ContextSummary + "\n\nTask:\n" + child.Task
		}
		if len(child.OutputSchema) > 0 {
			rawSchema, _ := json.Marshal(child.OutputSchema)
			text += "\n\nSubmit a compact result matching this JSON Schema with submit_output before finishing:\n" + string(rawSchema)
		}
		if _, err := s.eventJournal.Append(child.FrameID, eventjournal.Message{
			"type": "message", "role": "user", "text": text, "parentFrameId": child.ParentFrameID,
		}, eventjournal.Metadata{ClientMessageID: clientID}); err != nil {
			return err
		}
	}
	return nil
}

func isSessionRunnerTerminal(runner *sessionstore.Runner) bool {
	return runner != nil && (runner.Status == "completed" || runner.Status == "failed" || runner.Status == "cancelled")
}

func (s *Server) startKernelChildRun(child workspace.KernelSupervisedChild, gates ...chan struct{}) {
	key := kernelChildRunKey{server: s, frameID: child.FrameID}
	ctx, cancel := context.WithCancel(context.Background())
	run := &kernelChildRun{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	if existing, loaded := kernelChildRuns.LoadOrStore(key, run); loaded {
		cancel()
		existing.(*kernelChildRun).signal()
		return
	}
	_ = s.workspaceStore.MarkKernelChildDispatched(context.Background(), child.FrameID, child.OwnerUserID)
	go func() {
		defer close(run.done)
		defer cancel()
		if len(gates) > 0 && gates[0] != nil {
			select {
			case gates[0] <- struct{}{}:
				defer func() { <-gates[0] }()
			case <-ctx.Done():
				_, _ = s.workspaceStore.CompleteKernelSupervisedChild(context.Background(), child.FrameID, child.OwnerUserID, "cancelled", nil, "queued child run was cancelled")
				return
			}
		}
		s.runKernelChildSupervisor(ctx, child, run)
		kernelChildRuns.CompareAndDelete(key, run)
		if ctx.Err() == nil {
			if pending, _ := s.workspaceStore.KernelChildHasPendingMessages(context.Background(), child.FrameID, child.OwnerUserID); pending {
				if reopened, err := s.workspaceStore.ReopenKernelSupervisedChild(context.Background(), child.FrameID, child.OwnerUserID); err == nil {
					s.startKernelChildRun(reopened)
				}
			}
		}
	}()
}

func (s *Server) cancelAllKernelChildRuns(ctx context.Context) error {
	var joinErr error
	kernelChildRuns.Range(func(_, value any) bool {
		run := value.(*kernelChildRun)
		run.cancel()
		select {
		case <-run.done:
		case <-ctx.Done():
			joinErr = errors.Join(joinErr, ctx.Err())
			return false
		}
		return true
	})
	return joinErr
}

func (s *Server) runKernelChildSupervisor(ctx context.Context, child workspace.KernelSupervisedChild, run *kernelChildRun) {
	runnerID := "kernel-child-" + child.FrameID
	for {
		pending, err := s.workspaceStore.ListPendingKernelChildMessages(context.Background(), child.FrameID, child.OwnerUserID)
		if err != nil {
			s.settleKernelChildRun(child, "failed", nil, err.Error())
			return
		}
		if err := s.materializeKernelChildMessages(child, pending); err != nil {
			s.settleKernelChildRun(child, "failed", nil, err.Error())
			return
		}
		result, runErr := s.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
			SessionID: child.FrameID, RunnerID: runnerID,
			RequestTimeout: defaultSessionRunnerChatRequestTimeout,
			LeaseTTL:       defaultSessionRunnerLeaseTTL,
		})

		if result.Claimed {
			for _, message := range pending {
				_ = s.workspaceStore.MarkKernelChildMessageConsumed(context.Background(), child.FrameID, child.OwnerUserID, message.ID, message.Generation)
			}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			s.settleKernelChildRun(child, "cancelled", nil, "child run was cancelled")
			return
		}
		if hasPending, _ := s.workspaceStore.KernelChildHasPendingMessages(context.Background(), child.FrameID, child.OwnerUserID); hasPending {
			_, _ = s.workspaceStore.ReopenKernelSupervisedChild(context.Background(), child.FrameID, child.OwnerUserID)
			continue
		}
		if !result.Claimed && runErr == nil {
			select {
			case <-ctx.Done():
				continue
			case <-run.wake:
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		status, failure := "completed", ""
		if runErr != nil {
			status, failure = "failed", runErr.Error()
		} else if result.Status != "completed" {
			status = strings.TrimSpace(result.Status)
			if status == "" {
				status = "failed"
			}
			if status != "awaiting_user_response" {
				failure = "child runner finished with status " + status
				if latest, found, sessionErr := s.sessionStore.Get(child.FrameID); sessionErr == nil && found && latest.Runner != nil {
					if checkpoint := strings.TrimSpace(latest.Runner.LastCheckpoint); checkpoint != "" {
						failure = checkpoint
					}
				}
			}
		}
		if status == "completed" {
			if waiting, detectErr := s.kernelChildAwaitingUserResponse(child.FrameID); detectErr != nil {
				status, failure = "failed", detectErr.Error()
			} else if waiting {
				status = "awaiting_user_response"
			}
		}
		output, projectionErr := s.projectKernelChildOutput(child)
		if projectionErr != nil && failure == "" {
			status, failure = "failed", projectionErr.Error()
		}
		settled := s.settleKernelChildRun(child, status, output, failure)
		if settled.Status == "processing" {
			continue
		}
		return
	}
}

func (s *Server) settleKernelChildRun(child workspace.KernelSupervisedChild, status string, output map[string]any, failure string) workspace.KernelSupervisedChild {
	settled, err := s.workspaceStore.CompleteKernelSupervisedChild(context.Background(), child.FrameID, child.OwnerUserID, status, output, failure)
	if err != nil {
		return workspace.KernelSupervisedChild{FrameID: child.FrameID, Status: "failed", Error: err.Error()}
	}
	if kernelChildTerminal(settled.Status) {
		s.appendKernelParentCompletionNotification(settled)
	}
	return settled
}

func (s *Server) materializeKernelChildMessages(child workspace.KernelSupervisedChild, pending []workspace.KernelChildQueuedMessage) error {
	if len(pending) == 0 {
		return nil
	}
	appended := 0
	for _, message := range pending {
		clientID := "kernel-child-message:" + message.ID
		has, err := s.eventJournal.HasClientMessage(child.FrameID, clientID)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.eventJournal.Append(child.FrameID, eventjournal.Message{
			"type": "message", "role": "user", "text": message.Message, "kind": message.Kind,
			"sender_frame_id": message.SenderFrameID, "notification_type": "child_message",
		}, eventjournal.Metadata{ClientMessageID: clientID}); err != nil {
			return err
		}
		appended++
	}
	session, found, err := s.sessionStore.Get(child.FrameID)
	if err != nil {
		return err
	}
	if !found {
		if err := s.ensureKernelChildSession(child); err != nil {
			return err
		}
		session, found, err = s.sessionStore.Get(child.FrameID)
		if err != nil || !found {
			return errors.New("child session disappeared while materializing messages")
		}
	}
	if appended > 0 {
		session.MessageCount += appended
		session.LastRole = "user"
		session.LastUserMessageAt = time.Now().UTC()
	}
	if isSessionRunnerTerminal(session.Runner) {
		session.Runner = nil
	}
	return s.sessionStore.Save(session)
}

func (s *Server) kernelChildAwaitingUserResponse(frameID string) (bool, error) {
	entries, err := s.eventJournal.ReadAll(frameID)
	if err != nil {
		return false, err
	}
	var latestRequest, latestAnswer int64
	for _, entry := range entries {
		message := entry.Message
		if message["notification_type"] == "child_message" && message["role"] == "user" {
			latestAnswer = entry.EventID
		}
		name := stringValue(message["toolName"])
		if _, ok := transcriptstore.CanonicalAskUserToolNameV1(name); ok {
			result, _ := message["toolResult"].(map[string]any)
			answers, _ := result["answers"].(map[string]any)
			if len(answers) == 0 {
				latestRequest = entry.EventID
			}
		}
		if result, ok := message["toolResult"].(map[string]any); ok && stringValue(result["decision"]) == "pending_approval" {
			latestRequest = entry.EventID
		}
	}
	return latestRequest > latestAnswer, nil
}

func (s *Server) projectKernelChildOutput(child workspace.KernelSupervisedChild) (map[string]any, error) {
	entries, err := s.eventJournal.ReadAll(child.FrameID)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	for _, entry := range entries {
		if structured, found := findKernelStructuredOutput(entry.Message); found {
			output["structured_output"] = structured
		}
		if role, _ := entry.Message["role"].(string); role == "assistant" {
			if text := kernelJournalText(entry.Message); strings.TrimSpace(text) != "" {
				output["response"] = text
			}
		}
	}
	if s.runtimeStore != nil {
		structuredEntries, err := s.runtimeStore.List(structuredOutputRuntimeNamespace)
		if err != nil {
			return nil, err
		}
		for _, entry := range structuredEntries {
			record, ok := entry.Value.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(record["sessionId"])) != child.FrameID {
				continue
			}
			if structured, exists := record["structured_output"]; exists && structured != nil {
				output["structured_output"] = structured
			}
		}
	}
	artifacts, omitted, err := s.workspaceStore.ListKernelChildArtifacts(context.Background(), child.FrameID, child.OwnerUserID)
	if err != nil {
		return nil, err
	}
	if len(artifacts) > 0 {
		output["artifacts_created"] = artifacts
	}
	if omitted > 0 {
		output["versions_omitted"] = omitted
	}
	if len(child.OutputSchema) > 0 && output["structured_output"] == nil {
		output["structured_output_unsatisfied"] = true
	}
	return output, nil
}

func findKernelStructuredOutput(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if structured, exists := typed["structured_output"]; exists && structured != nil {
			return structured, true
		}
		for _, child := range typed {
			if structured, found := findKernelStructuredOutput(child); found {
				return structured, true
			}
		}
	case []any:
		for _, child := range typed {
			if structured, found := findKernelStructuredOutput(child); found {
				return structured, true
			}
		}
	}
	return nil, false
}

func kernelJournalText(message map[string]any) string {
	for _, key := range []string{"text", "content", "assistantMessage"} {
		if value, ok := message[key].(string); ok {
			return value
		}
	}
	if nested, ok := message["message"].(map[string]any); ok {
		return kernelJournalText(nested)
	}
	return ""
}

func (s *Server) appendKernelParentCompletionNotification(child workspace.KernelSupervisedChild) {
	if s == nil || s.eventJournal == nil {
		return
	}
	payload := map[string]any{
		"notifications": []any{map[string]any{
			"notification_type": "completion", "sender_frame_id": child.FrameID,
			"payload": map[string]any{
				"status": child.Status, "_completion_bullets": kernelChildCompletionBullets(child),
				"wall_s": kernelChildWallSeconds(child), "child_frame_id": child.FrameID,
			},
		}},
	}
	raw, _ := json.Marshal(payload)
	completionIdentity := child.FrameID
	if child.CompletedAt != nil {
		completionIdentity += ":" + child.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, _ = s.eventJournal.Append(child.ParentFrameID, eventjournal.Message{
		"type": "tool_result", "role": "user", "toolUseId": child.ToolUseID,
		"child_frame_id": child.FrameID, "content": string(raw),
	}, eventjournal.Metadata{ClientMessageID: "kernel-child-completion:" + completionIdentity})
}

func kernelChildCompletionBullets(child workspace.KernelSupervisedChild) []any {
	if child.Error != "" {
		return []any{"Child run " + child.Status, child.Error}
	}
	if response, _ := child.Output["response"].(string); strings.TrimSpace(response) != "" {
		return []any{"Child run completed", truncateKernelSupervisionResult(response, 240)}
	}
	if child.Output["structured_output"] != nil {
		return []any{"Child run completed", "Structured output was submitted"}
	}
	return []any{"Child run completed", "No prose response was recorded"}
}

func kernelChildWallSeconds(child workspace.KernelSupervisedChild) float64 {
	if child.CompletedAt == nil {
		return 0
	}
	seconds := child.CompletedAt.Sub(child.StartedAt).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}
