package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWebConversationActiveCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	frames, err := s.workspaceStore.ListVisibleCompatibilityFrames(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), "", true, 1000)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	count := 0
	for _, frame := range frames {
		if webConversationProcessing(frame.Status) {
			count++
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (s *Server) handleWebConversationClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var body struct {
		Conversation map[string]any `json:"conversation"`
		IntentID     string         `json:"intent_id"`
	}
	if err := decodeWebConversationJSON(w, r, &body); err != nil || body.Conversation == nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid clone request"})
		return
	}
	sourceID := strings.TrimSpace(webString(body.Conversation["id"]))
	source, project, ok := s.webConversationAccess(w, r, sourceID)
	if !ok {
		return
	}
	if s.transcriptStore == nil {
		writeWebConversationError(w, transcriptWebStorageError(errors.New("transcript storage is required for conversation clone")))
		return
	}
	name := strings.TrimSpace(source.Name) + " copy"
	if utf8.RuneCountInString(name) > maxWebConversationNameRunes {
		writeWebConversationError(w, &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "conversation name exceeds 255 characters",
		})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	targetID := uuid.NewString()
	if intentID := strings.TrimSpace(body.IntentID); intentID != "" {
		parsed, err := uuid.Parse(intentID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid clone request"})
			return
		}
		targetID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(
			"synon-conversation-clone-intent-v1\x00"+userID+"\x00"+parsed.String(),
		)).String()
	}
	cloned, err := s.workspaceStore.CloneConversationWithTranscript(r.Context(), workspace.CloneConversationInput{
		OwnerUserID: userID, SourceFrameID: source.ID,
		ExpectedSourceIncarnationID: source.IncarnationID,
		TargetFrameID:               targetID, TargetName: name,
	})
	if err != nil {
		if errors.Is(err, transcriptstore.ErrEventConflict) {
			writeWebConversationError(w, &webConversationRequestError{
				Status: http.StatusConflict, Detail: "conversation clone conflicts with current state", Cause: err,
			})
			return
		}
		writeWebConversationError(w, transcriptWebStorageError(err))
		return
	}
	writeWorkspaceJSON(w, http.StatusCreated, webConversation(cloned.Frame, project.Name))
}

func (s *Server) publishWebConversationListChange(userID, conversationID, action string) error {
	_, err := s.publishUserEvent(userID, "conversation.listChanged", map[string]any{
		"conversation_id": conversationID, "action": action, "source": "synonbiomed",
	})
	return err
}

func webConversationRuntime(frame workspace.CompatibilityFrame) map[string]any {
	return webConversationRuntimeWithActions(frame, 0, false)
}

func (s *Server) webConversationRuntimeSnapshot(frame workspace.CompatibilityFrame) (map[string]any, error) {
	// Completed and failed frames cannot expose pending input, live runner, pause,
	// or internal-recovery state. Keep the sidebar projection on the frame row
	// already loaded by the bounded list query instead of issuing several
	// workspace and transcript reads for every historical conversation.
	statusValue := strings.ToLower(strings.TrimSpace(frame.Status))
	if statusValue == "completed" || statusValue == "failed" {
		return webConversationRuntime(frame), nil
	}

	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		return nil, webConversationRuntimeSnapshotError(frame.ID, webConversationRuntimeMetadataCode, err)
	}
	pending := 0
	planApproval := false
	if found {
		pending = len(compatibilityServerPendingInputs(metadata.ContextData))
		planApproval = strings.EqualFold(strings.TrimSpace(frame.Status), "awaiting_plan_approval") &&
			strings.TrimSpace(stringValue(metadata.ContextData["_plan_artifact_id"])) != ""
	}
	// Pending-input metadata is a compatibility cache and may survive an
	// interruption while the durable runner has already been reclaimed and
	// resumed.  The transcript-owned runtime clock is authoritative whenever it
	// is available: an active logical task must remain visibly running rather
	// than being downgraded to a stale waiting state by that old cache.
	modelConfigurationWait := false
	recoveryConditionWait := false
	if runtimeProjection, _, projectionErr := s.compatibilityFrameRuntimeProjection(frame.ID); projectionErr == nil {
		if boolValue(runtimeProjection["runtime_active"], false) || boolValue(runtimeProjection["runtime_task_active"], false) {
			pending = 0
			planApproval = false
		} else if boolValue(runtimeProjection["runtime_paused"], false) &&
			strings.EqualFold(strings.TrimSpace(stringValue(runtimeProjection["runtime_interruption_reason"])), sessionRunnerModelProviderUnavailableReasonCode) {
			modelConfigurationWait = true
		} else if boolValue(runtimeProjection["runtime_paused"], false) {
			recoveryConditionWait = true
		}
	}
	runtime := webConversationRuntimeWithActions(frame, pending, planApproval)
	if modelConfigurationWait || recoveryConditionWait {
		// Missing model configuration is a recoverable user-owned input boundary,
		// not a failed task and not background work. Keep the same logical task
		// sendable so selecting a provider or sending Continue resumes it.
		runtime["state"] = "waiting_input"
		if recoveryConditionWait {
			runtime["state"] = "paused"
		}
		runtime["can_send_message"] = true
		runtime["has_task"] = true
		runtime["task_status"] = "pending"
		runtime["is_processing"] = false
		runtime["pending_confirmations"] = 0
		runtime["turn_id"] = frame.ID
		return runtime, nil
	}
	userPaused := false
	if statusValue == "cancelled" || statusValue == "canceled" {
		userPaused, err = s.compatibilityFrameUserPaused(frame.ID)
		if err != nil {
			return nil, webConversationRuntimeSnapshotError(frame.ID, webConversationRuntimeStateCode, err)
		}
		if userPaused {
			runtime["state"] = "paused"
			runtime["can_send_message"] = true
			runtime["has_task"] = true
			runtime["task_status"] = "pending"
			runtime["is_processing"] = false
			runtime["pending_confirmations"] = 0
			runtime["turn_id"] = frame.ID
		}
	}
	internalStall, err := s.compatibilityFrameHasInternalStall(frame.ID)
	if err != nil {
		return nil, webConversationRuntimeSnapshotError(frame.ID, webConversationRuntimeStateCode, err)
	}
	if internalStall && pending == 0 && !planApproval && !userPaused && !isFrameResumeTerminalStatus(statusValue) {
		// A blocked or expired internal recovery is a failed execution boundary,
		// never an implicit AskUser request. Project it as terminal attention so a
		// dead runner cannot keep a green spinner or require a magic "continue".
		runtime["state"] = "idle"
		runtime["can_send_message"] = true
		runtime["has_task"] = false
		runtime["task_status"] = "error"
		runtime["is_processing"] = false
		runtime["pending_confirmations"] = 0
		runtime["turn_id"] = nil
	}
	return runtime, nil
}

