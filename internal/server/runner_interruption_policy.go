package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	"time"
)

func providerResponseHeadersTimeoutWithoutProgress(err error, acceptedBytes int64, hasDurableContinuation bool) bool {
	return err != nil && acceptedBytes == 0 && !hasDurableContinuation &&
		strings.Contains(strings.ToLower(err.Error()), "provider stream response headers timeout")
}

func (s *Server) settleRunnerRegistrationDuringDrain(
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	transcriptFrame bool,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) error {
	if result == nil {
		return errors.New("runner registration drain requires a result")
	}
	if transcriptFrame && transcriptAuthority == nil {
		// An explicit frame resume resolves its Transcript stream before it
		// claims an attempt. If shutdown wins in that interval there is no claim
		// to interrupt; leave the durable resume dispatch recoverable.
		result.Status = "interrupted"
		result.InterruptionReasonCode = "runtime_draining"
		return nil
	}
	return s.interruptClaimedSessionRunner(
		options, result, &activeSessionRun{}, projectionClaim, transcriptAuthority, "runtime_draining",
	)
}

func (s *Server) interruptSessionRunnerForRuntimeDrain(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) (bool, error) {
	if !errors.Is(context.Cause(ctx), ErrRuntimeDraining) && (activeRun == nil || !activeRun.draining.Load()) {
		return false, nil
	}
	return true, s.interruptClaimedSessionRunner(
		options, result, activeRun, projectionClaim, transcriptAuthority, "runtime_draining",
	)
}

func (s *Server) interruptSessionRunnerForRuntimeDrainLocked(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) (bool, error) {
	if !errors.Is(context.Cause(ctx), ErrRuntimeDraining) && (activeRun == nil || !activeRun.draining.Load()) {
		return false, nil
	}
	return true, s.interruptClaimedSessionRunnerLocked(
		options, result, activeRun, projectionClaim, transcriptAuthority, "runtime_draining",
	)
}

func (s *Server) interruptSessionRunnerForInfrastructureFailure(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) (bool, error) {
	interruption, found := sessionRunnerInfrastructureInterruptionFromContext(ctx)
	if !found {
		return false, nil
	}
	return true, s.interruptClaimedSessionRunner(
		options, result, activeRun, projectionClaim, transcriptAuthority,
		interruption.ReasonCode, interruption.Detail,
	)
}

func (s *Server) interruptSessionRunnerForInfrastructureFailureLocked(
	ctx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) (bool, error) {
	interruption, found := sessionRunnerInfrastructureInterruptionFromContext(ctx)
	if !found {
		return false, nil
	}
	return true, s.interruptClaimedSessionRunnerLocked(
		options, result, activeRun, projectionClaim, transcriptAuthority,
		interruption.ReasonCode, interruption.Detail,
	)
}

func (s *Server) interruptClaimedSessionRunner(
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	reasonCode string,
	resumeDetails ...string,
) error {
	if activeRun == nil || result == nil {
		return errors.New("runner interruption requires an active runner result")
	}
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" {
		return errors.New("runner interruption reason is required")
	}
	return withActiveRunSettlementContext(activeRun, 2*time.Second, func(persistCtx context.Context) error {
		return s.interruptClaimedSessionRunnerLockedWithContext(
			persistCtx, options, result, activeRun, projectionClaim, transcriptAuthority, reasonCode, resumeDetails...,
		)
	})
}

func withActiveRunSettlementContext(
	activeRun *activeSessionRun,
	timeout time.Duration,
	operation func(context.Context) error,
) error {
	if activeRun == nil || operation == nil || timeout <= 0 {
		return errors.New("active runner settlement operation and positive timeout are required")
	}
	activeRun.settlement.Lock()
	defer activeRun.settlement.Unlock()
	persistCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return operation(persistCtx)
}

func (s *Server) interruptClaimedSessionRunnerLocked(
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	reasonCode string,
	resumeDetails ...string,
) error {
	persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.interruptClaimedSessionRunnerLockedWithContext(
		persistCtx, options, result, activeRun, projectionClaim, transcriptAuthority, reasonCode, resumeDetails...,
	)
}

func (s *Server) interruptClaimedSessionRunnerLockedWithContext(
	persistCtx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	reasonCode string,
	resumeDetails ...string,
) error {
	return s.interruptClaimedSessionRunnerLockedWithPolicy(
		persistCtx, options, result, activeRun, projectionClaim, transcriptAuthority,
		reasonCode, runnerInterruptionAutoResume(reasonCode), resumeDetails...,
	)
}

