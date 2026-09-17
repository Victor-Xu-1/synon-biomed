package server

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) RunFrameResumeDispatchLoop(ctx context.Context, options FrameResumeDispatchOptions) error {
	options = normalizeFrameResumeDispatchOptions(options)
	idlePollInterval := options.PollInterval
	for {
		if s.isDraining() {
			return nil
		}
		// Capture the wake generation before the durable claim to avoid losing a
		// frame_resumed commit that races with an empty queue observation.
		resumeWake := s.frameResumeDispatchWakeChannel()
		var kernelWake <-chan struct{}
		if s != nil && s.workspaceStore != nil {
			kernelWake = s.workspaceStore.KernelRetentionWake()
		}
		result, err := s.RunFrameResumeDispatchOnce(ctx, options)
		if err != nil {
			if !frameResumeDispatchDeferred(err) {
				return err
			}
			timer := time.NewTimer(idlePollInterval)
			select {
			case <-ctx.Done():
				stopRunnerIdleTimer(timer)
				return nil
			case <-resumeWake:
				stopRunnerIdleTimer(timer)
				idlePollInterval = options.PollInterval
			case <-kernelWake:
				stopRunnerIdleTimer(timer)
				idlePollInterval = options.PollInterval
			case <-timer.C:
				idlePollInterval = nextRunnerIdlePollInterval(idlePollInterval, options.PollInterval)
			}
			continue
		}
		if result.Claimed {
			idlePollInterval = options.PollInterval
			continue
		}
		timer := time.NewTimer(idlePollInterval)
		select {
		case <-ctx.Done():
			stopRunnerIdleTimer(timer)
			return nil
		case <-resumeWake:
			stopRunnerIdleTimer(timer)
			idlePollInterval = options.PollInterval
		case <-kernelWake:
			stopRunnerIdleTimer(timer)
			idlePollInterval = options.PollInterval
		case <-timer.C:
			idlePollInterval = nextRunnerIdlePollInterval(idlePollInterval, options.PollInterval)
		}
	}
}

// RunFrameResumeRecoveryLoop owns the single periodic recovery scan for all
// frame dispatch workers. It is deliberately independent from worker
// occupancy so one long task cannot starve recovery for unrelated frames.
func (s *Server) RunFrameResumeRecoveryLoop(ctx context.Context) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		log.Printf("frame_resume_recovery_unavailable server=%t workspace=%t transcript=%t", s != nil, s != nil && s.workspaceStore != nil, s != nil && s.transcriptStore != nil)
		return errors.New("frame resume recovery runtime is not configured")
	}
	log.Printf("frame_resume_recovery_started")
	if fenced, err := s.workspaceStore.ExpireCompatibilityFrameResumeDispatchesClaimedBefore(s.runtimeStartedAt); err != nil {
		log.Printf("frame_resume_startup_dispatch_fence_failed error=%v", err)
	} else if fenced > 0 {
		log.Printf("frame_resume_startup_dispatch_fenced dispatches=%d", fenced)
	}
	if fenced, err := s.transcriptStore.ExpireRunnerAttemptsClaimedBefore(ctx, s.runtimeStartedAt); err != nil {
		log.Printf("frame_resume_startup_lease_fence_failed error=%v", err)
	} else if fenced > 0 {
		log.Printf("frame_resume_startup_lease_fenced attempts=%d", fenced)
	}
	s.autoResumeInterruptedFrames(ctx)
	// Terminal runner/frame events wake recovery immediately. The timer is a
	// bounded lost-signal backstop, not a task duration or retry limit.
	recoveryWake := s.frameResumeDispatchWakeChannel()
	timer := time.NewTimer(autoResumeScanInterval)
	defer stopRunnerIdleTimer(timer)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-recoveryWake:
			recoveryWake = s.frameResumeDispatchWakeChannel()
			s.autoResumeInterruptedFrames(ctx)
			stopRunnerIdleTimer(timer)
			timer.Reset(autoResumeScanInterval)
		case <-timer.C:
			s.autoResumeInterruptedFrames(ctx)
			timer.Reset(autoResumeScanInterval)
		}
	}
}

