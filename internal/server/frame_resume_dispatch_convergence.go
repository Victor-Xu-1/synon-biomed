package server

import (
	"context"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// convergeFrameResumeDispatchIntoRunner retires a redundant compatibility
// dispatch only when a newer executing runner has already claimed every input
// revision visible to the Frame. A runner that predates the dispatch, or one
// with newer queued input behind it, leaves the dispatch active for the normal
// deferred-delivery path.
func (s *Server) convergeFrameResumeDispatchIntoRunner(
	ctx context.Context,
	dispatch workspace.CompatibilityFrameResumeDispatch,
	stream transcriptstore.Stream,
	state transcriptstore.RunnerRuntimeState,
) (*workspace.FrameEvent, error) {
	pendingInput, found, err := s.transcriptStore.GetFramePendingInputState(
		ctx, stream.OwnerID, dispatch.FrameID,
	)
	if err != nil || !found || state.Phase != transcriptstore.RunnerPhaseExecuting ||
		!state.ClaimedAt.After(dispatch.ResumeEvent.CreatedAt) ||
		state.ClaimedInputRevision < pendingInput.InputRevision {
		return nil, err
	}

	// The newer runner is the sole execution authority. Completing only the
	// dispatch prevents repeated claim/requeue audit churn without changing the
	// active Frame's processing status.
	event, _, err := s.workspaceStore.ConvergeCompatibilityFrameResumeDispatch(
		workspace.ConvergeCompatibilityFrameResumeDispatchInput{
			ResumeEventID: dispatch.ResumeEvent.ID, ExpectedAttempt: dispatch.Attempt,
			ClaimToken: dispatch.ClaimToken, RunnerID: state.RunnerID,
			RunnerAttempt: state.Attempt, ClaimedInputRevision: state.ClaimedInputRevision,
		},
	)
	if err != nil {
		return nil, err
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		return nil, err
	}
	return &event, nil
}