func (s *Server) interruptClaimedSessionRunnerLockedWithPolicy(
	persistCtx context.Context,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	reasonCode string,
	autoResume bool,
	resumeDetails ...string,
) error {
	if err := markActiveSessionRunnerPaused(activeRun); err != nil {
		return err
	}
	resumeDetail := ""
	if len(resumeDetails) > 0 {
		resumeDetail = strings.TrimSpace(resumeDetails[0])
	}
	if activeRun.settled {
		result.Status = "interrupted"
		if result.InterruptionReasonCode == "" {
			result.InterruptionReasonCode = reasonCode
		}
		result.InterruptionAutoResume = result.InterruptionAutoResume || autoResume
		return nil
	}
	if transcriptAuthority != nil {
		interrupted, err := s.transcriptStore.InterruptRunner(persistCtx, transcriptstore.InterruptRunnerInput{
			Claim:                    transcriptAuthority.Claim,
			ClientMessageID:          transcriptRunnerClientMessageID(transcriptAuthority.Claim, reasonCode),
			ReasonCode:               reasonCode,
			ResumeDetail:             resumeDetail,
			RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
			Resumable:                reasonCode == sessionRunnerModelProviderUnavailableReasonCode,
			AutoResume:               autoResume,
			Destinations:             transcriptRunnerDestinations(transcriptAuthority),
		})
		if err != nil {
			return fmt.Errorf("persist resumable runner interruption: %w", err)
		}
		if len(transcriptRunnerDestinations(transcriptAuthority)) > 0 {
			s.signalTranscriptWebDelivery()
		}
		result.CheckpointEventID = interrupted.Event.EventID
	} else {
		checkpointPayload := map[string]any{
			"sessionId": result.SessionID, "runnerId": options.RunnerID,
			"runnerAttempt": result.Attempt, "claimToken": projectionClaim.ClaimToken,
			"status": "running", "message": "runner interrupted; resumable lease released",
			"reasonCode":   reasonCode,
			"afterEventId": result.CheckpointEventID,
			"clientMessageId": runnerCommandClientMessageID(
				options.RunnerID, result.SessionID, result.Attempt, projectionClaim.ClaimToken, reasonCode,
			),
		}
		if resumeDetail != "" {
			checkpointPayload["resumeDetail"] = resumeDetail
		}
		checkpoint, err := s.checkpointSessionRunner(checkpointPayload)
		if err != nil {
			return fmt.Errorf("persist legacy runner interruption: %w", err)
		}
		if event, ok := checkpoint["event"].(*eventjournal.Entry); ok && event != nil {
			result.CheckpointEventID = event.EventID
		}
		if _, released, err := s.sessionStore.ReleaseRunner(projectionClaim); err != nil {
			return fmt.Errorf("release interrupted legacy runner lease: %w", err)
		} else if !released {
			return errors.New("interrupted legacy runner no longer owns its lease")
		}
	}
	activeRun.settled = true
	result.Status = "interrupted"
	result.InterruptionReasonCode = reasonCode
	result.InterruptionAutoResume = autoResume
	return nil
}

// runnerInterruptionNeedsRecoveryBackoff identifies transient no-progress
// and semantic-correction states that may continue indefinitely but must not
// form a hot retry loop. Repetition changes only the capped scheduling delay; it never
// makes a logical task terminal or dependent on new user input. Repeated
// identical tool actions are quarantined earlier by the per-execution-unit
// tool-round guard, allowing a later durable continuation to choose a new path.
func runnerInterruptionNeedsRecoveryBackoff(reasonCode string) bool {
	switch strings.TrimSpace(reasonCode) {
	case "provider_stream_no_progress",
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"artifact_reference_correction_required",
		"completion_review_correction_required",
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerPlanStepsIncompleteReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode:
		return true
	default:
		return false
	}
}