// autoResumeInterruptedFrames scans for transcript-layer AutoResume
// checkpoints and creates compatibility frame_resumed dispatches so
// RunFrameResumeDispatchOnce can claim and execute the resume.
func (s *Server) autoResumeInterruptedFrames(ctx context.Context) {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil {
		return
	}
	candidates, err := collectFrameResumeRecoveryPages(
		ctx, autoResumeCandidateLimit, s.transcriptStore.ListAllAutoResumeCandidatesPage,
	)
	if err != nil {
		log.Printf("auto_resume_interruption_scan_failed error=%v", err)
		candidates = nil
	}
	expired, expiredErr := collectFrameResumeRecoveryPages(
		ctx, autoResumeCandidateLimit, s.transcriptStore.ListAllExpiredRunnerCandidatesPage,
	)
	if expiredErr != nil {
		log.Printf("auto_resume_expired_runner_scan_failed error=%v", expiredErr)
	} else {
		for _, stale := range expired {
			candidates = append(candidates, transcriptstore.AutoResumeCandidate{
				StreamUID: stale.StreamUID, OwnerID: stale.OwnerID, FrameID: stale.FrameID,
				Attempt: stale.Attempt, CheckpointSequence: stale.CheckpointSequence,
				ReasonCode: sessionRunnerExpiredLeaseRecoveryReasonCode,
			})
		}
	}
	unrecoverable, unrecoverableErr := collectFrameResumeRecoveryPages(
		ctx, autoResumeCandidateLimit, s.transcriptStore.ListAllExpiredUnrecoverableRunnerCandidatesPage,
	)
	if unrecoverableErr != nil {
		log.Printf("auto_resume_unrecoverable_runner_scan_failed error=%v", unrecoverableErr)
	}
	approved, approvedErr := collectFrameResumeRecoveryPages(
		ctx, autoResumeCandidateLimit, s.workspaceStore.ListExpiredApprovedKernelLocalOperationRecoveryCandidatesPage,
	)
	approvedRecoveryStreams := make(map[string]struct{}, len(approved))
	if approvedErr != nil {
		log.Printf("auto_resume_expired_approved_kernel_scan_failed error=%v", approvedErr)
	} else {
		for _, operation := range approved {
			approvedRecoveryStreams[operation.StreamUID+"\x00"+operation.OwnerUserID] = struct{}{}
			checkpoint, found, checkpointErr := s.transcriptStore.LatestResumableCheckpoint(
				ctx, operation.StreamUID, operation.OwnerUserID,
			)
			if checkpointErr != nil || !found {
				continue
			}
			candidates = prioritizeKernelOperationRecoveryCandidate(candidates, transcriptstore.AutoResumeCandidate{
				StreamUID: operation.StreamUID, OwnerID: operation.OwnerUserID, FrameID: operation.FrameID,
				Attempt: operation.SourceRunnerAttempt, CheckpointSequence: checkpoint.Sequence,
				ReasonCode: sessionRunnerKernelOperationPendingRecoveryReasonCode,
			})
		}
	}
	if unrecoverableErr == nil {
		if approvedErr != nil {
			// Terminalization is irreversible for the visible Frame. If the exact
			// approved-operation inventory is unavailable, defer the decision until
			// a later scan instead of falsely failing work that may still have a
			// durable recovery owner.
			log.Printf("auto_resume_unrecoverable_terminalization_deferred candidates=%d reason=approved_kernel_inventory_unavailable",
				len(unrecoverable))
		} else {
			for _, candidate := range unrecoverable {
				if _, recoverable := approvedRecoveryStreams[candidate.StreamUID+"\x00"+candidate.OwnerID]; recoverable {
					continue
				}
				s.terminalizeUnrecoverableRunnerCandidate(ctx, candidate)
			}
		}
	}
	s.failLegacyBlockedFrameResumeDispatches(ctx)
	if len(candidates) == 0 {
		return
	}
	for _, candidate := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Only interruptions that were durably marked AutoResume may claim the
		// next execution unit without a new user message or explicit continue.
		// The same logical task may continue across any number of durable execution
		// units. Semantic no-progress states are rate-limited when requeued rather
		// than terminalized by an arbitrary attempt count.
		if !runnerInterruptionAutoResume(candidate.ReasonCode) {
			continue
		}
		if existing, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatchByFrame(candidate.FrameID); err == nil && found {
			switch existing.Status {
			case "registered":
				// A registered dispatch can carry a notBefore fence from an earlier
				// observation that the runner lease was still healthy. If the durable
				// transcript now exposes an auto-resumable expired checkpoint, that
				// observation has been superseded. Wake the same dispatch immediately
				// instead of leaving the UI in a running state with no execution entity
				// until the old lease horizon. The dispatch remains the single owner and
				// replays the latest transcript, so no competing resume path is created.
				if autoResumeCandidateSupersedesDispatchFence(existing, candidate) {
					wakeEvent, changed, wakeErr := s.workspaceStore.WakeCompatibilityFrameResumeDispatch(
						existing.ResumeEvent.ID,
					)
					if wakeErr != nil {
						log.Printf("auto_resume_registered_dispatch_wake_failed frame_id=%s event_id=%s error=%v",
							candidate.FrameID, existing.ResumeEvent.ID, wakeErr)
					} else if changed {
						if publishErr := s.publishWorkspaceEvent(wakeEvent); publishErr != nil {
							log.Printf("auto_resume_registered_dispatch_wake_publish_failed frame_id=%s event_id=%s error=%v",
								candidate.FrameID, existing.ResumeEvent.ID, publishErr)
						}
						s.signalFrameResumeDispatchForEvent(wakeEvent.Type)
					}
				}
				continue
			case "claimed":
				continue
			}
		}
		event, err := s.createAutoResumeDispatch(ctx, candidate)
		if err != nil {
			log.Printf("auto_resume_dispatch_create_failed frame_id=%s reason_code=%s error=%v",
				candidate.FrameID, candidate.ReasonCode, err)
			continue
		}
		if event == nil {
			// The candidate may have reached an intentional waiting or terminal
			// frame state between the recovery scan and dispatch creation. There is
			// no recovery failure to surface and no dispatch to register; the next
			// user/approval wake path remains authoritative.
			continue
		}
		if err := s.registerFrameResumeDispatch(event.ID, defaultFrameResumeReservationTTL); err != nil {
			log.Printf("auto_resume_dispatch_register_failed frame_id=%s event_id=%s reason_code=%s error=%v",
				candidate.FrameID, event.ID, candidate.ReasonCode, err)
		}
	}
}

