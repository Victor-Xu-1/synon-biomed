package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type transcriptRunnerAuthority struct {
	Stream transcriptstore.Stream
	// Claim is immutable across heartbeat/checkpoint goroutines. The repository
	// validates and renews the live expiry in durable runner state.
	Claim transcriptstore.RunnerClaim
}

func (s *Server) claimTranscriptFrameRunner(
	ctx context.Context,
	sessionID, runnerID string,
	ttl time.Duration,
	resumeSource transcriptstore.ResumeSource,
	resumeCheckpoint int64,
) (*transcriptRunnerAuthority, bool, bool, error) {
	stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, sessionID)
	if err != nil || !authoritative {
		return nil, false, authoritative, err
	}
	if resumeSource == transcriptstore.ResumeSourceFresh && resumeCheckpoint == 0 {
		// A paused attempt with a resumable checkpoint must be continued in
		// place after a user_input_response (for example a kernel local
		// execution approval), matching the frame-resume dispatch contract.
		// A fresh claim is valid only when the newest input is a new
		// user_message that supersedes the checkpoint, or when no resumable
		// checkpoint exists at all.
		checkpoint, found, err := s.transcriptStore.LatestResumableCheckpoint(
			ctx, stream.UID, stream.OwnerID,
		)
		if err != nil {
			return nil, false, true, err
		}
		if found {
			state, stateErr := s.transcriptStore.GetRunnerRuntimeState(
				ctx, stream.UID, stream.OwnerID, checkpoint.Attempt,
			)
			if stateErr != nil {
				return nil, false, true, stateErr
			}
			inputType, inputErr := s.transcriptStore.LatestRunnerInputEventType(
				ctx, stream.UID, stream.OwnerID,
			)
			if inputErr != nil {
				return nil, false, true, inputErr
			}
			if stream.InputRevision >= state.ClaimedInputRevision && inputType != "user_message" {
				resumeSource = transcriptstore.ResumeSourceCheckpoint
				resumeCheckpoint = checkpoint.Sequence
			}
		}
	}
	result, err := s.transcriptStore.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: runnerID,
		TTL: ttl, ResumeSource: resumeSource, ResumeCheckpoint: resumeCheckpoint,
	})
	if err != nil {
		return nil, false, true, err
	}
	if !result.Claimed {
		return nil, false, true, nil
	}
	return &transcriptRunnerAuthority{Stream: stream, Claim: result.Claim}, true, true, nil
}

func (s *Server) resolveTranscriptFrameStream(ctx context.Context, sessionID string) (transcriptstore.Stream, bool, error) {
	if s == nil || s.transcriptStore == nil {
		return transcriptstore.Stream{}, false, nil
	}
	if s.workspaceStore != nil {
		frameContext, found, err := s.workspaceStore.GetFrameRealtimeContextWithContext(ctx, sessionID)
		if err != nil {
			return transcriptstore.Stream{}, false, err
		}
		if found {
			stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, sessionID)
			if err != nil {
				return transcriptstore.Stream{}, true, err
			}
			if !found {
				return transcriptstore.Stream{}, true, transcriptstore.ErrSchemaUnavailable
			}
			return stream, true, nil
		}
	}
	ownerID, routed, err := s.transcriptIMRouteOwner(sessionID)
	if err != nil || !routed {
		return transcriptstore.Stream{}, routed, err
	}
	stream, found, err := s.transcriptStore.GetStreamBySession(ctx, ownerID, sessionID)
	if err != nil {
		return transcriptstore.Stream{}, true, err
	}
	if !found || stream.Kind != transcriptstore.StreamKindStandalone || stream.SessionID != sessionID {
		return transcriptstore.Stream{}, true, transcriptstore.ErrSchemaUnavailable
	}
	return stream, true, nil
}

