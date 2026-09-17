package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

func (s *Server) settleSessionRunnerChatOutcome(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	chatRun *sessionRunnerChatRun,
	session sessionstore.Session,
	entries []eventjournal.Entry,
	assistantMessage string,
	chatErr error,
	claimToken string,
) error {
	var err error
	if chatRun.AfterEventID > result.CheckpointEventID {
		result.CheckpointEventID = chatRun.AfterEventID
	}
	status := "completed"
	message := "runner chat completed"
	if cause := context.Cause(ctx); errors.Is(cause, ErrGenerationStopped) {
		status = "cancelled"
		message = cause.Error()
		assistantMessage = ""
	} else if errors.Is(chatErr, context.Canceled) {
		status = "cancelled"
		message = chatErr.Error()
		assistantMessage = ""
	} else if chatErr != nil {
		status = "failed"
		message = chatErr.Error()
	}
	if status == "failed" {
		log.Printf(
			"session runner chat failed session=%q attempt=%d err_type=%T reason=%q err=%v",
			session.ID, result.Attempt, chatErr, sessionRunnerFailureReasonCode(message), chatErr,
		)
	}
	hookCtx := ctx
	stopHook := func() {}
	if status == "cancelled" {
		hookCtx, stopHook = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	}
	stopHookErr := s.runSessionRunnerStopHooks(hookCtx, options, session, chatRun, status, message, assistantMessage)
	stopHook()
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); drained {
		if drainErr != nil {
			return drainErr
		}
		return nil
	}
	if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailure(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); interrupted {
		if interruptionErr != nil {
			return interruptionErr
		}
		return nil
	}
	if stopHookErr != nil {
		if status == "cancelled" {
			message += "; stop hook: " + stopHookErr.Error()
		} else {
			status = "failed"
			message = stopHookErr.Error()
		}
	}
	if status == "completed" || status == "cancelled" {
		if cleanupErr := s.releaseSessionRunnerKernels(ctx, session.ID, transcriptAuthority); cleanupErr != nil {
			status = "failed"
			message = "task runtime cleanup failed: " + cleanupErr.Error()
			assistantMessage = ""
		}
	}
	activeRun.settlement.Lock()
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); drained {
		activeRun.settlement.Unlock()
		if drainErr != nil {
			return drainErr
		}
		return nil
	}
	if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailureLocked(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); interrupted {
		activeRun.settlement.Unlock()
		if interruptionErr != nil {
			return interruptionErr
		}
		return nil
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrGenerationStopped) || errors.Is(cause, context.Canceled) {
		status = "cancelled"
		message = cause.Error()
		assistantMessage = ""
	}
	if status == "failed" {
		if reason := sessionRunnerFailureReasonCode(message); reason != "" &&
			runnerInterruptionMayContinueSameTask(reason) {
			resumeDetail := message
			if reason == sessionRunnerVisualMediaUnsupportedReasonCode {
				resumeDetail = "the selected model endpoint rejected model-visible image input; no text-only fallback was used. For a generated labeled plot or diagram, export the renderer-derived synon.visual-layout.v1 manifest and call VisualReview action=validate_layout. For semantic image interpretation, use an explicitly configured vision-capable model; do not claim inspection from metadata"
			}
			autoResume := runnerInterruptionAutoResume(reason) ||
				(reason == sessionRunnerModelProviderUnavailableReasonCode &&
					s.sessionRunnerModelSelectionAdvancedSinceFailure(session.ID, chatErr))
			interruptErr := s.interruptClaimedSessionRunnerLockedWithPolicy(
				context.WithoutCancel(ctx), options, result, activeRun, projectionClaim,
				transcriptAuthority, reason, autoResume, resumeDetail,
			)
			activeRun.settlement.Unlock()
			if interruptErr != nil {
				return interruptErr
			}
			return nil
		}
	}
	if transcriptAuthority != nil {
		if assistantMessage != "" {
			if err := s.appendTranscriptAssistantEvent(context.WithoutCancel(ctx), transcriptAuthority, "assistant-message", map[string]any{
				"text":              assistantMessage,
				"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(chatRun.assistantSegmentOrdinal(), ""),
			}); err != nil {
				status = "failed"
				message = fmt.Sprintf("append transcript assistant message failed: %v", err)
				assistantMessage = ""
			}
		}
		if status == "failed" {
			failureLanguage := ""
			if chatRun != nil {
				failureLanguage = chatRun.ResponseLanguage
			}
			message = publicSessionRunnerFailureMessage(message, failureLanguage)
		}
		terminalEvent, finishTranscriptErr := s.finishTranscriptRunner(
			context.WithoutCancel(ctx), transcriptAuthority, status, message, chatRun.assistantSegmentOrdinal(),
		)
		if finishTranscriptErr == nil {
			activeRun.settled = true
		}
		activeRun.settlement.Unlock()
		if finishTranscriptErr != nil {
			return finishTranscriptErr
		}
		result.FinishEventID = terminalEvent.EventID
		if status == "completed" && assistantMessage != "" && s.workspaceStore != nil {
			if asideFrame, found, getErr := s.workspaceStore.GetCompatibilityFrame(transcriptAuthority.Stream.FrameID); getErr == nil &&
				found && strings.HasPrefix(asideFrame.Name, "Aside") {
				// v1.1 side chats surface the final model reply through
				// output_data.response; persist it when the aside frame
				// completes so side-chat readers can consume the answer.
				if outputErr := s.workspaceStore.SetFrameOutputData(asideFrame.ID, map[string]any{
					"response": assistantMessage,
				}); outputErr != nil {
					return outputErr
				}
			}
		}
	} else {
		if status == "failed" {
			failureLanguage := ""
			if chatRun != nil {
				failureLanguage = chatRun.ResponseLanguage
			}
			message = publicSessionRunnerFailureMessage(message, failureLanguage)
		}
		var assistant *eventjournal.Entry
		if assistantMessage != "" {
			assistant, err = s.appendSessionToolEvent(map[string]any{
				"sessionId":       session.ID,
				"role":            "assistant",
				"runnerId":        options.RunnerID,
				"runnerAttempt":   result.Attempt,
				"claimToken":      claimToken,
				"message":         map[string]any{"type": "message", "text": assistantMessage},
				"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "chat-assistant"),
			})
			if err != nil {
				status = "failed"
				message = fmt.Sprintf("append assistant session projection failed: %v", err)
				assistantMessage = ""
				assistant = nil
			} else if assistant != nil {
				result.AssistantEventID = assistant.EventID
				// The durable journal orders the complete assistant message before
				// runner_finished. Preserve that same order for live consumers so the
				// final message can replace the provisional stream tail before the
				// terminal event closes the generation.
				s.publishSessionEntry(assistant)
			}
		}
		finished, err := s.finishSessionRunner(map[string]any{
			"sessionId":       session.ID,
			"runnerId":        options.RunnerID,
			"runnerAttempt":   result.Attempt,
			"claimToken":      claimToken,
			"status":          status,
			"message":         message,
			"afterEventId":    maxInt64(result.CheckpointEventID, result.AssistantEventID),
			"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "chat-finish"),
		})
		if err == nil {
			activeRun.settled = true
		}
		activeRun.settlement.Unlock()
		if err != nil {
			return err
		}
		if event, ok := finished["event"].(*eventjournal.Entry); ok && event != nil {
			result.FinishEventID = event.EventID
		}
	}
	result.Status = status
	if status == "completed" && assistantMessage != "" && s.memoryExtraction != nil && result.FinishEventID > 0 {
		s.memoryExtraction.ScheduleCompletedRootEvent(session.ID, result.FinishEventID)
	}
	if status == "completed" && transcriptAuthority == nil {
		advanced, err := s.autoAdvanceTaskRunSession(session.ID, "TaskRun auto-advanced after runner chat completion.")
		if err != nil {
			return err
		}
		result.AutoAdvanced = advanced
	}
	if err := markSessionRunnerTerminal(chatRun); err != nil {
		return err
	}
	return nil
}