func runnerInterruptionMayContinueSameTask(reasonCode string) bool {
	switch strings.TrimSpace(reasonCode) {
	case "runtime_draining",
		sessionRunnerSupervisorInterruptedReasonCode,
		sessionRunnerResumeDispatchInterruptedReasonCode,
		sessionRunnerToolLifecyclePersistenceReasonCode,
		sessionRunnerKernelOperationPendingRecoveryReasonCode,
		sessionRunnerToolReplaySafetyReasonCode,
		sessionRunnerKernelRecoveryTimeoutReasonCode,
		sessionRunnerPreparationTimeoutReasonCode,
		"provider_stream_interrupted",
		"provider_stream_no_progress",
		sessionRunnerProviderOutputTokenLimitReasonCode,
		sessionRunnerProviderContextPressureReasonCode,
		sessionRunnerToolRoundNoProgressReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
		sessionRunnerToolFailedReasonCode,
		sessionRunnerModelProtocolErrorReasonCode,
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerPlanStepsIncompleteReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerModelProviderUnavailableReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode,
		sessionRunnerExpiredLeaseRecoveryReasonCode,
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"artifact_reference_correction_required",
		"completion_review_correction_required":
		return true
	default:
		return false
	}
}

// runnerInterruptionAutoResume reports whether an unattended runner may claim
// the next bounded execution unit without a new user message. Reference and
// review corrections are auto-resumable because they are recoverable quality
// gates, not user-input requests. A tool-round limit is also auto-resumable: it
// is a resource boundary between execution segments, not a failed attempt. The
// dispatcher rate-limits repeated correction failures without imposing a
// continuation ceiling, while the independent per-unit no-progress guard
// quarantines repeated semantic tool loops.
func runnerInterruptionAutoResume(reasonCode string) bool {
	switch strings.TrimSpace(reasonCode) {
	case "runtime_draining",
		sessionRunnerSupervisorInterruptedReasonCode,
		sessionRunnerResumeDispatchInterruptedReasonCode,
		sessionRunnerToolLifecyclePersistenceReasonCode,
		sessionRunnerKernelOperationPendingRecoveryReasonCode,
		sessionRunnerToolReplaySafetyReasonCode,
		sessionRunnerKernelRecoveryTimeoutReasonCode,
		sessionRunnerPreparationTimeoutReasonCode,
		sessionRunnerToolFailedReasonCode,
		"provider_stream_interrupted",
		"provider_stream_no_progress",
		sessionRunnerProviderOutputTokenLimitReasonCode,
		sessionRunnerProviderContextPressureReasonCode,
		sessionRunnerToolRoundNoProgressReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
		sessionRunnerModelProtocolErrorReasonCode,
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerPlanStepsIncompleteReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode,
		sessionRunnerExpiredLeaseRecoveryReasonCode,
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"artifact_reference_correction_required",
		"completion_review_correction_required":
		return true
	default:
		return false
	}
}

// runnerInterruptionIsProgressBoundary identifies checkpoints that deliberately
// divide a progressing logical task into bounded execution segments or transfer
// its durable ownership after transient infrastructure pressure. These
// checkpoints must not be delayed as semantic no-progress failures: doing so
// would unnecessarily slow deployment, lease renewal, or SQLite recovery even
// when the task itself keeps making durable progress.
// A pending durable kernel operation is also a boundary: every operation has
// its own immutable identity, state machine, execution timeout and terminal
// receipt. Counting several independent foreground operations against one
// reason-wide ceiling would terminate a healthy long task solely because it
// used more than a few computation steps.
func runnerInterruptionIsProgressBoundary(reasonCode string) bool {
	switch strings.TrimSpace(reasonCode) {
	case "runtime_draining",
		sessionRunnerSupervisorInterruptedReasonCode,
		sessionRunnerResumeDispatchInterruptedReasonCode,
		sessionRunnerToolLifecyclePersistenceReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerExpiredLeaseRecoveryReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
		"provider_stream_interrupted",
		sessionRunnerProviderOutputTokenLimitReasonCode,
		sessionRunnerKernelOperationPendingRecoveryReasonCode:
		return true
	default:
		return false
	}
}