func (s *Server) loadTranscriptFrameSessionProjection(stream transcriptstore.Stream) (sessionstore.Session, error) {
	if s == nil {
		return sessionstore.Session{}, errors.New("server is not configured")
	}
	if stream.Kind == transcriptstore.StreamKindStandalone {
		if s.sessionStore == nil || strings.TrimSpace(stream.SessionID) == "" {
			return sessionstore.Session{}, transcriptstore.ErrSchemaUnavailable
		}
		session, found, err := s.sessionStore.Get(stream.SessionID)
		if err != nil {
			return sessionstore.Session{}, err
		}
		if !found {
			return sessionstore.Session{}, transcriptstore.ErrSchemaUnavailable
		}
		session.Orchestration = nil
		return session, nil
	}
	if s.workspaceStore == nil {
		return sessionstore.Session{}, errors.New("workspace store is not configured")
	}
	if stream.Kind != transcriptstore.StreamKindFrameRef || strings.TrimSpace(stream.SessionID) == "" ||
		strings.TrimSpace(stream.FrameID) == "" || stream.SessionID != stream.FrameID {
		return sessionstore.Session{}, transcriptstore.ErrEventConflict
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(stream.FrameID)
	if err != nil {
		return sessionstore.Session{}, err
	}
	if !found || frameContext.UserID != stream.OwnerID || frameContext.Frame.ProjectID != stream.ProjectID ||
		frameContext.Frame.RootFrameID != stream.RootFrameID {
		return sessionstore.Session{}, transcriptstore.ErrOwnerMismatch
	}
	project, found, err := s.workspaceStore.GetProject(stream.ProjectID)
	if err != nil {
		return sessionstore.Session{}, err
	}
	if !found {
		return sessionstore.Session{}, fmt.Errorf("transcript project %q not found", stream.ProjectID)
	}
	session := sessionstore.Session{ID: stream.SessionID, CreatedAt: frameContext.Frame.CreatedAt}
	if s.sessionStore != nil {
		stored, storedFound, err := s.sessionStore.Get(stream.SessionID)
		if err != nil {
			return sessionstore.Session{}, fmt.Errorf("read session compatibility projection: %w", err)
		}
		if storedFound {
			session = stored
		}
	}
	// JSON Session orchestration is a compatibility projection, never runtime
	// authority for a Transcript-backed Frame.
	session.Orchestration = nil
	if config, found, err := s.transcriptStore.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID); err != nil {
		return sessionstore.Session{}, err
	} else if found {
		session = projectRuntimeSessionConfig(session, config)
	}
	// Workspace/Transcript identity is authoritative; JSON Session fields are only
	// a temporary configuration projection until the transcript cutover completes.
	session.ID = stream.SessionID
	session.Title = strings.TrimSpace(frameContext.Frame.Name)
	if session.Title == "" {
		session.Title = stream.FrameID
	}
	session.WorkDir = strings.TrimSpace(project.Path)
	if session.WorkDir == "" {
		session.WorkDir = strings.TrimSpace(s.fileRoot)
	}
	session.Project = &sessionstore.Project{
		ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: frameContext.Frame.UpdatedAt,
	}
	return session, nil
}

func (s *Server) checkpointTranscriptRunner(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	phase transcriptstore.RunnerPhase,
	identity string,
	payload map[string]any,
	resumable bool,
) error {
	_, err := s.checkpointTranscriptRunnerEvent(ctx, authority, phase, identity, payload, resumable)
	return err
}

func (s *Server) pauseTranscriptRunnerForInput(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	status, detail string,
) (transcriptstore.Event, error) {
	if authority == nil {
		return transcriptstore.Event{}, errors.New("transcript runner authority is required")
	}
	raw, err := json.Marshal(map[string]any{"status": strings.TrimSpace(status), "detail": strings.TrimSpace(detail)})
	if err != nil {
		return transcriptstore.Event{}, err
	}
	_, event, _, err := s.transcriptStore.PauseRunnerForInput(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: authority.Claim, ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, "runner-paused"),
		Phase: transcriptstore.RunnerPhaseWaitingUser, Resumable: true, PayloadJSON: raw,
		Destinations: transcriptRunnerDestinations(authority),
	})
	if err == nil && len(transcriptRunnerDestinations(authority)) > 0 {
		s.signalTranscriptWebDelivery()
	}
	return event, err
}