func collectFrameResumeRecoveryPages[T any](
	ctx context.Context,
	pageSize int,
	load func(context.Context, int, int) ([]T, error),
) ([]T, error) {
	if ctx == nil || pageSize <= 0 || load == nil {
		return nil, errors.New("frame resume recovery page loader is invalid")
	}
	result := make([]T, 0, pageSize)
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := load(ctx, offset, pageSize)
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		offset += len(page)
		if len(page) < pageSize {
			return result, nil
		}
	}
}

// autoResumeCandidateSupersedesDispatchFence distinguishes a genuinely newer
// recovery cause from the interruption checkpoint emitted by the dispatch that
// installed the fence itself. Without this causal comparison, the periodic
// scan sees that same checkpoint every cycle, clears its own notBefore fence,
// and hot-reclaims one healthy foreground kernel operation forever. Kernel
// approval and terminal settlement have dedicated exact-operation wake paths;
// an unchanged recovery cause must wait for those events or the long lost-wake
// backstop.
func autoResumeCandidateSupersedesDispatchFence(
	existing workspace.CompatibilityFrameResumeDispatch,
	candidate transcriptstore.AutoResumeCandidate,
) bool {
	if existing.NotBefore.IsZero() {
		return false
	}
	dispatch, ok := existing.ResumeEvent.Payload["dispatch"].(map[string]any)
	if !ok {
		return true
	}
	existingReason, _ := dispatch["interruptionReasonCode"].(string)
	existingReason = strings.TrimSpace(existingReason)
	existingRunnerAttempt, hasRunnerAttempt := exactPositiveInt(dispatch["runnerAttempt"])
	existingCheckpoint, hasCheckpoint := exactPositiveInt(dispatch["checkpointEventId"])
	if existingReason == "" || !hasRunnerAttempt || !hasCheckpoint {
		return true
	}
	candidateReason := strings.TrimSpace(candidate.ReasonCode)
	if frameResumeRecoveryCauseEquivalent(existingReason, candidateReason) &&
		candidate.Attempt == int64(existingRunnerAttempt) {
		return false
	}
	return candidate.Attempt > int64(existingRunnerAttempt) ||
		candidate.CheckpointSequence > int64(existingCheckpoint) ||
		candidateReason != existingReason
}

