package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) RunFrameResumeDispatchOnce(ctx context.Context, options FrameResumeDispatchOptions) (FrameResumeDispatchResult, error) {
	options = normalizeFrameResumeDispatchOptions(options)
	if s == nil || s.workspaceStore == nil {
		return FrameResumeDispatchResult{}, errors.New("frame resume dispatch runtime is not configured")
	}
	if s.isDraining() {
		return FrameResumeDispatchResult{}, nil
	}
	dispatch, claimed, err := s.workspaceStore.ClaimNextCompatibilityFrameResumeDispatch(options.WorkerID, options.ClaimTTL)
	if err != nil {
		return FrameResumeDispatchResult{}, err
	}
	result := FrameResumeDispatchResult{Claimed: claimed}
	if !claimed {
		return result, nil
	}
	result.ResumeEventID = dispatch.ResumeEvent.ID
	result.FrameID = dispatch.FrameID
	result.Attempt = dispatch.Attempt
	result.Recovered = dispatch.Recovered

	// A claimed dispatch is durable authority. Start renewing it before any
	// publication, projection, or Transcript lookup so a slow preflight cannot
	// silently lose ownership and leave a week-long task stuck between the
	// workspace dispatch and its runner attempt.
	runContext, cancelRun := context.WithCancelCause(ctx)
	monitorDone := make(chan struct{})
	monitorErr := make(chan error, 1)
	var monitorWait sync.WaitGroup
	monitorWait.Add(1)
	go func() {
		defer monitorWait.Done()
		s.monitorFrameResumeDispatch(runContext, cancelRun, monitorDone, monitorErr, dispatch, options.ClaimTTL)
	}()
	var (
		stopMonitorOnce sync.Once
		monitorStopErr  error
	)
	stopMonitor := func() error {
		stopMonitorOnce.Do(func() {
			close(monitorDone)
			cancelRun(nil)
			monitorWait.Wait()
			select {
			case monitorStopErr = <-monitorErr:
			default:
			}
		})
		return monitorStopErr
	}
	defer stopMonitor()

	for _, event := range dispatch.AuditEvents {
		if err := s.publishWorkspaceEvent(event); err != nil {
			return result, fmt.Errorf("publish resume dispatch audit: %w", err)
		}
	}
	if err := s.registerFrameResumeDispatch(dispatch.ResumeEvent.ID, options.ReservationTTL); err != nil {
		return result, err
	}

	// A terminal frame must never be re-run. Old dispatches left behind by a
	// completed/failed/cancelled conversation are reconciled here so the
	// dispatcher cannot monopolize the loop on stale claims.
	if frame, found, err := s.workspaceStore.GetFrame(dispatch.FrameID); err != nil {
		return result, err
	} else if found && isFrameResumeTerminalStatus(frame.Status) {
		terminal, _, completeErr := s.workspaceStore.CompleteCompatibilityFrameResumeDispatch(
			workspace.CompleteCompatibilityFrameResumeDispatchInput{
				ResumeEventID:   dispatch.ResumeEvent.ID,
				ExpectedAttempt: dispatch.Attempt,
				ClaimToken:      dispatch.ClaimToken,
				Status:          frame.Status,
				Message:         "reconciled terminal frame dispatch",
				Details: map[string]any{
					"reconciledFrameStatus": true,
					"frameStatus":           frame.Status,
				},
			},
		)
		if completeErr != nil {
			return result, completeErr
		}
		result.Status = frame.Status
		result.TerminalEvent = &terminal
		if err := s.publishWorkspaceEvent(terminal); err != nil {
			return result, err
		}
		return result, nil
	}

	runnerID := frameResumeRunnerID(dispatch.ResumeEvent.ID)
	var legacyClaim *sessionstore.RunnerMutationClaim
	stream, transcriptFrame, err := s.resolveTranscriptFrameStream(runContext, dispatch.FrameID)
	if err != nil {
		return result, err
	}
	if transcriptFrame {
		state, stateFound, stateErr := s.transcriptStore.GetLatestRunnerRuntimeState(runContext, stream.UID, stream.OwnerID)
		if stateErr != nil {
			return result, stateErr
		}
		if stateFound && state.Status == "running" && state.ExpiresAt.After(time.Now().UTC()) {
			convergedEvent, convergeErr := s.convergeFrameResumeDispatchIntoRunner(
				runContext, dispatch, stream, state,
			)
			if convergeErr != nil {
				return result, convergeErr
			}
			if convergedEvent != nil {
				result.Status = "completed"
				result.TerminalEvent = convergedEvent
				return result, nil
			}
			// The ordinary runner may have claimed the Frame before an Ask User
			// answer or explicit continue created this resume dispatch. Release the
			// dispatch until that authoritative runner lease expires instead of
			// letting the short dispatch claim expire and be recovered by every
			// worker in a tight audit-producing loop.
			deferredEvent, _, requeueErr := s.workspaceStore.RequeueCompatibilityFrameResumeDispatch(
				workspace.RequeueCompatibilityFrameResumeDispatchInput{
					ResumeEventID:   dispatch.ResumeEvent.ID,
					ExpectedAttempt: dispatch.Attempt,
					ClaimToken:      dispatch.ClaimToken,
					ReasonCode:      "runner_still_active",
					RunnerAttempt:   int(state.Attempt),
					NotBefore:       state.ExpiresAt,
				},
			)
			if requeueErr != nil {
				return result, requeueErr
			}
			result.Status = "deferred"
			if err := s.publishWorkspaceEvent(deferredEvent); err != nil {
				return result, err
			}
			return result, nil
		}
		if stateFound && state.RunnerID == runnerID && isFrameResumeTerminalStatus(state.Status) {
			result.Status = state.Status
			terminal, _, completeErr := s.workspaceStore.CompleteCompatibilityFrameResumeDispatch(
				workspace.CompleteCompatibilityFrameResumeDispatchInput{
					ResumeEventID:   dispatch.ResumeEvent.ID,
					ExpectedAttempt: dispatch.Attempt,
					ClaimToken:      dispatch.ClaimToken,
					Status:          state.Status,
					Message:         state.Status,
					Details: map[string]any{
						"runnerId":           runnerID,
						"runnerAttempt":      state.Attempt,
						"reconciledTerminal": true,
					},
				},
			)
			if completeErr != nil {
				return result, completeErr
			}
			result.TerminalEvent = &terminal
			if err := s.publishWorkspaceEvent(terminal); err != nil {
				return result, err
			}
			return result, nil
		}
		// A message sent while the previous runner/dispatch was still active is
		// intentionally held in the compatibility queue. The wake signal can
		// race with a claimed dispatch, so it is not sufficient to drain the
		// queue only from the HTTP request path: that path may observe `claimed`
		// and correctly leave the message queued. Drain it after this dispatch
		// has confirmed that no runner lease is active and before configuring the
		// transcript resume source. Otherwise the runner replays the stale
		// checkpoint, can reopen clarification, and the queued same-task repair
		// instruction never becomes part of its logical input.
		if _, drainErr := s.advanceCompatibilityFrameAfterRunner(dispatch.FrameID, "processing"); drainErr != nil {
			return result, fmt.Errorf("deliver queued user input before resume: %w", drainErr)
		}
	} else {
		if s.sessionStore == nil || s.eventJournal == nil {
			return result, errors.New("legacy resume dispatch runtime is not configured")
		}
		session, found, sessionErr := s.sessionStore.Get(dispatch.FrameID)
		if sessionErr != nil {
			return result, sessionErr
		}
		if !found {
			return result, fmt.Errorf("resume dispatch session %q is not registered", dispatch.FrameID)
		}
		if session.Runner != nil && session.Runner.RunnerID == runnerID && isFrameResumeTerminalStatus(session.Runner.Status) {
			result.Status = session.Runner.Status
			terminal, _, err := s.workspaceStore.CompleteCompatibilityFrameResumeDispatch(
				workspace.CompleteCompatibilityFrameResumeDispatchInput{
					ResumeEventID:   dispatch.ResumeEvent.ID,
					ExpectedAttempt: dispatch.Attempt,
					ClaimToken:      dispatch.ClaimToken,
					Status:          session.Runner.Status,
					Message:         session.Runner.LastCheckpoint,
					Details: map[string]any{
						"runnerId":           runnerID,
						"runnerAttempt":      session.Runner.Attempt,
						"reconciledTerminal": true,
					},
				},
			)
			if err != nil {
				return result, err
			}
			result.TerminalEvent = &terminal
			if err := s.publishWorkspaceEvent(terminal); err != nil {
				return result, err
			}
			return result, nil
		}
		if s.hasActiveSessionRun(dispatch.FrameID) {
			return result, errFrameResumeRunnerStillActive
		}
		if session.Runner == nil {
			// The API prepares the legacy session but only the dispatch worker
			// may reserve its runner claim.
		} else if session.Runner.RunnerID == runnerID {
			claim := sessionstore.RunnerClaimFromSession(session)
			if _, validateErr := s.sessionStore.ValidateRunnerClaim(claim, true); validateErr == nil {
				return result, errFrameResumeRunnerStillActive
			} else if !errors.Is(validateErr, sessionstore.ErrRunnerClaimStale) {
				return result, validateErr
			}
		}
		entries, readErr := s.eventJournal.ReadAll(dispatch.FrameID)
		if readErr != nil {
			return result, readErr
		}
		if legacyResumeHasUnresolvedSideEffectingTool(entries) {
			return result, s.failUncertainFrameResumeDispatch(&result, dispatch)
		}
		reserved, claimed, reserveErr := s.sessionStore.ClaimRunner(dispatch.FrameID, runnerID, options.ReservationTTL)
		if reserveErr != nil {
			return result, reserveErr
		}
		if !claimed || reserved.Runner == nil {
			return result, errFrameResumeRunnerStillActive
		}
		claim := sessionstore.RunnerClaimFromSession(reserved)
		active, renewErr := s.renewFrameResumeDispatchClaim(runContext, dispatch, options.ClaimTTL)
		if renewErr != nil || !active {
			_, _, _ = s.sessionStore.ReleaseRunner(claim)
			if renewErr != nil {
				return result, renewErr
			}
			return result, errFrameResumeDispatchClaimLost
		}
		entries, readErr = s.eventJournal.ReadAll(dispatch.FrameID)
		if readErr != nil {
			_, _, _ = s.sessionStore.ReleaseRunner(claim)
			return result, readErr
		}
		if legacyResumeHasUnresolvedSideEffectingTool(entries) {
			if _, _, releaseErr := s.sessionStore.ReleaseRunner(claim); releaseErr != nil && !errors.Is(releaseErr, sessionstore.ErrRunnerClaimStale) {
				return result, releaseErr
			}
			return result, s.failUncertainFrameResumeDispatch(&result, dispatch)
		}
		legacyClaim = &claim
	}

	chat := options.Chat
	chat.SessionID = dispatch.FrameID
	chat.RunnerID = runnerID
	var runnerResult SessionRunnerCycleResult
	runErr := s.configureTranscriptFrameResume(runContext, dispatch, &chat)
	if runErr == nil {
		runnerResult, runErr = s.runSessionRunnerChatOnce(runContext, chat, legacyClaim)
	}
	if runErr == nil && !runnerResult.Claimed {
		runErr = errFrameResumeRunnerStillActive
	}
	monitorStopErr = stopMonitor()
	runErrFromMonitor := false
	if monitorStopErr != nil && runErr == nil {
		runErr = monitorStopErr
		runErrFromMonitor = true
	}
	active, ownershipErr := s.renewFrameResumeDispatchClaim(ctx, dispatch, options.ClaimTTL)
	if ownershipErr != nil {
		// Without confirmed ownership, this worker must not terminalize or mutate
		// the durable dispatch. Its lease can expire and be recovered safely.
		if legacyClaim != nil {
			if _, _, releaseErr := s.sessionStore.ReleaseRunner(*legacyClaim); releaseErr != nil &&
				!errors.Is(releaseErr, sessionstore.ErrRunnerClaimStale) {
				ownershipErr = errors.Join(ownershipErr, releaseErr)
			}
		}
		result.Runner = runnerResult
		return result, ownershipErr
	} else if !active {
		if legacyClaim != nil {
			_, _, _ = s.sessionStore.ReleaseRunner(*legacyClaim)
		}
		result.Runner = runnerResult
		return result, errFrameResumeDispatchClaimLost
	}
	if errors.Is(monitorStopErr, errFrameResumeDispatchClaimLost) {
		if legacyClaim != nil {
			_, _, _ = s.sessionStore.ReleaseRunner(*legacyClaim)
		}
		result.Runner = runnerResult
		return result, errFrameResumeDispatchClaimLost
	}
	if runErrFromMonitor && runnerResult.Claimed && strings.TrimSpace(runnerResult.Status) != "" {
		// The runner already durably settled this execution unit and this worker
		// still owns the dispatch. Respect that durable runner state; a monitor
		// renewal error must not overwrite it with a terminal dispatch failure.
		runErr = nil
	}
	if runErr != nil && legacyClaim != nil {
		if _, released, releaseErr := s.sessionStore.ReleaseRunner(*legacyClaim); releaseErr != nil && !errors.Is(releaseErr, sessionstore.ErrRunnerClaimStale) {
			runErr = errors.Join(runErr, releaseErr)
		} else if released {
			runnerResult.Claimed = false
		}
	}
	result.Runner = runnerResult
	if errors.Is(runErr, errFrameResumeDispatchClaimLost) {
		return result, errFrameResumeDispatchClaimLost
	}
	if frameResumeDispatchShouldRequeueActiveRunner(transcriptFrame, runErr) {
		if err := s.deferClaimedFrameResumeDispatchForActiveRunner(
			dispatch, runnerResult.Attempt,
		); err != nil {
			return result, err
		}
		result.Status = "deferred"
		return result, errFrameResumeRunnerStillActive
	}
	if errors.Is(runErr, errFrameResumeRunnerStillActive) || errors.Is(runErr, ErrSessionRunAlreadyActive) {
		return result, errFrameResumeRunnerStillActive
	}
	if frameResumeDispatchDeferred(runErr) {
		return result, runErr
	}
	if runErr == nil && runnerResult.Claimed && runnerResult.Status == "interrupted" {
		reasonCode := strings.TrimSpace(runnerResult.InterruptionReasonCode)
		if reasonCode == "" {
			reasonCode = "runner_interrupted"
		}
		if runnerResult.AwaitingModelSelection {
			// Model configuration is a recoverable user-owned prerequisite, not a
			// failed scientific task. Keep the exact dispatch and resumable runner
			// checkpoint parked until a model selection or explicit continue wakes
			// it; no timer may turn this wait into a task failure or hot retry loop.
			pausedEvent, _, requeueErr := s.workspaceStore.RequeueCompatibilityFrameResumeDispatch(
				workspace.RequeueCompatibilityFrameResumeDispatchInput{
					ResumeEventID:     dispatch.ResumeEvent.ID,
					ExpectedAttempt:   dispatch.Attempt,
					ClaimToken:        dispatch.ClaimToken,
					ReasonCode:        reasonCode,
					RunnerAttempt:     runnerResult.Attempt,
					CheckpointEventID: runnerResult.CheckpointEventID,
					WaitingFor:        workspace.CompatibilityFrameResumeDispatchWaitModelSelection,
				},
			)
			if requeueErr != nil {
				return result, requeueErr
			}
			result.Status = "paused"
			if err := s.publishWorkspaceEvent(pausedEvent); err != nil {
				return result, err
			}
			return result, nil
		}
		// Only auto-resume interruptions may be re-claimed without a new user
		// message or an explicit continue. The transcript candidate query does not
		// cap a progressing logical task. Repeated semantic no-progress signals use
		// a capped scheduling delay below, never a terminal attempt ceiling.
		if frameResumeRunnerShouldAutoResume(transcriptFrame, runnerResult, reasonCode) {
			repetition := int64(dispatch.Attempt)
			if transcriptFrame && reasonCode != sessionRunnerKernelOperationPendingRecoveryReasonCode {
				candidate, eligible, eligibilityErr := s.transcriptStore.GetAutoResumeCandidate(
					ctx, stream.UID, stream.OwnerID,
				)
				if eligibilityErr != nil {
					return result, eligibilityErr
				}
				if eligible && candidate.ReasonCode == reasonCode {
					repetition = candidate.InputRevisionBounces
				}
			}
			notBefore := frameResumeDispatchAutoResumePolicy(
				reasonCode, repetition, stream.UID, time.Now().UTC(),
			)
			interruptedEvent, _, requeueErr := s.workspaceStore.RequeueCompatibilityFrameResumeDispatch(
				workspace.RequeueCompatibilityFrameResumeDispatchInput{
					ResumeEventID:     dispatch.ResumeEvent.ID,
					ExpectedAttempt:   dispatch.Attempt,
					ClaimToken:        dispatch.ClaimToken,
					ReasonCode:        reasonCode,
					RunnerAttempt:     runnerResult.Attempt,
					CheckpointEventID: runnerResult.CheckpointEventID,
					NotBefore:         notBefore,
				},
			)
			if requeueErr != nil {
				return result, requeueErr
			}
			result.Status = "interrupted"
			if err := s.publishWorkspaceEvent(interruptedEvent); err != nil {
				return result, err
			}
			if transcriptFrame && reasonCode == sessionRunnerKernelOperationPendingRecoveryReasonCode &&
				strings.TrimSpace(runnerResult.KernelOperationID) != "" {
				operation, found, loadErr := s.workspaceStore.GetKernelLocalOperation(
					ctx, stream.OwnerID, runnerResult.KernelOperationID,
				)
				if loadErr != nil {
					return result, loadErr
				}
				// Approval or terminal settlement can win the race immediately
				// before this dispatcher commits its recovery fence. Re-check the
				// exact operation after requeue and consume that already-durable
				// transition, otherwise a healthy task waits for the backstop or
				// incorrectly appears to require manual continuation.
				if found && frameResumeKernelOperationReady(operation.State) {
					if wakeErr := s.wakeFrameResumeDispatchAfterKernelTransition(ctx, operation); wakeErr != nil {
						return result, wakeErr
					}
				}
			}
			return result, nil
		}
		settledEvent, resumedByModelSwitch, settleErr := s.workspaceStore.FailCompatibilityFrameResumeDispatch(
			dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, reasonCode,
		)
		if settleErr != nil {
			return result, settleErr
		}
		if resumedByModelSwitch {
			result.Status = "interrupted"
		} else {
			result.Status = "failed"
		}
		if err := s.publishWorkspaceEvent(settledEvent); err != nil {
			return result, err
		}
		return result, nil
	}
	if runErr == nil && runnerResult.Claimed && runnerInterruptionPauseStatus(runnerResult.Status) {
		// A durable waiting checkpoint (kernel approval or ask-user) is NOT a
		// terminal Frame outcome. Requeue the dispatch with a backstop fence:
		// the approval/user-response resolution path wakes it immediately, and
		// the fence prevents a hot re-claim loop while the checkpoint is still
		// unresolved. Completing the dispatch here would leave the Frame
		// stranded in a non-terminal state.
		pausedEvent, _, requeueErr := s.workspaceStore.RequeueCompatibilityFrameResumeDispatch(
			workspace.RequeueCompatibilityFrameResumeDispatchInput{
				ResumeEventID:     dispatch.ResumeEvent.ID,
				ExpectedAttempt:   dispatch.Attempt,
				ClaimToken:        dispatch.ClaimToken,
				ReasonCode:        runnerResult.Status,
				RunnerAttempt:     runnerResult.Attempt,
				CheckpointEventID: runnerResult.CheckpointEventID,
				NotBefore:         time.Now().UTC().Add(frameResumePauseRecheckInterval),
			},
		)
		if requeueErr != nil {
			return result, requeueErr
		}
		result.Status = runnerResult.Status
		if err := s.publishWorkspaceEvent(pausedEvent); err != nil {
			return result, err
		}
		return result, nil
	}
	status := runnerResult.Status
	message := status
	if runErr != nil {
		status = "failed"
		message = runErr.Error()
	} else if !runnerResult.Claimed {
		status = "failed"
		message = "session runner did not claim the reserved resume session"
	} else if status == "" {
		status = "failed"
		message = "session runner returned no terminal status"
	}
	result.Status = status
	terminal, _, completeErr := s.workspaceStore.CompleteCompatibilityFrameResumeDispatch(
		workspace.CompleteCompatibilityFrameResumeDispatchInput{
			ResumeEventID:   dispatch.ResumeEvent.ID,
			ExpectedAttempt: dispatch.Attempt,
			ClaimToken:      dispatch.ClaimToken,
			Status:          status,
			Message:         message,
			Details: map[string]any{
				"runnerId":          runnerID,
				"runnerAttempt":     runnerResult.Attempt,
				"assistantEventId":  runnerResult.AssistantEventID,
				"finishEventId":     runnerResult.FinishEventID,
				"checkpointEventId": runnerResult.CheckpointEventID,
			},
		},
	)
	if completeErr != nil {
		return result, completeErr
	}
	result.TerminalEvent = &terminal
	if err := s.publishWorkspaceEvent(terminal); err != nil {
		return result, err
	}
	return result, nil
}

func frameResumeKernelOperationReady(state string) bool {
	state = strings.TrimSpace(state)
	return state == workspace.KernelLocalOperationStateApproved || workspace.KernelLocalOperationIsTerminal(state)
}

// frameResumeRunnerShouldAutoResume preserves the interruption's durable
// continuation decision for canonical Transcript tasks. Falling back to a
// reason-code default after the writer deliberately set AutoResume=false would
// revive a checkpoint that requires an external decision and create a visible
// correction loop.