func (s *Server) pauseTranscriptRunnerForApproval(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	pause *agentruntime.PauseError,
) (transcriptstore.Event, error) {
	if authority == nil || pause == nil {
		return transcriptstore.Event{}, errors.New("transcript runner approval authority is required")
	}
	if err := s.copyLatestRunnerTaskMemorySnapshotForPause(ctx, authority); err != nil {
		return transcriptstore.Event{}, err
	}
	payload := map[string]any{
		"status": "awaiting_approval",
		"detail": strings.TrimSpace(pause.Message),
	}
	for _, key := range []string{
		"approval_kind", "operation_id", "request_id", "state_version", "tool_call_id",
		"approval_id", "tool_name", "description", "target", "rememberable",
		"plan_artifact_id", "plan_version_id", "plan_sha256",
	} {
		if value, found := pause.Data[key]; found {
			payload[key] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return transcriptstore.Event{}, err
	}
	approvalKind := strings.TrimSpace(stringValue(payload["approval_kind"]))
	checkpointIdentity := ""
	var commitHook transcriptstore.RunnerCheckpointCommitHook
	var approvalEvents []workspace.FrameEvent
	if approvalKind == "plan" {
		if s.workspaceStore == nil {
			return transcriptstore.Event{}, errors.New("generated plan workspace authority is required")
		}
		frameID := strings.TrimSpace(stringValue(pause.Data["frame_id"]))
		artifactID := strings.TrimSpace(stringValue(pause.Data["plan_artifact_id"]))
		versionID := strings.TrimSpace(stringValue(pause.Data["plan_version_id"]))
		toolCallID := strings.TrimSpace(stringValue(pause.Data["tool_call_id"]))
		requestID := strings.TrimSpace(stringValue(pause.Data["request_id"]))
		planJSON := mapValue(pause.Data["plan_json"])
		if frameID == "" || frameID != authority.Stream.FrameID || frameID != authority.Stream.SessionID ||
			artifactID == "" || versionID == "" || toolCallID == "" || requestID == "" || len(planJSON) == 0 {
			return transcriptstore.Event{}, errors.New("generated plan approval identity does not match the runner authority")
		}
		bindInput := workspace.BindGeneratedPlanApprovalInput{
			FrameID: frameID, ToolCallID: toolCallID, RequestID: requestID,
			ArtifactID: artifactID, VersionID: versionID,
			Filename:      strings.TrimSpace(stringValue(pause.Data["plan_filename"])),
			PlanSHA256:    strings.TrimSpace(stringValue(pause.Data["plan_sha256"])),
			PlanSizeBytes: numberValue(pause.Data["plan_size_bytes"]), PlanJSON: planJSON,
			TaskIntentID:       strings.TrimSpace(stringValue(pause.Data["task_intent_id"])),
			TaskIntentRevision: numberValue(pause.Data["task_intent_revision"]),
			TaskIntentSHA256:   strings.TrimSpace(stringValue(pause.Data["task_intent_sha256"])),
		}
		commitHook = func(
			ctx context.Context,
			tx *transcriptstore.ImmediateTransaction,
			_ transcriptstore.Event,
			_ bool,
		) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			event, err := s.workspaceStore.BindGeneratedPlanApprovalTransaction(ctx, tx, bindInput)
			if err == nil {
				approvalEvents = append(approvalEvents, event)
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{}, err
		}
		checkpointIdentity = "plan-" + artifactID + "-" + versionID
	} else if approvalKind == agentToolApprovalKind {
		frameID := strings.TrimSpace(stringValue(pause.Data["frame_id"]))
		approvalID := strings.TrimSpace(stringValue(pause.Data["approval_id"]))
		toolCallID := strings.TrimSpace(stringValue(pause.Data["tool_call_id"]))
		toolName := strings.TrimSpace(stringValue(pause.Data["tool_name"]))
		if s.workspaceStore == nil || frameID == "" || frameID != authority.Stream.FrameID ||
			approvalID == "" || toolCallID == "" || toolName == "" {
			return transcriptstore.Event{}, errors.New("agent tool approval identity does not match the runner authority")
		}
		bindInput := workspace.BindAgentToolApprovalInput{
			FrameID: frameID, ApprovalID: approvalID, ToolCallID: toolCallID, ToolName: toolName,
			Description:  strings.TrimSpace(stringValue(pause.Data["description"])),
			Target:       strings.TrimSpace(stringValue(pause.Data["target"])),
			Rememberable: boolValue(pause.Data["rememberable"], false),
		}
		commitHook = func(
			ctx context.Context,
			tx *transcriptstore.ImmediateTransaction,
			_ transcriptstore.Event,
			_ bool,
		) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			bound, err := s.workspaceStore.BindAgentToolApprovalTransaction(ctx, tx, bindInput)
			if err == nil && !bound.AlreadyPending {
				approvalEvents = append(approvalEvents, bound.Events...)
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{}, err
		}
		checkpointIdentity = "agent-tool-approval-" + approvalID
	} else {
		operationID := strings.TrimSpace(stringValue(payload["operation_id"]))
		if operationID == "" {
			return transcriptstore.Event{}, errors.New("kernel operation approval identity is required")
		}
		checkpointIdentity = "runner-approval-" + operationID
	}
	_, event, _, err := s.transcriptStore.PauseRunnerForApproval(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim:           authority.Claim,
		ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, checkpointIdentity),
		Phase:           transcriptstore.RunnerPhaseWaitingApproval, Resumable: true, PayloadJSON: raw,
		Destinations: transcriptRunnerDestinations(authority),
		CommitHook:   commitHook,
	})
	if err == nil && len(transcriptRunnerDestinations(authority)) > 0 {
		s.signalTranscriptWebDelivery()
	}
	if err == nil {
		for _, approvalEvent := range approvalEvents {
			if publishErr := s.publishWorkspaceEvent(approvalEvent); publishErr != nil {
				log.Printf("approval projection deferred frame=%s event=%s: %v", authority.Stream.FrameID, approvalEvent.ID, publishErr)
			}
		}
	}
	return event, err
}