func (s *Server) compatibilityFrameUserPaused(frameID string) (bool, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		return false, err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frameID)
	if err != nil || !found {
		return false, err
	}
	summary, found, err := s.transcriptStore.GetLatestRunnerTimingSummary(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		// An interrupted/resumable attempt has no terminal receipt yet, so
		// the aggregate timing query quite correctly reports a conflict. It
		// is not a storage failure and must not make the runtime endpoint
		// unusable; the caller can continue with the durable terminal check.
		if !errors.Is(err, transcriptstore.ErrEventConflict) {
			return false, err
		}
		return false, nil
	}
	if !found || summary.FinishedEventID <= 0 {
		return false, nil
	}
	terminal, err := s.transcriptStore.GetTerminalProjection(ctx, stream.OwnerID, stream.UID, summary.FinishedEventID)
	if errors.Is(err, transcriptstore.ErrTerminalProjectionUnavailable) || errors.Is(err, transcriptstore.ErrEventConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return terminal.TerminalStatus == "cancelled" &&
		strings.EqualFold(strings.TrimSpace(terminal.ReasonCode), "user_cancelled"), nil
}

func webConversationRuntimeSnapshotError(frameID, code string, cause error) error {
	log.Printf(
		"web_conversation_runtime_snapshot_failed code=%s frame_id=%s cause_type=%T",
		code,
		strings.TrimSpace(frameID),
		cause,
	)
	return &webConversationRequestError{
		Status: http.StatusInternalServerError,
		Code:   code,
		Detail: "unable to load conversation runtime",
		Cause:  cause,
	}
}

func (s *Server) compatibilityFrameHasInternalStall(frameID string) (bool, error) {
	if s == nil || s.workspaceStore == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dispatch, found, err := s.workspaceStore.GetCompatibilityFrameResumeDispatchByFrame(frameID)
	if err != nil {
		return false, err
	}
	activeKernel, err := s.workspaceStore.HasActiveDetachedKernelExecutionForFrame(ctx, frameID)
	if err != nil {
		return false, err
	}
	if activeKernel {
		// Long local work is owned by its durable executor receipt, not by a
		// short runner lease or the resume dispatch polling cadence.
		return false, nil
	}
	// A resumable execution segment may release its in-memory tool observer and
	// publish an interruption checkpoint before the durable kernel recovery
	// attaches. The runner lease remains the logical-task authority throughout
	// that short handoff. Never convert a fresh running attempt into a terminal
	// stall merely because there is a momentary gap between physical executors.
	if s.transcriptStore != nil {
		frameContext, contextFound, contextErr := s.workspaceStore.GetFrameRealtimeContext(frameID)
		if contextErr != nil {
			return false, contextErr
		}
		if contextFound {
			stream, streamFound, streamErr := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frameID)
			if streamErr != nil {
				return false, streamErr
			}
			if streamFound {
				timing, timingFound, timingErr := s.transcriptStore.GetLatestRunnerTimingSummary(ctx, stream.UID, stream.OwnerID)
				if timingErr != nil {
					// Resumable interruptions intentionally lack a finished event.
					// Treat that transiently incomplete aggregate as unknown and let
					// the dispatch/lease checks below decide whether work is stalled.
					if !errors.Is(timingErr, transcriptstore.ErrEventConflict) {
						return false, timingErr
					}
				} else if timingFound && timing.Active {
					return false, nil
				}
			}
		}
	}
	if found {
		now := time.Now().UTC()
		switch dispatch.Status {
		case "blocked":
			// Legacy blocked history loses to a live runner/kernel above. Without
			// either, it is an exhausted internal recovery boundary.
			return true, nil
		case "claimed":
			// A claimed dispatch owns the short handoff between runner and kernel
			// execution. Its renewable lease is durable liveness authority even
			// when neither in-memory projection exists for a few milliseconds.
			if dispatch.LeaseExpiresAt.After(now) {
				return false, nil
			}
			return true, nil
		case "registered":
			// A future not-before is an intentional recovery backoff. A newly
			// registered immediate dispatch also gets one claim-lease grace window;
			// after that, an unclaimed row is a real control-plane stall.
			if dispatch.NotBefore.After(now) || dispatch.ResumeEvent.CreatedAt.Add(frameResumeDispatchClaimLeaseGrace).After(now) {
				return false, nil
			}
			return true, nil
		}
	}
	// A transcript runner may have already emitted a resumable correction
	// checkpoint while its compatibility dispatch is still completing or has
	// not yet been materialized. Once its lease expires without replacement,
	// the task is stalled and must not remain projected as active work.
	direct, directErr := s.shouldDirectlyDeliverExpiredTranscriptMessage(frameID)
	if directErr != nil {
		return false, directErr
	}
	if direct {
		return true, nil
	}
	return false, nil
}

