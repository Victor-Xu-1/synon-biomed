package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	runnermachine "synon-go/internal/sessionrunner"
)

func (s *Server) runSessionRunnerChatOnce(
	ctx context.Context,
	options SessionRunnerChatOptions,
	existingLegacyClaim *sessionstore.RunnerMutationClaim,
) (result SessionRunnerCycleResult, cycleErr error) {
	options = normalizeSessionRunnerChatOptions(options)
	if s == nil {
		return SessionRunnerCycleResult{}, errors.New("session runner is not configured")
	}
	if s.isDraining() {
		return SessionRunnerCycleResult{RunnerID: options.RunnerID}, nil
	}

	var session sessionstore.Session
	var projectionClaim sessionstore.RunnerMutationClaim
	var projectionClaimed bool
	var projectionClaimAcquired bool
	var transcriptFrame bool
	var transcriptStream transcriptstore.Stream
	var transcriptAuthority *transcriptRunnerAuthority
	var err error
	if options.SessionID == "" && s.transcriptStore != nil {
		next, claimErr := s.transcriptStore.ClaimNextRunner(ctx, transcriptstore.ClaimNextRunnerInput{
			RunnerID: options.RunnerID, TTL: options.LeaseTTL,
		})
		if claimErr != nil {
			return SessionRunnerCycleResult{}, claimErr
		}
		if next.Claimed {
			options.SessionID = next.Stream.SessionID
			transcriptFrame = true
			transcriptStream = next.Stream
			transcriptAuthority = &transcriptRunnerAuthority{Stream: next.Stream, Claim: next.Claim}
		}
	}
	if options.SessionID == "" {
		if s.sessionStore == nil || s.eventJournal == nil {
			return SessionRunnerCycleResult{}, errors.New("session store is not configured")
		}
		if _, err := s.recoverCompatibilityQueuedMessages(); err != nil {
			return SessionRunnerCycleResult{}, fmt.Errorf("recover compatibility message queue: %w", err)
		}
	}
	if options.SessionID != "" {
		if !transcriptFrame {
			transcriptStream, transcriptFrame, err = s.resolveTranscriptFrameStream(ctx, options.SessionID)
			if err != nil {
				return SessionRunnerCycleResult{}, err
			}
		}
	}
	if transcriptFrame {
		session, err = s.loadTranscriptFrameSessionProjection(transcriptStream)
		if err != nil {
			if transcriptAuthority != nil {
				owned := SessionRunnerCycleResult{RunnerID: options.RunnerID, SessionID: transcriptStream.SessionID, Claimed: true, Attempt: int(transcriptAuthority.Claim.Attempt)}
				settleErr := s.interruptClaimedSessionRunner(options, &owned, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, transcriptAuthority,
					sessionRunnerSupervisorInterruptedReasonCode, "runner preparation failed after durable admission; resume the same task from its retained state")
				return owned, errors.Join(err, settleErr)
			}
			return SessionRunnerCycleResult{}, err
		}
	} else {
		if s.sessionStore == nil || s.eventJournal == nil {
			return SessionRunnerCycleResult{}, errors.New("session store is not configured")
		}
		if existingLegacyClaim != nil {
			if options.SessionID == "" || strings.TrimSpace(existingLegacyClaim.SessionID) != options.SessionID ||
				strings.TrimSpace(existingLegacyClaim.RunnerID) != options.RunnerID {
				return SessionRunnerCycleResult{}, errors.New("reserved runner claim does not match the requested session")
			}
			session, err = s.sessionStore.ValidateRunnerClaim(*existingLegacyClaim, true)
			if err == nil {
				projectionClaimed = true
				projectionClaim = *existingLegacyClaim
			}
		} else if options.SessionID != "" {
			session, projectionClaimed, err = s.sessionStore.ClaimRunner(options.SessionID, options.RunnerID, options.LeaseTTL)
			projectionClaimAcquired = projectionClaimed
		} else if s.transcriptStore != nil {
			session, projectionClaimed, err = s.claimNextLegacyRunnerExcludingTranscriptFrames(ctx, options.RunnerID, options.LeaseTTL)
			projectionClaimAcquired = projectionClaimed
		} else {
			session, projectionClaimed, err = s.sessionStore.ClaimNextRunner(options.RunnerID, options.LeaseTTL)
			projectionClaimAcquired = projectionClaimed
		}
	}
	if err != nil {
		return SessionRunnerCycleResult{}, err
	}
	if len(options.RuntimeSessionConfig) > 0 {
		session = projectRuntimeSessionConfig(session, options.RuntimeSessionConfig)
	}
	result = SessionRunnerCycleResult{
		RunnerID: options.RunnerID,
	}
	if !transcriptFrame && !projectionClaimed {
		return result, nil
	}
	if projectionClaimed && projectionClaim.ClaimToken == "" {
		projectionClaim = sessionstore.RunnerClaimFromSession(session)
	}
	result.SessionID = session.ID
	// Record an already-owned claim before active-run registration. A runtime
	// drain can begin between the storage claim and the in-memory registration;
	// the shutdown path must then fence the real attempt instead of emitting an
	// invalid attempt-zero legacy checkpoint.
	if transcriptAuthority != nil {
		result.Claimed = true
		result.Attempt = int(transcriptAuthority.Claim.Attempt)
	} else if projectionClaimed && session.Runner != nil {
		result.Claimed = true
		result.Attempt = session.Runner.Attempt
	}
	activeCtx, activeRun, finishActiveRun, err := s.registerActiveSessionRun(ctx, session.ID, options.RunnerID)
	if err != nil {
		if errors.Is(err, ErrRuntimeDraining) {
			if interruptErr := s.settleRunnerRegistrationDuringDrain(
				options, &result, transcriptFrame, projectionClaim, transcriptAuthority,
			); interruptErr != nil {
				return result, interruptErr
			}
			return result, nil
		}
		if transcriptAuthority != nil {
			_, _ = s.finishTranscriptRunner(context.WithoutCancel(ctx), transcriptAuthority, "failed", "active runner registration failed")
		}
		if projectionClaimAcquired {
			_, _, _ = s.sessionStore.ReleaseRunner(projectionClaim)
		}
		return result, err
	}
	defer finishActiveRun()
	ctx = activeCtx
	if transcriptFrame {
		if transcriptAuthority == nil {
			var transcriptClaimed bool
			transcriptAuthority, transcriptClaimed, _, err = s.claimTranscriptFrameRunner(
				ctx, session.ID, options.RunnerID, options.LeaseTTL,
				options.TranscriptResumeSource, options.TranscriptCheckpoint,
			)
			if err != nil {
				return result, err
			}
			if !transcriptClaimed {
				return result, nil
			}
		}
		result.Claimed = true
		result.Attempt = int(transcriptAuthority.Claim.Attempt)
	} else {
		result.Claimed = true
		if session.Runner != nil {
			result.Attempt = session.Runner.Attempt
		}
	}
	claimToken := projectionClaim.ClaimToken
	if transcriptAuthority != nil {
		claimToken = transcriptAuthority.Claim.ClaimToken
	}
	activeRun.settlement.Lock()
	activeRun.transcript = transcriptAuthority
	if transcriptAuthority != nil {
		state, stateErr := s.transcriptStore.GetRunnerRuntimeState(
			context.WithoutCancel(ctx), transcriptAuthority.Stream.UID, transcriptAuthority.Stream.OwnerID, transcriptAuthority.Claim.Attempt,
		)
		if stateErr != nil {
			activeRun.settlement.Unlock()
			settleErr := s.interruptClaimedSessionRunner(options, &result, activeRun, projectionClaim, transcriptAuthority,
				sessionRunnerSupervisorInterruptedReasonCode, "runtime state could not be read after claim; preserve the durable attempt for recovery")
			return result, errors.Join(stateErr, settleErr)
		}
		if state.Status != "running" {
			activeRun.settled = true
			activeRun.settlement.Unlock()
			result.Status = state.Status
			return result, nil
		}
	}
	activeRun.settlement.Unlock()
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
	); drained {
		if drainErr != nil {
			return result, drainErr
		}
		return result, nil
	}
	if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailure(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
	); interrupted {
		if interruptionErr != nil {
			return result, interruptionErr
		}
		return result, nil
	}
	if cause := context.Cause(ctx); cause != nil {
		activeRun.settlement.Lock()
		if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); drained {
			activeRun.settlement.Unlock()
			if drainErr != nil {
				return result, drainErr
			}
			return result, nil
		}
		if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailureLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); interrupted {
			activeRun.settlement.Unlock()
			if interruptionErr != nil {
				return result, interruptionErr
			}
			return result, nil
		}
		message := cause.Error()
		if transcriptAuthority != nil {
			terminalEvent, finishErr := s.finishTranscriptRunner(context.WithoutCancel(ctx), transcriptAuthority, "cancelled", message)
			if finishErr == nil {
				activeRun.settled = true
			}
			activeRun.settlement.Unlock()
			if finishErr != nil {
				return result, finishErr
			}
			result.FinishEventID = terminalEvent.EventID
			result.Status = "cancelled"
			return result, nil
		}
		finished, finishErr := s.finishSessionRunner(map[string]any{
			"sessionId": session.ID, "runnerId": options.RunnerID, "runnerAttempt": result.Attempt,
			"claimToken": claimToken, "status": "cancelled", "message": message,
			"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "chat-finish"),
		})
		if finishErr == nil {
			activeRun.settled = true
		}
		activeRun.settlement.Unlock()
		if finishErr != nil {
			return result, finishErr
		}
		if event, ok := finished["event"].(*eventjournal.Entry); ok && event != nil {
			result.FinishEventID = event.EventID
		}
		result.Status = "cancelled"
		return result, nil
	}
	phaseMachine, phaseErr := newSessionRunnerPhaseMachine()
	if phaseErr != nil {
		return result, phaseErr
	}
	chatRun := &sessionRunnerChatRun{
		SessionID: session.ID, Attempt: result.Attempt, ClaimToken: claimToken, Transcript: transcriptAuthority,
		phaseMachine: phaseMachine,
	}
	activeRun.settlement.Lock()
	activeRun.phaseMachine = phaseMachine
	activeRun.chatRun = chatRun
	activeRun.settlement.Unlock()
	preChatParentCtx := ctx
	// One lease observer owns the complete claimed execution unit, including
	// model/tool discovery. Preparation has its own deadline beneath it.
	runCtx, stopHeartbeat := s.startSessionRunnerChatHeartbeat(
		preChatParentCtx, projectionClaim, transcriptAuthority, options,
	)
	defer s.settleSessionRunnerHeartbeatOnReturn(
		preChatParentCtx, &result, transcriptAuthority, &cycleErr, stopHeartbeat,
	)
	preChatStage := "resume_state"
	preChatCtx, cancelPreChat := context.WithTimeoutCause(
		runCtx, options.PreparationTimeout, errSessionRunnerPreparationDeadline,
	)
	defer cancelPreChat()
	ctx = preChatCtx
	preChatFinished := false
	settlePreChatError := func(preparationErr error) (bool, error) {
		if runCtx.Err() != nil && preChatParentCtx.Err() == nil {
			if handled, heartbeatErr := s.settleSessionRunnerHeartbeatError(
				preChatParentCtx, &result, transcriptAuthority, stopHeartbeat(),
			); handled {
				return true, heartbeatErr
			}
		}
		return s.settleSessionRunnerPreparationError(
			preChatCtx, preparationErr, preChatStage, options.PreparationTimeout,
			options, &result, activeRun, projectionClaim, transcriptAuthority,
		)
	}
	checkpointPreChatStage := func(stage string) error {
		checkpointCtx := preChatCtx
		if preChatFinished {
			checkpointCtx = ctx
		}
		if err := checkpointCtx.Err(); err != nil {
			return err
		}
		preChatStage = stage
		return s.checkpointSessionRunnerPreparationStage(checkpointCtx, chatRun, stage)
	}
	finishPreChat := func() {
		if preChatFinished {
			return
		}
		preChatFinished = true
		cancelPreChat()
		ctx = runCtx
	}
	if err := checkpointPreChatStage(preChatStage); err != nil {
		if handled, settleErr := settlePreChatError(err); handled {
			return result, settleErr
		}
		return result, err
	}
	if err := s.restoreTranscriptAssistantSegmentState(ctx, chatRun); err != nil {
		if handled, settleErr := settlePreChatError(err); handled {
			return result, settleErr
		}
		return result, err
	}
	if transcriptAuthority != nil {
		if err := checkpointPreChatStage("completion_recovery"); err != nil {
			if handled, settleErr := settlePreChatError(err); handled {
				return result, settleErr
			}
			return result, err
		}
		status, detail, segment, recovered, recoveryErr := s.recoverTranscriptCompletionOnly(ctx, session, chatRun)
		if recoveryErr != nil {
			if handled, settleErr := settlePreChatError(recoveryErr); handled {
				return result, settleErr
			}
			return result, recoveryErr
		}
		if recovered {
			if status == "completed" {
				if err := advanceSessionRunnerPhase(chatRun, runnermachine.PhaseVerify); err != nil {
					return result, err
				}
				if err := markSessionRunnerComplete(chatRun); err != nil {
					return result, err
				}
			}
			activeRun.settlement.Lock()
			terminalEvent, finishErr := s.finishTranscriptRunner(
				context.WithoutCancel(ctx), transcriptAuthority, status, detail, segment.Ordinal,
			)
			if finishErr == nil {
				activeRun.settled = true
			}
			activeRun.settlement.Unlock()
			if finishErr != nil {
				return result, finishErr
			}
			result.Status = status
			result.FinishEventID = terminalEvent.EventID
			if err := markSessionRunnerTerminal(chatRun); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	if err := checkpointPreChatStage("agent_authority"); err != nil {
		if handled, settleErr := settlePreChatError(err); handled {
			return result, settleErr
		}
		return result, err
	}
	resolvedAgentOptions, _, agentErr := s.applySessionRunnerAgentModelAuthority(session, options)
	if agentErr != nil {
		if handled, settleErr := settlePreChatError(agentErr); handled {
			return result, settleErr
		}
		resumeDetail := fmt.Sprintf("agent runtime authority resolution failed before provider call: %v", agentErr)
		if err := s.interruptClaimedSessionRunner(
			options, &result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerSelectedSkillContractUnavailableReasonCode, resumeDetail,
		); err != nil {
			return result, err
		}
		return result, nil
	}
	options = resolvedAgentOptions
	if err := checkpointPreChatStage("model_resolution"); err != nil {
		if handled, settleErr := settlePreChatError(err); handled {
			return result, settleErr
		}
		return result, err
	}
	resolvedOptions, resolutionErr := s.resolveSessionRunnerModelAuthority(ctx, session, options, result.Attempt)
	if resolutionErr != nil {
		if handled, settleErr := settlePreChatError(resolutionErr); handled {
			return result, settleErr
		}
		finishPreChat()
		if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); drained {
			if drainErr != nil {
				return result, drainErr
			}
			return result, nil
		}
		if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailure(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); interrupted {
			if interruptionErr != nil {
				return result, interruptionErr
			}
			return result, nil
		}
		status, message := "failed", resolutionErr.Error()
		activeRun.settlement.Lock()
		if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); drained {
			activeRun.settlement.Unlock()
			if drainErr != nil {
				return result, drainErr
			}
			return result, nil
		}
		if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailureLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); interrupted {
			activeRun.settlement.Unlock()
			if interruptionErr != nil {
				return result, interruptionErr
			}
			return result, nil
		}
		// Only the active execution's parent can request user cancellation.
		// The lease observer can independently cancel runCtx at any moment.
		if cause := context.Cause(preChatParentCtx); cause != nil {
			status, message = "cancelled", cause.Error()
		}
		if status == "failed" {
			reasonCode := sessionRunnerFailureReasonCode(message)
			if reasonCode == "" {
				// This branch runs before any provider call and contains only model
				// authority/configuration resolution failures. Preserve the task so
				// the user can repair or switch the selected model in place.
				reasonCode = sessionRunnerModelProviderUnavailableReasonCode
			}
			autoResume := runnerInterruptionAutoResume(reasonCode) ||
				(reasonCode == sessionRunnerModelProviderUnavailableReasonCode &&
					s.sessionRunnerModelSelectionAdvancedSinceFailure(session.ID, resolutionErr))
			resumeDetail := message
			if reasonCode == sessionRunnerModelProviderUnavailableReasonCode {
				resumeDetail = "No active model configuration is available. Configure or select a model, then continue this same task."
				result.AwaitingModelSelection = true
			}
			interruptErr := s.interruptClaimedSessionRunnerLockedWithPolicy(
				context.WithoutCancel(ctx), options, &result, activeRun, projectionClaim,
				transcriptAuthority, reasonCode, autoResume, resumeDetail,
			)
			activeRun.settlement.Unlock()
			if interruptErr != nil {
				return result, interruptErr
			}
			return result, nil
		}
		if transcriptAuthority != nil {
			terminalEvent, finishErr := s.finishTranscriptRunner(context.WithoutCancel(ctx), transcriptAuthority, status, message)
			if finishErr == nil {
				activeRun.settled = true
			}
			activeRun.settlement.Unlock()
			if finishErr != nil {
				return result, finishErr
			}
			result.Status = status
			result.FinishEventID = terminalEvent.EventID
			return result, nil
		}
		if status == "cancelled" {
			finished, err := s.finishSessionRunner(map[string]any{
				"sessionId": session.ID, "runnerId": options.RunnerID, "runnerAttempt": result.Attempt,
				"claimToken": claimToken, "status": status, "message": message,
				"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "chat-finish"),
			})
			if err == nil {
				activeRun.settled = true
			}
			activeRun.settlement.Unlock()
			if err != nil {
				return result, err
			}
			if event, ok := finished["event"].(*eventjournal.Entry); ok && event != nil {
				result.FinishEventID = event.EventID
			}
			result.Status = status
			return result, nil
		}
		finishErr := s.finalizeClaimedSessionRunnerFailure(session.ID, options.RunnerID, result.Attempt, claimToken, message, &result)
		if finishErr == nil {
			activeRun.settled = true
		}
		activeRun.settlement.Unlock()
		if finishErr != nil {
			return result, finishErr
		}
		return result, nil
	}
	options = resolvedOptions
	// Approved kernel work can execute before the next provider turn. Rebuild
	// the same complete task-scoped authority here so a restart does not reduce
	// host.mcp (or another host bridge) to the compact model-facing tool set.
	if transcriptAuthority != nil {
		toolAuthority, authorityErr := s.bindSessionRunnerToolAuthority(ctx, session, options)
		if authorityErr != nil {
			if handled, settleErr := settlePreChatError(authorityErr); handled {
				return result, settleErr
			}
			return result, authorityErr
		}
		options = toolAuthority.Options
	}

	var resumeErr error
	if transcriptAuthority != nil {
		if err := checkpointPreChatStage("kernel_recovery"); err != nil {
			if handled, settleErr := settlePreChatError(err); handled {
				return result, settleErr
			}
			return result, err
		}
		// Durable approved work must not inherit the short chat-preparation
		// deadline. Retain the same heartbeat used throughout preparation.
		// Each runnable operation applies its own bounded recovery budget; in
		// particular software_runtime covers both provisioning and execution
		// from the admitted timeout_seconds instead of inheriting a fixed 10m
		// deadline that can repeatedly cancel a slow but healthy installation.
		finishPreChat()
		_, resumeErr = s.resumeApprovedAgentKernelOperations(runCtx, options, chatRun)
		if handled, settleErr := settlePreChatError(resumeErr); handled {
			return result, settleErr
		}
	}

	var entries []eventjournal.Entry
	if resumeErr == nil {
		if err := checkpointPreChatStage("history_replay"); err != nil {
			if handled, settleErr := settlePreChatError(err); handled {
				return result, settleErr
			}
			return result, err
		}
		if transcriptAuthority != nil {
			entries, err = s.loadTranscriptRunnerReplay(
				ctx, transcriptAuthority, int(options.ReplayLimit), int(options.ReplayLimit),
			)
		} else {
			entries, err = s.eventJournal.ReadAfter(session.ID, 0, int(options.ReplayLimit))
		}
		if err != nil {
			return result, err
		}
		if len(entries) > 0 {
			result.CheckpointEventID = entries[len(entries)-1].EventID
		}
		chatRun.AfterEventID = result.CheckpointEventID
		s.hydrateSessionRunnerReadReuse(chatRun, entries)
	}
	if err := s.checkpointTranscriptRunner(ctx, transcriptAuthority, transcriptstore.RunnerPhasePlanning, "runner-started", map[string]any{
		"status": "running", "detail": "runner chat started",
	}, false); err != nil {
		if handled, settleErr := settlePreChatError(err); handled {
			return result, settleErr
		}
		activeRun.settlement.Lock()
		if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); drained {
			activeRun.settlement.Unlock()
			if drainErr != nil {
				return result, errors.Join(err, drainErr)
			}
			return result, nil
		}
		if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailureLocked(
			ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		); interrupted {
			activeRun.settlement.Unlock()
			if interruptionErr != nil {
				return result, errors.Join(err, interruptionErr)
			}
			return result, nil
		}
		_, finishErr := s.finishTranscriptRunner(context.WithoutCancel(ctx), transcriptAuthority, "failed", err.Error())
		if finishErr == nil {
			activeRun.settled = true
		}
		activeRun.settlement.Unlock()
		if finishErr != nil {
			return result, errors.Join(err, finishErr)
		}
		return result, err
	}
	if transcriptAuthority == nil {
		checkpoint, err := s.checkpointSessionRunner(map[string]any{
			"sessionId":       session.ID,
			"runnerId":        options.RunnerID,
			"runnerAttempt":   result.Attempt,
			"claimToken":      claimToken,
			"status":          "running",
			"message":         "runner chat started",
			"afterEventId":    result.CheckpointEventID,
			"clientMessageId": runnerCommandClientMessageID(options.RunnerID, session.ID, result.Attempt, claimToken, "chat-checkpoint"),
		})
		if err != nil {
			return result, err
		}
		if event, ok := checkpoint["event"].(*eventjournal.Entry); ok && event != nil {
			result.CheckpointEventID = event.EventID
		}
	}

	chatRun.AfterEventID = result.CheckpointEventID
	finishPreChat()
	var assistantMessage string
	var chatErr error
	if resumeErr != nil {
		chatErr = resumeErr
	} else {
		assistantMessage, chatErr = s.runSessionRunnerChat(
			withTranscriptRunnerChatRun(runCtx, chatRun), options, session, entries, chatRun,
		)
	}
	heartbeatErr := stopHeartbeat()
	// stopHeartbeat deliberately cancels runCtx to join the heartbeat goroutine.
	// Terminal settlement must inspect the active task parent instead; otherwise
	// that internal cleanup cancellation is indistinguishable from a user stop
	// and a successful response is incorrectly committed as cancelled.
	ctx = preChatParentCtx
	if drained, drainErr := s.interruptSessionRunnerForRuntimeDrain(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
	); drained {
		if drainErr != nil {
			return result, drainErr
		}
		return result, nil
	}
	if interrupted, interruptionErr := s.interruptSessionRunnerForInfrastructureFailure(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
	); interrupted {
		if interruptionErr != nil {
			return result, interruptionErr
		}
		return result, nil
	}
	if handled, handleErr := s.handleSessionRunnerChatInterruption(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		chatRun, entries, &chatErr, heartbeatErr,
	); handled {
		return result, handleErr
	}
	if err := s.settleSessionRunnerChatOutcome(
		ctx, options, &result, activeRun, projectionClaim, transcriptAuthority,
		chatRun, session, entries, assistantMessage, chatErr, claimToken,
	); err != nil {
		return result, err
	}
	return result, nil
}
