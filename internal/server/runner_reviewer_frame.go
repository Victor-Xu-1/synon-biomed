package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const sessionReviewerFrameLeaseTTL = 30 * time.Minute

func (s *Server) startSessionReviewerHeartbeat(
	parent context.Context,
	claim transcriptstore.RunnerClaim,
) (context.Context, func() error) {
	return s.startSessionReviewerHeartbeatWithTTL(parent, claim, sessionReviewerFrameLeaseTTL)
}

func (s *Server) startSessionReviewerHeartbeatWithTTL(
	parent context.Context,
	claim transcriptstore.RunnerClaim,
	ttl time.Duration,
) (context.Context, func() error) {
	if s == nil || s.transcriptStore == nil || claim.ClaimToken == "" {
		return parent, func() error { return nil }
	}
	if ttl <= 0 {
		ttl = sessionReviewerFrameLeaseTTL
	}
	heartbeatCtx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(sessionRunnerHeartbeatInterval(ttl))
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				result, err := s.transcriptStore.HeartbeatRunner(heartbeatCtx, transcriptstore.HeartbeatRunnerInput{
					Claim: claim, TTL: ttl,
				})
				if heartbeatCtx.Err() != nil && errors.Is(err, heartbeatCtx.Err()) {
					return
				}
				if err == nil && result.Renewed {
					continue
				}
				if err == nil {
					err = transcriptstore.ErrClaimStale
				}
				select {
				case heartbeatErr <- fmt.Errorf("renew reviewer runner lease: %w", err):
				default:
				}
				cancel(err)
				return
			}
		}
	}()
	var once sync.Once
	return heartbeatCtx, func() error {
		once.Do(func() { cancel(nil) })
		<-done
		select {
		case err := <-heartbeatErr:
			return err
		default:
			return nil
		}
	}
}

func runnerReviewerFrameID(sessionID string, attempt, reviewIndex int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", strings.TrimSpace(sessionID), attempt, reviewIndex)))
	return "completion-review-" + hex.EncodeToString(digest[:12])
}

func runnerFixedJobFrameID(profileName, sessionID string, attempt, reviewIndex int) string {
	profileName = strings.ToUpper(strings.TrimSpace(profileName))
	if profileName == "REVIEWER" {
		return runnerReviewerFrameID(sessionID, attempt, reviewIndex)
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", profileName, strings.TrimSpace(sessionID), attempt, reviewIndex)))
	return "completion-" + strings.ToLower(profileName) + "-" + hex.EncodeToString(digest[:12])
}

// bindSessionReviewerRun gives fixed-job tools the reviewer Frame's own
// transcript claim. Without this boundary, the runtime gateway inherits the
// root task claim while resolving a reviewer kernel identity, so every local
// reviewer operation fails the Frame-authority check.
func (s *Server) bindSessionReviewerRun(
	ctx context.Context,
	frame workspace.Frame,
	claim transcriptstore.RunnerClaim,
) (context.Context, error) {
	if s == nil || s.transcriptStore == nil || strings.TrimSpace(frame.ID) == "" ||
		strings.TrimSpace(frame.ParentFrameID) == "" || frame.ConversationType != "delegate" ||
		strings.TrimSpace(claim.StreamUID) == "" || strings.TrimSpace(claim.OwnerID) == "" ||
		strings.TrimSpace(claim.ClaimToken) == "" || claim.Attempt <= 0 {
		return ctx, errors.New("completion reviewer runner authority is unavailable")
	}
	stream, err := s.transcriptStore.GetStream(ctx, claim.StreamUID, claim.OwnerID)
	if err != nil {
		return ctx, fmt.Errorf("load completion reviewer transcript: %w", err)
	}
	if stream.Kind != transcriptstore.StreamKindFrameRef || stream.FrameID != frame.ID ||
		stream.SessionID != frame.ID || stream.OwnerID != claim.OwnerID ||
		stream.ProjectID != frame.ProjectID || stream.RootFrameID != frame.RootFrameID {
		return ctx, errors.New("completion reviewer transcript authority does not match the reviewer Frame")
	}
	reviewerRun := &sessionRunnerChatRun{
		SessionID: frame.ID, Attempt: int(claim.Attempt), ClaimToken: claim.ClaimToken,
		AfterEventID:  claim.ResumeCheckpoint,
		Transcript:    &transcriptRunnerAuthority{Stream: stream, Claim: claim},
		frameOwnedJob: true,
	}
	return withTranscriptRunnerChatRun(ctx, reviewerRun), nil
}

func (s *Server) beginSessionReviewerFrame(
	ctx context.Context,
	session sessionstore.Session,
	model string,
	attempt, reviewIndex int,
) (workspace.Frame, error) {
	frame, _, err := s.beginSessionReviewerFrameWithClaim(ctx, session, model, attempt, reviewIndex)
	return frame, err
}

func (s *Server) beginSessionReviewerFrameWithClaim(
	ctx context.Context,
	session sessionstore.Session,
	model string,
	attempt, reviewIndex int,
) (workspace.Frame, transcriptstore.RunnerClaim, error) {
	spec, err := s.sessionCompletionReviewExecutionSpec()
	if err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, err
	}
	return s.beginSessionReviewFrameWithClaim(
		ctx, session, model, attempt, reviewIndex, spec,
	)
}

