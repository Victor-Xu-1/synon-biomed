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
	"synon-go/internal/providers"
)

func (s *Server) handleSessionRunnerChatInterruption(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	chatRun *sessionRunnerChatRun,
	entries []eventjournal.Entry,
	chatErrRef *error,
	heartbeatErr error,
) (bool, error) {
	var chatErr error
	if chatErrRef != nil {
		chatErr = *chatErrRef
	}
	var reviewStageErr *sessionRunnerReviewStageError
	if errors.As(chatErr, &reviewStageErr) {
		if chatRun != nil && chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		resumeDetail := "completion review execution failed after the main candidate was produced; preserve the candidate and reviewer diagnostics, then retry the independent review path in a new execution unit"
		if reviewStageErr != nil && strings.TrimSpace(reviewStageErr.Error()) != "" {
			resumeDetail += ": " + reviewStageErr.Error()
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerCompletionReviewRecoveryReasonCode, resumeDetail,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var noProgressInterruption sessionRunnerProviderNoProgressInterruption
	if providers.IsRetryableModelProtocolError(chatErr) {
		if chatRun != nil && chatRun.ProviderAttemptSemanticBytes > 0 {
			boundaryEvent, boundaryErr := s.persistProviderContinuationBoundary(context.WithoutCancel(ctx), chatRun)
			if boundaryErr != nil {
				chatErr = fmt.Errorf("persist exact model-protocol recovery boundary: %w", boundaryErr)
			} else {
				result.CheckpointEventID = maxInt64(result.CheckpointEventID, boundaryEvent.EventID)
			}
		}
		if providers.IsRetryableModelProtocolError(chatErr) {
			if err := s.interruptClaimedSessionRunner(
				options, result, activeRun, projectionClaim, transcriptAuthority,
				sessionRunnerModelProtocolErrorReasonCode,
				"the model provider returned malformed tool-call protocol data; start a fresh bounded generation while preserving completed tools, artifacts, and the exact accepted response prefix",
			); err != nil {
				return true, err
			}
			return true, nil
		}
	}
	if providers.IsProviderEmptyResponse(chatErr) {
		if chatRun != nil && chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			"provider_stream_no_progress",
			"the model provider returned no semantic output after bounded request retries; preserve completed tools and resume from the latest durable checkpoint",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	providerStreamInterruption := providers.IsRecoverableStreamInterruption(chatErr)
	providerResponseTruncation := providers.IsContinuationSafeResponseTruncation(chatErr)
	providerOutputTokenLimit := providers.IsProviderOutputTokenLimit(chatErr)
	hasDurableProviderContinuation := chatRun.ProviderContinuation != nil &&
		chatRun.ProviderContinuation.Content.Len() > 0 &&
		chatRun.ProviderContinuation.Contract.AcceptedSemanticBytes > 0
	if providerResponseHeadersTimeoutWithoutProgress(
		chatErr, chatRun.ProviderAttemptSemanticBytes, hasDurableProviderContinuation,
	) {
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerProviderTransportTemporaryReasonCode,
			"the model provider did not return response headers after this bounded transport attempt; preserve the task and retry automatically from the latest durable checkpoint",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	if chatRun.ProviderAttemptSemanticBytes == 0 && providers.IsRetryableProviderTransportFailure(chatErr) {
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerProviderTransportTemporaryReasonCode,
			"the model provider transport ended before returning semantic output after bounded request retries; preserve the task and retry from the latest durable checkpoint",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	if !providerStreamInterruption &&
		(chatRun.ProviderAttemptSemanticBytes > 0 || hasDurableProviderContinuation) {
		providerStreamInterruption = providers.IsContinuationSafeStreamFailure(chatErr)
	}
	if providerStreamInterruption || providerResponseTruncation || providerOutputTokenLimit || errors.As(chatErr, &noProgressInterruption) {
		noProgress := chatRun.ProviderAttemptSemanticBytes == 0
		if providerOutputTokenLimit && noProgress && !hasDurableProviderContinuation {
			if err := s.interruptClaimedSessionRunner(
				options, result, activeRun, projectionClaim, transcriptAuthority,
				sessionRunnerProviderOutputTokenLimitReasonCode,
				"provider reached its output token limit while forming the next action; discard the incomplete action and resume from the last completed tool checkpoint",
			); err != nil {
				return true, err
			}
			return true, nil
		}
		// No-progress segments are bounded execution units, not a logical-task
		// failure budget. Persist every exact continuation fence and let the
		// durable dispatcher apply capped backoff before reclaiming it. This keeps
		// an unavailable or slow provider from forming a hot loop without imposing
		// an artificial lifetime or continuation ceiling on the user's task.
		boundaryEvent, boundaryErr := s.persistProviderContinuationBoundary(context.WithoutCancel(ctx), chatRun)
		if boundaryErr != nil {
			chatErr = fmt.Errorf("persist exact provider continuation boundary: %w", boundaryErr)
		} else {
			result.CheckpointEventID = maxInt64(result.CheckpointEventID, boundaryEvent.EventID)
			reasonCode := "provider_stream_interrupted"
			if noProgress {
				reasonCode = "provider_stream_no_progress"
			}
			resumeDetail := ""
			if providerResponseTruncation {
				resumeDetail = "provider reached its output token limit after durable content; continue from the exact accepted prefix"
			} else if providerOutputTokenLimit {
				reasonCode = sessionRunnerProviderOutputTokenLimitReasonCode
				resumeDetail = "provider reached its output token limit before completing the next action; start a fresh bounded generation from the last completed tool checkpoint"
			}
			if err := s.interruptClaimedSessionRunner(
				options, result, activeRun, projectionClaim, transcriptAuthority, reasonCode, resumeDetail,
			); err != nil {
				return true, err
			}
			return true, nil
		}
	}
	var persistenceInterruption sessionRunnerPersistenceInterruption
	var emptyFinalCandidate *sessionRunnerEmptyFinalCandidateError
	if errors.As(chatErr, &emptyFinalCandidate) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerEmptyFinalResponseReasonCode,
			"the previous bounded execution ended without a user-visible final answer; repair the completion path and produce a concrete conclusion or durable artifact before claiming completion",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	if errors.As(chatErr, &persistenceInterruption) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
			"content delta persistence exceeded its local operation deadline; resume from the durable checkpoint",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var toolLifecycleInterruption sessionRunnerToolLifecyclePersistenceInterruption
	if errors.As(chatErr, &toolLifecycleInterruption) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerToolLifecyclePersistenceReasonCode,
			toolLifecycleInterruption.diagnosticDetail(),
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var kernelRecovery *kernelLocalOperationPendingRecoveryError
	if errors.As(chatErr, &kernelRecovery) {
		result.KernelOperationID = strings.TrimSpace(kernelRecovery.operationID)
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerKernelOperationPendingRecoveryReasonCode,
			"kernel local execution is pending durable recovery; recover the exact tool batch before continuing",
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var toolRoundNoProgress *agentruntime.ToolRoundNoProgressError
	if errors.As(chatErr, &toolRoundNoProgress) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		noProgressCount := runnerToolRoundNoProgressInterruptionCount(entries) + 1
		noProgressState := sessionRunnerNoProgressRecovery{}
		if chatRun != nil {
			noProgressState = chatRun.recordNoProgress(toolRoundNoProgress.Calls)
			noProgressCount = noProgressState.Consecutive
		}
		reasonCode := sessionRunnerToolRoundNoProgressReasonCode
		resumeDetail := "the agent repeated tool rounds without producing new evidence; resume must reuse completed receipts and choose a materially different action or finish"
		if noProgressCount >= sessionRunnerConsecutiveIdenticalToolRoundBudget {
			reasonCode = sessionRunnerToolRoundNoProgressExhaustedReasonCode
			resumeDetail = "the agent repeatedly cycled across completed tool routes without semantic progress; preserve completed receipts, quarantine the repeated route, and continue automatically with a materially different action"
		}
		if noProgressState.Schema == sessionRunnerNoProgressRecoverySchema {
			resumeDetail = noProgressState.resumeDetail(resumeDetail)
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			reasonCode, resumeDetail,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var toolRoundLimit *agentruntime.ToolRoundLimitError
	if errors.As(chatErr, &toolRoundLimit) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		limit := 0
		if toolRoundLimit != nil {
			limit = toolRoundLimit.Limit
		}
		resumeDetail := fmt.Sprintf(
			"the agent reached the bounded tool-round limit (%d) for this execution segment; successful tool results and artifacts are preserved, and the next bounded segment will continue automatically",
			limit,
		)
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerToolRoundLimitReasonCode, resumeDetail,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var initialToolChoiceViolation *agentruntime.InitialToolChoiceViolationError
	if errors.As(chatErr, &initialToolChoiceViolation) {
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		reasonCode := sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode
		resumeDetail := "the provider repeatedly returned prose instead of the required first tool call; retry the same task with a model-selected advertised tool and do not claim completion before tool evidence"
		if initialToolChoiceViolation != nil && initialToolChoiceViolation.ToolsForbidden {
			resumeDetail = "the provider repeatedly emitted a tool call after the outer runtime closed tool execution for immutable validation; preserve the current candidate and retry the terminal response without executing another tool"
		}
		if correction, found := latestRunnerCorrection(entries); found && recoveredRunnerCorrectionRequiresTool(entries) {
			reasonCode = correction.ReasonCode
			resumeDetail = correction.Detail
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			reasonCode, resumeDetail,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	var boundedCorrection sessionRunnerBoundedCorrection
	if errors.As(chatErr, &boundedCorrection) {
		reasonCode, resumeDetail := boundedCorrection.runnerCorrection()
		repeatedCorrections := runnerRepeatedCorrectionInterruptionCount(entries, reasonCode, resumeDetail)
		correctionExhausted := repeatedCorrections >= sessionRunnerCorrectionNoProgressBudget-1
		if correctionExhausted {
			// Preserve the exact integrity failure and stop unattended recovery
			// after bounded identical obligations. A task with durable artifacts is
			// still recoverable by an explicit user continuation, but it must not
			// spin through the same final-candidate path forever.
			reasonCode = sessionRunnerCorrectionNoProgressExhaustedReasonCode
			resumeDetail = fmt.Sprintf(
				"the same completion correction remained unresolved after %d bounded attempts; preserve all durable artifacts and wait for an explicit continuation before trying a materially different repair. Last failure: %s",
				sessionRunnerCorrectionNoProgressBudget, strings.TrimSpace(resumeDetail),
			)
		} else if !runnerInterruptionMayContinueSameTask(reasonCode) {
			return false, nil
		}
		if chatRun.AssistantSegmentHasContent {
			// Published assistant segments are immutable. A correction continues
			// in a fresh segment instead of making accepted text disappear.
			chatRun.beginNextAssistantSegment()
		}
		if chatRun.AfterEventID > result.CheckpointEventID {
			result.CheckpointEventID = chatRun.AfterEventID
		}
		if err := s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority,
			reasonCode, resumeDetail,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	if handled, heartbeatErr := s.settleSessionRunnerHeartbeatError(ctx, result, transcriptAuthority, heartbeatErr); handled {
		return true, heartbeatErr
	}
	var pause *agentruntime.PauseError
	if errors.As(chatErr, &pause) {
		if err := markSessionRunnerPaused(chatRun); err != nil {
			return true, err
		}
		if transcriptAuthority != nil {
			activeRun.settlement.Lock()
			if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
				ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
			); drained {
				activeRun.settlement.Unlock()
				if drainErr != nil {
					return true, drainErr
				}
				return true, nil
			}
			if cause := context.Cause(ctx); errors.Is(cause, ErrGenerationStopped) || errors.Is(cause, context.Canceled) {
				terminalEvent, finishErr := s.finishTranscriptRunner(context.WithoutCancel(ctx), transcriptAuthority, "cancelled", cause.Error())
				if finishErr == nil {
					activeRun.settled = true
				}
				activeRun.settlement.Unlock()
				if finishErr != nil {
					return true, finishErr
				}
				result.Status = "cancelled"
				result.FinishEventID = terminalEvent.EventID
				return true, nil
			}
			if strings.TrimSpace(pause.Status) == "awaiting_approval" {
				pauseEvent, pauseErr := s.pauseTranscriptRunnerForApproval(
					context.WithoutCancel(ctx), transcriptAuthority, pause,
				)
				if pauseErr == nil {
					activeRun.settled = true
				}
				activeRun.settlement.Unlock()
				if pauseErr != nil {
					return true, pauseErr
				}
				result.CheckpointEventID = pauseEvent.EventID
				result.Status = pause.Status
				return true, nil
			}
			parked, pauseEvent, pauseErr := s.parkTranscriptAskUser(context.WithoutCancel(ctx), transcriptAuthority, pause)
			if pauseErr == nil {
				activeRun.settled = true
			}
			activeRun.settlement.Unlock()
			if pauseErr != nil {
				return true, pauseErr
			}
			for _, event := range parked.Events {
				if err := s.publishWorkspaceEvent(event); err != nil {
					return true, err
				}
			}
			result.CheckpointEventID = pauseEvent.EventID
		} else {
			activeRun.settlement.Lock()
			if drained, drainErr := s.interruptSessionRunnerForRuntimeDrainLocked(
				ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
			); drained {
				activeRun.settlement.Unlock()
				if drainErr != nil {
					return true, drainErr
				}
				return true, nil
			}
			if _, released, err := s.sessionStore.ReleaseRunner(projectionClaim); err != nil {
				activeRun.settlement.Unlock()
				return true, fmt.Errorf("release paused runner chat lease: %w", err)
			} else if !released {
				activeRun.settlement.Unlock()
				return true, errors.New("paused runner chat no longer owns its session lease")
			}
			activeRun.settled = true
			activeRun.settlement.Unlock()
		}
		result.Status = pause.Status
		return true, nil
	}
	return false, nil
}

func (s *Server) settleSessionRunnerHeartbeatError(
	ctx context.Context,
	result *SessionRunnerCycleResult,
	transcriptAuthority *transcriptRunnerAuthority,
	heartbeatErr error,
) (bool, error) {
	if heartbeatErr == nil {
		return false, nil
	}
	if transcriptAuthority != nil && errors.Is(heartbeatErr, transcriptstore.ErrClaimStale) {
		state, stateErr := s.transcriptStore.GetRunnerRuntimeState(
			context.WithoutCancel(ctx), transcriptAuthority.Stream.UID,
			transcriptAuthority.Stream.OwnerID, transcriptAuthority.Claim.Attempt,
		)
		if stateErr == nil && (state.Status == "completed" || state.Status == "failed" ||
			state.Status == "cancelled" || state.Status == "canceled") {
			result.Status = state.Status
			result.FinishEventID = state.FinishedEventID
			return true, nil
		}
	}
	return true, heartbeatErr
}

func (s *Server) settleSessionRunnerHeartbeatOnReturn(
	ctx context.Context,
	result *SessionRunnerCycleResult,
	transcriptAuthority *transcriptRunnerAuthority,
	cycleErr *error,
	stopHeartbeat func() error,
) {
	// Preparation also has direct error returns. Preserve any lease failure
	// they have not consumed without replacing their own error.
	if heartbeatErr := stopHeartbeat(); heartbeatErr != nil {
		_, settleErr := s.settleSessionRunnerHeartbeatError(ctx, result, transcriptAuthority, heartbeatErr)
		if settleErr != nil && !errors.Is(*cycleErr, settleErr) {
			*cycleErr = errors.Join(*cycleErr, settleErr)
		}
	}
}