// copyLatestRunnerTaskMemorySnapshotForPause carries the newest durable task
// memory snapshot onto the current runner attempt before it parks at a
// WaitingApproval checkpoint. A resumed attempt can pause before its first
// model call (the durable kernel operation is already pending), so no snapshot
// was sealed on this attempt; the resume authority needs one on the same
// attempt as the pause checkpoint.
func (s *Server) copyLatestRunnerTaskMemorySnapshotForPause(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) error {
	if s == nil || s.transcriptStore == nil || authority == nil {
		return nil
	}
	snapshot, found, err := s.transcriptStore.LatestRunnerTaskMemorySnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID,
	)
	if err != nil || !found {
		return err
	}
	if existing, found, err := s.transcriptStore.GetRunnerTaskMemorySnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt,
	); err != nil {
		return err
	} else if found && existing.SHA256 == snapshot.SHA256 {
		return nil
	}
	payload, err := runnerTaskMemorySnapshotPayload(snapshot)
	if err != nil || len(payload) == 0 {
		return errors.New("runner task memory snapshot payload is unavailable")
	}
	_, err = s.checkpointTranscriptRunnerEventWithDestinations(
		ctx, authority, transcriptstore.RunnerPhasePlanning,
		runnerTaskMemorySnapshotClientMessageID(snapshot), payload, false, nil,
	)
	return err
}

func (s *Server) checkpointTranscriptRunnerEvent(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	phase transcriptstore.RunnerPhase,
	identity string,
	payload map[string]any,
	resumable bool,
) (transcriptstore.Event, error) {
	return s.checkpointTranscriptRunnerEventWithDestinations(
		ctx, authority, phase, identity, payload, resumable, transcriptRunnerDestinations(authority),
	)
}

func (s *Server) checkpointTranscriptRunnerEventWithDestinations(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	phase transcriptstore.RunnerPhase,
	identity string,
	payload map[string]any,
	resumable bool,
	destinations []string,
) (transcriptstore.Event, error) {
	return s.checkpointTranscriptRunnerEventWithDestinationsAndHook(
		ctx, authority, phase, identity, payload, resumable, destinations, nil,
	)
}

func (s *Server) checkpointTranscriptRunnerEventWithDestinationsAndHook(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	phase transcriptstore.RunnerPhase,
	identity string,
	payload map[string]any,
	resumable bool,
	destinations []string,
	commitHook transcriptstore.RunnerCheckpointCommitHook,
) (transcriptstore.Event, error) {
	if authority == nil {
		return transcriptstore.Event{}, nil
	}
	if s.generatedPlanResearchSourceCheckpoint(authority.Stream.FrameID, payload) && s.workspaceStore != nil {
		priorHook := commitHook
		commitHook = func(
			ctx context.Context,
			tx *transcriptstore.ImmediateTransaction,
			event transcriptstore.Event,
			created bool,
		) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			receipt := transcriptstore.RunnerCheckpointCommitReceipt{}
			if priorHook != nil {
				var err error
				receipt, err = priorHook(ctx, tx, event, created)
				if err != nil {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, err
				}
			}
			if err := s.workspaceStore.AdvanceClaimedFrameRuntimeEventCursorTx(
				ctx, tx, authority.Claim, generatedPlanResearchLatestSourceEventIDKey, event.EventID,
			); err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			return receipt, nil
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return transcriptstore.Event{}, err
	}
	_, event, _, err := s.transcriptStore.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: authority.Claim, ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, identity),
		Phase: phase, Resumable: resumable, PayloadJSON: raw, Destinations: destinations, CommitHook: commitHook,
	})
	if err == nil && len(destinations) > 0 {
		s.signalTranscriptWebDelivery()
	}
	return event, err
}