func (s *Server) beginSessionReviewFrame(
	ctx context.Context,
	session sessionstore.Session,
	model string,
	attempt, reviewIndex int,
	spec sessionRunnerReviewExecutionSpec,
) (workspace.Frame, error) {
	frame, _, err := s.beginSessionReviewFrameWithClaim(ctx, session, model, attempt, reviewIndex, spec)
	return frame, err
}

func (s *Server) beginSessionReviewFrameWithClaim(
	ctx context.Context,
	session sessionstore.Session,
	model string,
	attempt, reviewIndex int,
	spec sessionRunnerReviewExecutionSpec,
) (workspace.Frame, transcriptstore.RunnerClaim, error) {
	if s == nil || s.workspaceStore == nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, errors.New("workspace store is required for reviewer frames")
	}
	if strings.TrimSpace(spec.ReviewKind) == "" || strings.TrimSpace(spec.ProfileName) == "" ||
		(spec.ReviewTrigger != "auto" && spec.ReviewTrigger != "manual") {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, errors.New("reviewer frame specification is invalid")
	}
	var parent workspace.Frame
	var found bool
	err := retryTransientStoreContention(ctx, "load_reviewer_parent", func() error {
		var loadErr error
		parent, found, loadErr = s.workspaceStore.GetFrame(strings.TrimSpace(session.ID))
		return loadErr
	})
	if err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, err
	}
	if !found {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("review target frame %q does not exist", session.ID)
	}
	frameID := runnerFixedJobFrameID(spec.ProfileName, session.ID, attempt, reviewIndex)
	frameName := fmt.Sprintf("Completion %s %d", strings.ToLower(spec.ProfileName), reviewIndex+1)
	description := "Independent " + strings.ToLower(spec.ProfileName) + " fixed job is running"
	inputText := spec.ProfileName + " completion checkpoint for frame " + parent.ID
	messageOrigin := "internal_" + strings.ToLower(spec.ProfileName)
	var frame workspace.Frame
	err = retryTransientStoreContention(ctx, "load_reviewer_frame", func() error {
		var loadErr error
		frame, found, loadErr = s.workspaceStore.GetFrame(frameID)
		return loadErr
	})
	if err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, err
	}
	created := false
	if !found {
		err = retryTransientStoreContention(ctx, "create_reviewer_frame", func() error {
			var createErr error
			frame, createErr = s.workspaceStore.CreateFrame(workspace.CreateFrameInput{
				ID: frameID, ProjectID: parent.ProjectID, ParentFrameID: parent.ID,
				AgentName: spec.ProfileName, Status: "processing", ConversationType: "delegate",
				Name: frameName,
			})
			return createErr
		})
		if err != nil {
			return workspace.Frame{}, transcriptstore.RunnerClaim{}, err
		}
		created = true
	} else if frame.ProjectID != parent.ProjectID || frame.ParentFrameID != parent.ID ||
		frame.RootFrameID != parent.RootFrameID || !strings.EqualFold(frame.AgentName, spec.ProfileName) ||
		frame.ConversationType != "delegate" {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("reviewer frame %q conflicts with an existing frame", frameID)
	} else if !strings.EqualFold(frame.Status, "processing") {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("reviewer frame %q is already terminal with status %q", frameID, frame.Status)
	}

	var reviewerClaim transcriptstore.RunnerClaim
	if err := retryTransientStoreContention(ctx, "start_reviewer_transcript", func() error {
		var startErr error
		reviewerClaim, startErr = s.beginSessionReviewerTranscript(
			ctx, frame, parent, attempt, reviewIndex, spec, inputText, messageOrigin,
		)
		return startErr
	}); err != nil {
		if created {
			deleteErr := retryTransientStoreContention(ctx, "remove_incomplete_reviewer_frame", func() error {
				return s.workspaceStore.DeleteFrame(frame.ID)
			})
			if deleteErr != nil {
				return workspace.Frame{}, transcriptstore.RunnerClaim{}, errors.Join(
					fmt.Errorf("start reviewer transcript: %w", err),
					fmt.Errorf("remove incomplete reviewer frame: %w", deleteErr),
				)
			}
		}
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("start reviewer transcript: %w", err)
	}
	cleanup := func(cause error) error {
		finishErr := s.finishSessionReviewerFrame(frame.ID, "failed", cause.Error(), reviewIndex)
		if finishErr != nil {
			return errors.Join(cause, fmt.Errorf("settle reviewer setup: %w", finishErr))
		}
		return cause
	}
	input := map[string]any{
		"_review_target_frame_id": parent.ID,
		"kind":                    spec.ReviewKind,
		"review_trigger":          spec.ReviewTrigger,
		"reviewer_profile":        spec.ProfileName,
		"review_index":            reviewIndex,
		"runner_attempt":          attempt,
	}
	if err := retryTransientStoreContention(ctx, "persist_reviewer_input", func() error {
		return s.workspaceStore.SetFrameSubmissionMetadata(frame.ID, input, true)
	}); err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("persist reviewer input: %w", cleanup(err))
	}
	model = strings.TrimSpace(model)
	if err := retryTransientStoreContention(ctx, "persist_reviewer_presentation", func() error {
		return s.workspaceStore.UpdateFrameRuntimePresentation(
			frame.ID, workspace.FrameRuntimePresentationInput{Model: &model, StatusDescription: &description},
		)
	}); err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("persist reviewer presentation: %w", cleanup(err))
	}
	var event workspace.FrameEvent
	eventPrefix, eventType := "runner-reviewer:", "verification_started"
	if strings.EqualFold(spec.ProfileName, "BOOKMARKER") {
		eventPrefix, eventType = "runner-bookmarker:", "bookmarking_started"
	}
	err = retryTransientStoreContention(ctx, "persist_reviewer_start_event", func() error {
		var appendErr error
		event, appendErr = s.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
			ID: eventPrefix + frame.ID + ":started", FrameID: frame.ID, Type: eventType,
			Payload: map[string]any{
				"status": "processing", "targetFrameId": parent.ID,
				"reviewIndex": reviewIndex, "runnerAttempt": attempt, "model": model,
				"reviewKind": spec.ReviewKind, "reviewerProfile": spec.ProfileName,
				"reviewTrigger": spec.ReviewTrigger,
			},
		})
		return appendErr
	})
	if err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("persist reviewer start event: %w", cleanup(err))
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		return workspace.Frame{}, transcriptstore.RunnerClaim{}, fmt.Errorf("publish reviewer start event: %w", cleanup(err))
	}
	return frame, reviewerClaim, nil
}

