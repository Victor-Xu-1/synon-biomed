package server

import (
	"context"
	"errors"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
)

// settleSessionRunnerPreparationError closes the gap between durable runner
// claim and provider execution. Runtime drain and infrastructure cancellation
// must remain resumable in this window just as they are during model/tool work;
// otherwise a deploy can turn a valid checkpoint into a false terminal failure.
func (s *Server) settleSessionRunnerPreparationError(
	ctx context.Context,
	preparationErr error,
	stage string,
	timeout time.Duration,
	options SessionRunnerChatOptions,
	result *SessionRunnerCycleResult,
	activeRun *activeSessionRun,
	projectionClaim sessionstore.RunnerMutationClaim,
	transcriptAuthority *transcriptRunnerAuthority,
) (bool, error) {
	if drained, err := s.interruptSessionRunnerForRuntimeDrain(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); drained {
		return true, err
	}
	if interrupted, err := s.interruptSessionRunnerForInfrastructureFailure(
		ctx, options, result, activeRun, projectionClaim, transcriptAuthority,
	); interrupted {
		return true, err
	}
	if reasonCode, resumeDetail, bounded := sessionRunnerBoundedCorrectionDetails(preparationErr); bounded {
		return true, s.interruptClaimedSessionRunner(
			options, result, activeRun, projectionClaim, transcriptAuthority, reasonCode, resumeDetail,
		)
	}
	if ctx.Err() == nil && isTransientSQLiteContention(preparationErr) {
		return true, s.interruptClaimedSessionRunner(options, result, activeRun, projectionClaim, transcriptAuthority,
			sessionRunnerStoreContentionReasonCode, "preparation could not acquire the persistence lock; preserve the owned claim and resume from durable state")
	}
	if !errors.Is(context.Cause(ctx), errSessionRunnerPreparationDeadline) {
		return false, nil
	}
	correction := sessionRunnerPreparationTimeout{
		stage: stage, timeout: timeout, cause: context.DeadlineExceeded,
	}
	reasonCode, resumeDetail := correction.runnerCorrection()
	return true, s.interruptClaimedSessionRunner(
		options, result, activeRun, projectionClaim, transcriptAuthority, reasonCode, resumeDetail,
	)
}