func (s *Server) generatedPlanResearchSourceCheckpoint(frameID string, payload map[string]any) bool {
	if s == nil {
		return false
	}
	if _, _, found := s.generatedPlanActiveResearchStep(frameID); !found {
		return false
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	var checkpoint sessionRunnerDurableToolCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil || !researchCheckpointExecutedTerminal(checkpoint) {
		return false
	}
	return s.sessionRunnerResearchAttemptTool(checkpoint)
}

func (s *Server) appendTranscriptRunnerEvent(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	eventType, identity string,
	payload map[string]any,
) (transcriptstore.Event, error) {
	if authority == nil {
		return transcriptstore.Event{}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return transcriptstore.Event{}, err
	}
	event, _, err := s.transcriptStore.AppendRunnerEvent(ctx, transcriptstore.AppendEventInput{
		Claim: authority.Claim, ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, identity),
		Type: eventType, Source: transcriptstore.EventSourcePayload, PayloadJSON: raw,
		Destinations: transcriptRunnerDestinations(authority),
	})
	if err == nil && len(transcriptRunnerDestinations(authority)) > 0 {
		s.signalTranscriptWebDelivery()
	}
	return event, err
}

func (s *Server) appendTranscriptAssistantEvent(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	identity string,
	payload map[string]any,
) error {
	if authority == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	input := transcriptstore.AppendEventInput{
		Claim: authority.Claim, ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, identity),
		Type: "assistant_message", Source: transcriptstore.EventSourcePayload, PayloadJSON: raw,
		Destinations: transcriptRunnerDestinations(authority),
	}
	err = s.appendTranscriptAssistantEventWithTurnArtifacts(ctx, authority, input)
	if err == nil && len(transcriptRunnerDestinations(authority)) > 0 {
		s.signalTranscriptWebDelivery()
	}
	return err
}

func (s *Server) finishTranscriptRunner(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	status, detail string,
	segmentOrdinals ...int64,
) (transcriptstore.Event, error) {
	return s.finishTranscriptRunnerWithReason(ctx, authority, status, detail, "", segmentOrdinals...)
}

func (s *Server) finishTranscriptRunnerWithReason(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	status, detail, reasonCode string,
	segmentOrdinals ...int64,
) (transcriptstore.Event, error) {
	if authority == nil {
		return transcriptstore.Event{}, nil
	}
	status = strings.TrimSpace(status)
	segmentOrdinal := int64(1)
	if len(segmentOrdinals) > 0 && segmentOrdinals[0] > 0 {
		segmentOrdinal = segmentOrdinals[0]
	}
	payload := map[string]any{
		"status": status, "detail": strings.TrimSpace(detail),
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(segmentOrdinal, ""),
	}
	if reasonCode = strings.TrimSpace(reasonCode); reasonCode != "" {
		payload["reason_code"] = reasonCode
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return transcriptstore.Event{}, err
	}
	event, _, _, err := s.transcriptStore.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
		Claim: authority.Claim, ClientMessageID: transcriptRunnerClientMessageID(authority.Claim, "runner-finished"),
		Status: status, PayloadJSON: raw, Destinations: transcriptRunnerDestinations(authority),
	})
	if err == nil && s.workspaceStore != nil {
		s.workspaceStore.NotifyKernelRunnerStateChanged()
	}
	if err == nil && len(transcriptRunnerDestinations(authority)) > 0 {
		s.signalTranscriptWebDelivery()
	}
	if errors.Is(err, transcriptstore.ErrClaimStale) {
		state, stateErr := s.transcriptStore.GetRunnerRuntimeState(
			ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt,
		)
		if stateErr == nil && state.Status == status {
			return transcriptstore.Event{}, nil
		}
	}
	return event, err
}

func (s *Server) releaseSessionProjection(claim sessionstore.RunnerMutationClaim) error {
	_, released, err := s.sessionStore.ReleaseRunner(claim)
	if err != nil {
		return err
	}
	if !released {
		return errors.New("session projection runner lease was lost")
	}
	return nil
}

func transcriptRunnerClientMessageID(claim transcriptstore.RunnerClaim, identity string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(claim.RunnerID), strings.TrimSpace(claim.StreamUID),
		fmt.Sprintf("%d", claim.Attempt), strings.TrimSpace(claim.ClaimToken), strings.TrimSpace(identity),
	}, "\x00")))
	return "transcript-runner:" + hex.EncodeToString(sum[:])
}

func transcriptRunnerDestinations(authority *transcriptRunnerAuthority) []string {
	if authority == nil {
		return nil
	}
	if authority.Stream.Kind == transcriptstore.StreamKindFrameRef {
		return []string{transcriptWebDestination}
	}
	if authority.Stream.Kind == transcriptstore.StreamKindStandalone && strings.HasPrefix(authority.Stream.SessionID, "im:") {
		return []string{authority.Stream.SessionID}
	}
	return nil
}

func transcriptCheckpointPayload(input map[string]any) map[string]any {
	payload := make(map[string]any, len(input))
	for key, value := range input {
		if key == "claimToken" || value == nil {
			continue
		}
		payload[key] = value
	}
	return payload
}