// frameResumeRecoveryCauseEquivalent keeps the public waiting status and its
// internal durable recovery classification in one causal family. A runner
// parked for local approval is not a new recovery cause merely because the
// transcript candidate names the exact kernel-operation recovery path.
func frameResumeRecoveryCauseEquivalent(existingReason, candidateReason string) bool {
	existingReason = strings.TrimSpace(existingReason)
	candidateReason = strings.TrimSpace(candidateReason)
	return existingReason == candidateReason ||
		(existingReason == "awaiting_approval" &&
			(candidateReason == sessionRunnerKernelOperationPendingRecoveryReasonCode ||
				candidateReason == sessionRunnerExpiredLeaseRecoveryReasonCode))
}

func (s *Server) terminalizeUnrecoverableRunnerCandidate(
	ctx context.Context,
	candidate transcriptstore.AutoResumeCandidate,
) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || strings.TrimSpace(candidate.FrameID) == "" {
		return
	}
	frame, found, err := s.workspaceStore.GetFrame(candidate.FrameID)
	if err != nil || !found || isFrameResumeTerminalStatus(frame.Status) {
		return
	}
	state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(ctx, candidate.StreamUID, candidate.OwnerID)
	if err != nil || !found || state.Status != "running" || state.ExpiresAt.After(time.Now().UTC()) {
		return
	}
	failed := "failed"
	if _, err := s.workspaceStore.UpdateFrame(candidate.FrameID, workspace.UpdateFrameInput{Status: &failed}); err != nil {
		log.Printf("unrecoverable_runner_terminalize_failed frame_id=%s stage=frame error=%v", candidate.FrameID, err)
		return
	}
	reason := strings.TrimSpace(candidate.ReasonCode)
	if reason == "" {
		reason = "unrecoverable_runner_recovery"
	}
	_ = s.workspaceStore.UpdateFrameRuntimePresentation(candidate.FrameID, workspace.FrameRuntimePresentationInput{
		StatusDescription: &reason,
	})
	if _, err := s.transcriptStore.ReconcileTerminalFrameRunnerAttempts(ctx, autoResumeCandidateLimit); err != nil {
		log.Printf("unrecoverable_runner_terminalize_failed frame_id=%s stage=transcript error=%v", candidate.FrameID, err)
	}
}

// prioritizeKernelOperationRecoveryCandidate gives a durable approved local
// operation precedence over a generic expired-runner candidate for the same
// stream. Otherwise an exhausted lease bounce can consume the dedupe slot and
// permanently suppress the operation that must be executed or terminalized.
func prioritizeKernelOperationRecoveryCandidate(
	candidates []transcriptstore.AutoResumeCandidate,
	kernelRecovery transcriptstore.AutoResumeCandidate,
) []transcriptstore.AutoResumeCandidate {
	for index, candidate := range candidates {
		if candidate.StreamUID == kernelRecovery.StreamUID && candidate.OwnerID == kernelRecovery.OwnerID {
			candidates[index] = kernelRecovery
			return candidates
		}
	}
	return append(candidates, kernelRecovery)
}

func (s *Server) failLegacyBlockedFrameResumeDispatches(ctx context.Context) {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil {
		return
	}
	dispatches, err := s.workspaceStore.ListLegacyBlockedCompatibilityFrameResumeDispatches(autoResumeCandidateLimit)
	if err != nil {
		log.Printf("legacy_blocked_resume_scan_failed error=%v", err)
		return
	}
	for _, dispatch := range dispatches {
		select {
		case <-ctx.Done():
			return
		default:
		}
		failed, resumed, failErr := s.workspaceStore.FailCompatibilityFrameResumeDispatch(
			dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, "legacy_blocked_runtime",
		)
		if failErr != nil {
			log.Printf("legacy_blocked_resume_fail_failed frame_id=%s event_id=%s error=%v", dispatch.FrameID, dispatch.ResumeEvent.ID, failErr)
			continue
		}
		if resumed {
			log.Printf("legacy_blocked_resume_unexpected_model_wake frame_id=%s event_id=%s", dispatch.FrameID, dispatch.ResumeEvent.ID)
			continue
		}
		if publishErr := s.publishWorkspaceEvent(failed); publishErr != nil {
			log.Printf("legacy_blocked_resume_publish_failed frame_id=%s event_id=%s error=%v", dispatch.FrameID, dispatch.ResumeEvent.ID, publishErr)
		}
	}
}

