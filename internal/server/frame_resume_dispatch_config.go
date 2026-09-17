package server

import (
	"context"
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func frameResumeRunnerShouldAutoResume(
	transcriptFrame bool,
	result SessionRunnerCycleResult,
	reasonCode string,
) bool {
	if strings.TrimSpace(reasonCode) == sessionRunnerKernelOperationPendingRecoveryReasonCode {
		// A prepared/started kernel operation is durable in the workspace store
		// and owns its own recovery/terminalization contract. It is a progress
		// boundary, never a semantic retry decision: the runner must wait for and
		// reconcile that exact operation instead of failing the whole Frame with
		// a running tool item left behind.
		return true
	}
	if transcriptFrame {
		return result.InterruptionAutoResume
	}
	return result.InterruptionAutoResume || runnerInterruptionAutoResume(reasonCode)
}

func (s *Server) failUncertainFrameResumeDispatch(
	result *FrameResumeDispatchResult,
	dispatch workspace.CompatibilityFrameResumeDispatch,
) error {
	failed, _, err := s.workspaceStore.FailCompatibilityFrameResumeDispatch(
		dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, "tool_outcome_uncertain",
	)
	if err != nil {
		return err
	}
	result.Status = "failed"
	return s.publishWorkspaceEvent(failed)
}

func (s *Server) configureTranscriptFrameResume(
	ctx context.Context,
	dispatch workspace.CompatibilityFrameResumeDispatch,
	chat *SessionRunnerChatOptions,
) error {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil || chat == nil {
		return nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(dispatch.FrameID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("resume frame %q is not available", dispatch.FrameID)
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, dispatch.FrameID)
	if err != nil {
		return err
	}
	if !found {
		return transcriptstore.ErrSchemaUnavailable
	}
	runtimeConfig := map[string]any{"agentName": dispatch.AgentName}
	if controls, ok := dispatch.ResumeEvent.Payload["controls"].(map[string]any); ok {
		for key, value := range controls {
			runtimeConfig[key] = value
		}
	}
	chat.RuntimeSessionConfig = runtimeConfig
	if frameResumeUsesPlanApprovalCheckpoint(dispatch) {
		expectedAttempt, validAttempt := exactPositiveInt(dispatch.ResumeEvent.Payload["runnerAttempt"])
		expectedSequence, validSequence := exactPositiveInt(dispatch.ResumeEvent.Payload["checkpointSequence"])
		if !validAttempt || !validSequence {
			return transcriptstore.ErrCheckpointUnavailable
		}
		checkpoint, found, err := s.transcriptStore.GetResumableCheckpoint(
			ctx, stream.UID, frameContext.UserID, int64(expectedAttempt), int64(expectedSequence),
		)
		if err != nil {
			return err
		}
		if !found || checkpoint.Phase != transcriptstore.RunnerPhaseWaitingApproval {
			return transcriptstore.ErrCheckpointUnavailable
		}
		chat.TranscriptResumeSource = transcriptstore.ResumeSourceCheckpoint
		chat.TranscriptCheckpoint = checkpoint.Sequence
		return nil
	}
	checkpoint, found, err := s.transcriptStore.LatestResumableCheckpoint(ctx, stream.UID, frameContext.UserID)
	if err != nil {
		return err
	}
	if !found {
		chat.TranscriptResumeSource = transcriptstore.ResumeSourceUserInput
		chat.TranscriptCheckpoint = 0
		return nil
	}
	// A checkpoint is superseded only by a NEW user message that the latest
	// attempt has not already claimed. The stream's consumed revision is not
	// the right signal here: ordinary runner checkpoints do not advance it, so
	// an interrupted attempt would otherwise look like unclaimed input and
	// force a fresh parent attempt, breaking the provider continuation lease.
	state, err := s.transcriptStore.GetRunnerRuntimeState(ctx, stream.UID, frameContext.UserID, checkpoint.Attempt)
	if err != nil {
		return err
	}
	if stream.InputRevision > state.ClaimedInputRevision {
		inputType, err := s.transcriptStore.LatestRunnerInputEventType(ctx, stream.UID, frameContext.UserID)
		if err != nil {
			return err
		}
		if inputType == "user_message" {
			// A fresh task message supersedes the old interruption checkpoint:
			// the checkpoint-resume contract only accepts user_input_response
			// as a newer input, so replaying a checkpoint after a fresh
			// message would fail. Start a new attempt from the new message.
			chat.TranscriptResumeSource = transcriptstore.ResumeSourceUserInput
			chat.TranscriptCheckpoint = 0
			return nil
		}
	}
	chat.TranscriptResumeSource = transcriptstore.ResumeSourceCheckpoint
	chat.TranscriptCheckpoint = checkpoint.Sequence
	return nil
}

func frameResumeUsesPlanApprovalCheckpoint(dispatch workspace.CompatibilityFrameResumeDispatch) bool {
	return dispatch.Attempt == 1 &&
		strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["reason"])) == "plan_approved"
}