// sessionRunnerFailureReasonCode classifies a runner failure message into the
// durable interruption reason that governs whether the task may continue
// without user intervention. Unknown failures return "" and remain terminal.
func sessionRunnerFailureReasonCode(message string) string {
	message = strings.TrimSpace(message)
	switch {
	case strings.HasPrefix(message, "runner completion reference integrity failed"):
		return "artifact_reference_correction_required"
	case strings.Contains(message, "provider stream made no progress"):
		return "provider_stream_no_progress"
	case strings.Contains(message, "provider stream response headers timeout"):
		// A header timeout proves only that this transport attempt made no
		// semantic progress. Preserve the task and continue through the durable
		// backoff dispatcher; do not misclassify a transient network path as a
		// missing model configuration that requires user intervention.
		return sessionRunnerProviderTransportTemporaryReasonCode
	case strings.Contains(message, "provider stream interrupted"),
		strings.Contains(message, "provider stream idle timeout"),
		strings.Contains(message, "provider stream total timeout"):
		return "provider_stream_interrupted"
	case providerContextPressureFailure(message):
		return sessionRunnerProviderContextPressureReasonCode
	case isTransientSQLiteContentionMessage(message):
		return sessionRunnerStoreContentionReasonCode
	case strings.HasPrefix(message, "tool result reported failure"),
		strings.HasPrefix(message, "tool ") && strings.Contains(message, " failed"):
		return sessionRunnerToolFailedReasonCode
	case sessionRunnerVisualMediaUnsupportedFailure(message):
		return sessionRunnerVisualMediaUnsupportedReasonCode
	case sessionRunnerModelProviderQuotaExhaustedFailure(message):
		return sessionRunnerModelProviderUnavailableReasonCode
	case sessionRunnerModelProviderTemporaryFailure(message):
		return sessionRunnerModelProviderTemporaryReasonCode
	case sessionRunnerModelProviderUnavailableFailure(message):
		return sessionRunnerModelProviderUnavailableReasonCode
	default:
		return ""
	}
}