func (s *Server) beginSessionReviewerTranscript(
	ctx context.Context,
	frame, parent workspace.Frame,
	attempt, reviewIndex int,
	spec sessionRunnerReviewExecutionSpec,
	inputText, messageOrigin string,
) (transcriptstore.RunnerClaim, error) {
	if s.transcriptStore == nil {
		return transcriptstore.RunnerClaim{}, errors.New("transcript authority is required for reviewer frames")
	}
	if s.transcriptContractErr != nil {
		return transcriptstore.RunnerClaim{}, s.transcriptContractErr
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil {
		return transcriptstore.RunnerClaim{}, err
	}
	if !found || strings.TrimSpace(frameContext.UserID) == "" || frameContext.Frame.ProjectID != parent.ProjectID ||
		frameContext.Frame.RootFrameID != parent.RootFrameID {
		return transcriptstore.RunnerClaim{}, transcriptstore.ErrOwnerMismatch
	}
	payload, err := json.Marshal(map[string]any{
		"text": inputText, "message_origin": messageOrigin,
		"target_frame_id": parent.ID, "parent_runner_attempt": attempt, "review_index": reviewIndex,
		"review_kind": spec.ReviewKind, "reviewer_profile": spec.ProfileName,
	})
	if err != nil {
		return transcriptstore.RunnerClaim{}, err
	}
	jobName := strings.ToLower(strings.TrimSpace(spec.ProfileName))
	runnerID := jobName + ":" + frame.ID
	started, err := s.transcriptStore.StartInternalFrameRunner(ctx, transcriptstore.StartInternalFrameRunnerInput{
		Stream: transcriptstore.CreateStreamInput{
			UID: "frame:" + frame.ID, OwnerID: frameContext.UserID, ExternalID: frame.ID, SessionID: frame.ID,
			Kind: transcriptstore.StreamKindFrameRef, ProjectID: frame.ProjectID, RootFrameID: frame.RootFrameID,
			FrameID: frame.ID, Epoch: 1,
		},
		UserEvent: transcriptstore.AppendUserEventInput{
			StreamUID: "frame:" + frame.ID, OwnerID: frameContext.UserID,
			ClientMessageID: jobName + "-input:" + frame.ID, PayloadJSON: payload,
		},
		RunnerID: runnerID, TTL: sessionReviewerFrameLeaseTTL,
	})
	if err != nil {
		return transcriptstore.RunnerClaim{}, err
	}
	if !started.Claimed || started.Claim.RunnerID != runnerID {
		return transcriptstore.RunnerClaim{}, transcriptstore.ErrEventConflict
	}
	return started.Claim, nil
}

func (s *Server) finishSessionReviewerFrame(frameID, status, description string, reviewIndex int) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return errors.New("workspace and transcript stores are required for completion reviewer frames")
	}
	frameID = strings.TrimSpace(frameID)
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "completed", "failed", "cancelled":
	default:
		return fmt.Errorf("invalid completion reviewer terminal status %q", status)
	}
	description = truncateReviewerText(description, 16<<10)
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(frameContext.UserID) == "" {
		return transcriptstore.ErrOwnerMismatch
	}
	err = s.transcriptStore.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		_, _, _, finishErr := tx.FinishLatestFrameRunner(
			context.Background(), frameContext.UserID, frameID, status, description, []string{"ws"},
		)
		return finishErr
	})
	if err != nil {
		return err
	}
	if err := s.workspaceStore.UpdateFrameRuntimePresentation(frameID, workspace.FrameRuntimePresentationInput{StatusDescription: &description}); err != nil {
		return err
	}
	eventPrefix, eventType := "runner-reviewer:", "verification_"
	if strings.EqualFold(frameContext.Frame.AgentName, "BOOKMARKER") {
		eventPrefix, eventType = "runner-bookmarker:", "bookmarking_"
	}
	event, err := s.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
		ID: eventPrefix + frameID + ":" + status, FrameID: frameID, Type: eventType + status,
		Payload: map[string]any{"status": status, "reviewIndex": reviewIndex},
	})
	if err != nil {
		return err
	}
	return s.publishWorkspaceEvent(event)
}

func sessionReviewerFailureStatus(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrGenerationStopped) {
		return "cancelled"
	}
	if ctx != nil {
		cause := context.Cause(ctx)
		if errors.Is(cause, context.Canceled) || errors.Is(cause, ErrGenerationStopped) {
			return "cancelled"
		}
	}
	return "failed"
}