// createAutoResumeDispatch creates a frame_resumed event for an
// auto-resumable interrupted frame and returns it for dispatch registration.
func (s *Server) createAutoResumeDispatch(ctx context.Context, candidate transcriptstore.AutoResumeCandidate) (*workspace.FrameEvent, error) {
	if s == nil || s.workspaceStore == nil {
		return nil, errors.New("workspace store is not configured")
	}
	frame, found, err := s.workspaceStore.GetFrame(candidate.FrameID)
	if err != nil || !found {
		return nil, fmt.Errorf("auto-resume frame %q not found", candidate.FrameID)
	}
	// Failed is intentionally admitted here: CreateAutoResumeDispatch performs
	// the atomic reactivation and dispatch insert for a recoverable interrupted
	// task. Returning an empty result for failed frames left resumable transcript
	// checkpoints in a hot scan loop and stranded the task behind a visible
	// failure even though the durable recovery contract allowed continuation.
	if frame.Status != "processing" && frame.Status != "running" && frame.Status != workspace.FrameStatusFailed {
		return nil, nil
	}
	result, err := s.workspaceStore.CreateAutoResumeDispatch(
		candidate.FrameID, frame.RootFrameID, frame.ProjectID, frame.AgentName, candidate.ReasonCode,
		workspace.AutoResumeDispatchTranscriptCoordinate{
			RunnerAttempt: candidate.Attempt, CheckpointEventID: candidate.CheckpointSequence,
		},
	)
	if err != nil {
		return nil, err
	}
	if result.Event == nil {
		return nil, errors.New("auto-resume dispatch event was not created")
	}
	return result.Event, nil
}

func frameResumeDispatchDeferred(err error) bool {
	return errors.Is(err, errFrameResumeRunnerStillActive) ||
		errors.Is(err, errFrameResumeDispatchClaimLost) ||
		isTransientSQLiteContention(err)
}

func frameResumeDispatchAutoResumePolicy(
	reasonCode string,
	repetition int64,
	jitterKey string,
	now time.Time,
) time.Time {
	if strings.TrimSpace(reasonCode) == sessionRunnerKernelOperationPendingRecoveryReasonCode {
		// Durable kernel recovery owns prepared/started operation reconciliation.
		// A dispatch attempt also counts ordinary Ask User and approval wakeups, so
		// it is not a failure budget for this operation. Give durable recovery one
		// backstop interval before another runner inspects the same batch; the
		// transcript interruption query independently preserves the logical-task
		// continuation while this lost-wake fence avoids a hot recovery loop.
		return now.UTC().Add(frameResumeKernelRecoveryRecheckInterval)
	}
	if runnerInterruptionIsProgressBoundary(reasonCode) {
		// The dispatch attempt counts bounded execution segments, but each segment
		// made durable tool progress. Requeue immediately; semantic no-progress is
		// rate-limited separately without a logical-task continuation ceiling.
		return time.Time{}
	}
	if !runnerInterruptionNeedsRecoveryBackoff(reasonCode) {
		return time.Time{}
	}
	if repetition < 1 {
		repetition = 1
	}
	if strings.TrimSpace(reasonCode) == sessionRunnerProviderTransportTemporaryReasonCode {
		delay := autoResumeProviderTransportBackoffBase
		for step := int64(1); step < repetition && delay < autoResumeProviderTransportBackoffMax; step++ {
			delay *= 2
			if delay > autoResumeProviderTransportBackoffMax {
				delay = autoResumeProviderTransportBackoffMax
			}
		}
		return now.UTC().Add(delay + frameResumeDispatchDeterministicJitter(
			repetition, reasonCode+"\x00"+jitterKey, delay/2,
		))
	}
	delay := autoResumeNoProgressBackoffBase
	for step := int64(1); step < repetition && delay < autoResumeNoProgressBackoffMax; step++ {
		delay = delay * 3 / 2
		if delay > autoResumeNoProgressBackoffMax {
			delay = autoResumeNoProgressBackoffMax
		}
	}
	return now.UTC().Add(delay + frameResumeDispatchDeterministicJitter(
		repetition, jitterKey, autoResumeNoProgressBackoffJitterMax,
	))
}

func frameResumeDispatchDeterministicJitter(
	repetition int64,
	jitterKey string,
	window time.Duration,
) time.Duration {
	if window <= 0 {
		return 0
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(strings.TrimSpace(jitterKey)))
	_, _ = hasher.Write([]byte(fmt.Sprintf(":%d", repetition)))
	jitterWindowMs := uint32(window / time.Millisecond)
	return time.Duration(hasher.Sum32()%(jitterWindowMs+1)) * time.Millisecond
}