func sessionRunnerVisualMediaUnsupportedFailure(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{
		"only support text input", "only supports text input", "does not support image input",
		"image input is not supported", "unsupported image input",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func sessionRunnerModelProviderQuotaExhaustedFailure(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"quota exhausted", "quota exceeded", "insufficient quota", "insufficient_quota",
		"billing quota", "credit balance", "credits exhausted", "out of credits",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func sessionRunnerModelProviderTemporaryFailure(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"provider endpoint returned 429", "model provider returned http 429",
		"provider endpoint returned 500", "provider endpoint returned 502",
		"provider endpoint returned 503", "provider endpoint returned 504",
		"model provider returned http 500", "model provider returned http 502",
		"model provider returned http 503", "model provider returned http 504",
		"concurrent limit exceeded", "rate limit", "too many requests",
		"dial tcp", "connection refused", "connection reset by peer", "no such host",
		"tls handshake timeout", "i/o timeout", "network is unreachable",
		"server misbehaving", "unexpected eof",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == context.DeadlineExceeded.Error()
}

func sessionRunnerModelProviderUnavailableFailure(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "provider endpoint returned") ||
		strings.HasPrefix(normalized, "model provider returned http") {
		return true
	}
	if normalized == context.DeadlineExceeded.Error() {
		return true
	}
	for _, marker := range []string{
		"dial tcp", "connection refused", "connection reset by peer", "no such host",
		"tls handshake timeout", "i/o timeout", "network is unreachable",
		"server misbehaving", "unexpected eof", "no active saved model provider",
		"model provider base url", "model provider store", "model provider ",
		"provider protocol ", "provider endpoint is required", "provider model is required",
		"dynamic session model", "secret store is required", "does not contain an api key",
		"unsupported secret ref", "secret ref ",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	if strings.HasPrefix(normalized, "model ") &&
		(strings.Contains(normalized, " is not enabled") || strings.Contains(normalized, " is ambiguous")) {
		return true
	}
	if strings.HasPrefix(normalized, "secret ") && strings.Contains(normalized, " not found") {
		return true
	}
	return false
}

func providerContextPressureFailure(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	for _, marker := range []string{
		"exceed max message tokens",
		"maximum context length is",
		"maximum context length exceeded",
		"context_length_exceeded",
		"context length exceeded",
		"context window exceeded",
		"input tokens exceed",
		"prompt is too long",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func projectRuntimeSessionConfig(session sessionstore.Session, runtimeConfig map[string]any) sessionstore.Session {
	orchestration := cloneResumeOrchestration(session.Orchestration)
	config := map[string]any{}
	if existing, ok := orchestration["sessionConfig"].(map[string]any); ok {
		for key, value := range existing {
			config[key] = value
		}
	}
	for key, value := range runtimeConfig {
		config[key] = value
	}
	orchestration["sessionConfig"] = config
	session.Orchestration = orchestration
	return session
}

func (s *Server) claimNextLegacyRunnerExcludingTranscriptFrames(
	ctx context.Context,
	runnerID string,
	ttl time.Duration,
) (sessionstore.Session, bool, error) {
	backlog, err := s.sessionStore.RunnerBacklog(sessionstore.RunnerBacklogOptions{})
	if err != nil {
		return sessionstore.Session{}, false, err
	}
	for _, item := range backlog.Items {
		_, transcriptFrame, err := s.resolveTranscriptFrameStream(ctx, item.SessionID)
		if err != nil {
			return sessionstore.Session{}, false, err
		}
		if transcriptFrame {
			continue
		}
		session, claimed, err := s.sessionStore.ClaimRunner(item.SessionID, runnerID, ttl)
		if err != nil {
			return sessionstore.Session{}, false, err
		}
		if claimed {
			return session, true, nil
		}
	}
	return sessionstore.Session{}, false, nil
}

func (s *Server) startSessionRunnerChatHeartbeat(
	parent context.Context,
	claim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	options SessionRunnerChatOptions,
) (context.Context, func() error) {
	if s == nil || (transcriptAuthority == nil && s.sessionStore == nil) {
		return parent, func() error { return nil }
	}
	ticker := time.NewTicker(sessionRunnerHeartbeatInterval(options.LeaseTTL))
	return s.startSessionRunnerChatHeartbeatWithTicks(
		parent, claim, transcriptAuthority, options, ticker.C, ticker.Stop, nil,
	)
}

// startSessionRunnerChatHeartbeatWithTicks owns the lease-renewal loop while
// leaving tick scheduling to its caller. Production supplies a real ticker;
// deterministic lifecycle tests can drive and observe exact committed renewal
// boundaries without waiting for wall-clock expiry or racing machine load.
func (s *Server) startSessionRunnerChatHeartbeatWithTicks(
	parent context.Context,
	claim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
	options SessionRunnerChatOptions,
	ticks <-chan time.Time,
	stopTicks func(),
	afterRenewal func(),
) (context.Context, func() error) {
	if s == nil || ticks == nil || (transcriptAuthority == nil && s.sessionStore == nil) {
		if stopTicks != nil {
			stopTicks()
		}
		return parent, func() error { return nil }
	}
	heartbeatCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	var initialExpiry time.Time
	if transcriptAuthority != nil {
		initialExpiry = transcriptAuthority.Claim.ExpiresAt
	}
	go func() {
		expiresAt := initialExpiry
		defer close(done)
		if stopTicks != nil {
			defer stopTicks()
		}
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticks:
				if transcriptAuthority != nil {
					transcriptHeartbeat, err := s.renewTranscriptRunnerLease(heartbeatCtx, transcriptAuthority.Claim, options.LeaseTTL, expiresAt)
					// Joining the heartbeat cancels any SQLite acquisition in flight.
					// That cleanup is not a loss of the durable runner claim.
					if heartbeatCtx.Err() != nil && errors.Is(err, heartbeatCtx.Err()) {
						return
					}
					if err == nil && transcriptHeartbeat.Renewed {
						expiresAt = transcriptHeartbeat.ExpiresAt
						if afterRenewal != nil {
							afterRenewal()
						}
						continue
					}
					if err == nil {
						err = transcriptstore.ErrClaimStale
					}
					select {
					case heartbeatErr <- fmt.Errorf("renew transcript runner lease: %w", err):
					default:
					}
					cancel()
					return
				}
				session, renewed, err := s.sessionStore.HeartbeatRunner(claim, options.LeaseTTL)
				if err != nil {
					select {
					case heartbeatErr <- fmt.Errorf("renew runner chat lease: %w", err):
					default:
					}
					cancel()
					return
				}
				if renewed {
					if afterRenewal != nil {
						afterRenewal()
					}
					continue
				}
				owner := "none"
				if session.Runner != nil {
					owner = session.Runner.RunnerID
				}
				select {
				case heartbeatErr <- fmt.Errorf("runner chat lease was lost to runner %s", owner):
				default:
				}
				cancel()
				return
			}
		}
	}()
	var once sync.Once
	return heartbeatCtx, func() error {
		once.Do(cancel)
		<-done
		select {
		case err := <-heartbeatErr:
			return err
		default:
			return nil
		}
	}
}
