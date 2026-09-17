package server

import (
	"errors"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func frameResumeDispatchShouldRequeueActiveRunner(transcriptFrame bool, runErr error) bool {
	return transcriptFrame &&
		(errors.Is(runErr, errFrameResumeRunnerStillActive) ||
			errors.Is(runErr, ErrSessionRunAlreadyActive))
}

// deferClaimedFrameResumeDispatchForActiveRunner releases a Transcript-backed
// compatibility dispatch that raced the in-memory cleanup of the runner that
// just parked it. The existing runner remains the only execution authority;
// the same dispatch is returned to the durable queue so an approval, user
// response, runner settlement, or the bounded lost-wake recheck can resume it
// without waiting for the longer compatibility claim expiry. Legacy session
// claims retain their own authoritative lease recovery contract.
func (s *Server) deferClaimedFrameResumeDispatchForActiveRunner(
	dispatch workspace.CompatibilityFrameResumeDispatch,
	runnerAttempt int,
) error {
	reasonCode, runnerAttempt, checkpointEventID :=
		frameResumeActiveRunnerDeferralCoordinates(dispatch, runnerAttempt)
	event, _, err := s.workspaceStore.RequeueCompatibilityFrameResumeDispatch(
		workspace.RequeueCompatibilityFrameResumeDispatchInput{
			ResumeEventID:     dispatch.ResumeEvent.ID,
			ExpectedAttempt:   dispatch.Attempt,
			ClaimToken:        dispatch.ClaimToken,
			ReasonCode:        reasonCode,
			RunnerAttempt:     runnerAttempt,
			CheckpointEventID: checkpointEventID,
			NotBefore: time.Now().UTC().Add(
				frameResumePauseRecheckInterval,
			),
		},
	)
	if err != nil {
		return err
	}
	return s.publishWorkspaceEvent(event)
}

func frameResumeActiveRunnerDeferralCoordinates(
	dispatch workspace.CompatibilityFrameResumeDispatch,
	runnerAttempt int,
) (reasonCode string, resolvedRunnerAttempt int, checkpointEventID int64) {
	reasonCode = "runner_still_active"
	resolvedRunnerAttempt = runnerAttempt
	payload, _ := dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
	if payload == nil {
		return reasonCode, resolvedRunnerAttempt, 0
	}
	if existing := strings.TrimSpace(stringValue(payload["interruptionReasonCode"])); existing != "" {
		reasonCode = existing
	} else if existing = strings.TrimSpace(stringValue(payload["reasonCode"])); existing != "" {
		reasonCode = existing
	}
	if resolvedRunnerAttempt <= 0 {
		if existing, ok := exactPositiveInt(payload["runnerAttempt"]); ok {
			resolvedRunnerAttempt = existing
		}
	}
	if existing, ok := exactPositiveInt(payload["checkpointEventId"]); ok {
		checkpointEventID = int64(existing)
	}
	return reasonCode, resolvedRunnerAttempt, checkpointEventID
}