func compatibilityFrameResumeDispatchReasonCode(dispatch workspace.CompatibilityFrameResumeDispatch) string {
	code := strings.TrimSpace(dispatch.Error)
	if payload, ok := dispatch.ResumeEvent.Payload["dispatch"].(map[string]any); ok {
		if code == "" {
			code = strings.TrimSpace(webString(payload["errorCode"]))
		}
		if code == "" {
			code = strings.TrimSpace(webString(payload["reasonCode"]))
		}
	}
	return code
}

func webConversationRuntimeWithActions(
	frame workspace.CompatibilityFrame,
	pending int,
	planApproval bool,
) map[string]any {
	statusValue := strings.ToLower(strings.TrimSpace(frame.Status))
	if planApproval {
		return map[string]any{
			"state": "waiting_approval", "can_send_message": true, "has_task": true, "task_status": "pending",
			"is_processing": false, "pending_confirmations": 1, "turn_id": frame.ID,
		}
	}
	processing := webConversationProcessing(statusValue)
	starting := statusValue == "queued" || statusValue == "pending" || statusValue == "created"
	waiting := pending > 0
	legacyWaitingHint := statusValue == "awaiting_user_response" || statusValue == "needs_input" ||
		statusValue == "awaiting_input" || statusValue == "awaiting_plan_approval"
	processing = processing || legacyWaitingHint
	hasTask := processing || starting || waiting
	state := "idle"
	status := "finished"
	switch {
	case statusValue == "failed":
		status = "error"
	case statusValue == "cancelled" || statusValue == "canceled":
		status = "cancelled"
	case waiting:
		state = "waiting_confirmation"
		status = "pending"
	case processing:
		state = "running"
		status = "running"
	case starting:
		state = "starting"
		status = "pending"
	}
	var turnID any
	if hasTask {
		turnID = frame.ID
	}
	return map[string]any{
		"state": state, "can_send_message": !hasTask, "has_task": hasTask, "task_status": status,
		"is_processing": waiting || processing || starting, "pending_confirmations": pending, "turn_id": turnID,
	}
}

func webConversationProcessing(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "processing" || status == "running" || status == "executing" ||
		status == "in_progress" || status == "in-progress"
}

type webConversationRequestError struct {
	Status int
	Code   string
	Detail string
	Cause  error
}

func (e *webConversationRequestError) Error() string { return e.Detail }
func (e *webConversationRequestError) Unwrap() error { return e.Cause }

func writeWebConversationError(w http.ResponseWriter, err error) {
	var requestErr *webConversationRequestError
	if errors.As(err, &requestErr) {
		payload := map[string]any{"message": requestErr.Detail}
		if requestErr.Code != "" {
			w.Header().Set(webErrorCodeHeader, requestErr.Code)
			payload["code"] = requestErr.Code
		}
		writeWorkspaceJSON(w, requestErr.Status, payload)
		return
	}
	var messageErr *compatibilityFrameMessageError
	if errors.As(err, &messageErr) {
		writeWorkspaceJSON(w, messageErr.Status, map[string]any{"message": messageErr.Detail})
		return
	}
	status := workspaceStatus(err)
	if status < 400 {
		status = http.StatusInternalServerError
	}
	message := err.Error()
	if status >= http.StatusInternalServerError {
		message = "unable to process conversation request"
	}
	writeWorkspaceJSON(w, status, map[string]any{"message": message})
}

func decodeWebConversationJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebConversationBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func webString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}
