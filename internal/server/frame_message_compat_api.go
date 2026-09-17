package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityFrameMessageRequest struct {
	InputData          map[string]any                               `json:"input_data"`
	Model              *string                                      `json:"model"`
	Effort             *string                                      `json:"effort"`
	Thinking           *bool                                        `json:"thinking"`
	TargetBranchID     string                                       `json:"target_branch_id"`
	ExpectedBranchID   string                                       `json:"expected_branch_id"`
	ExpectedGeneration int64                                        `json:"expected_generation"`
	ClientMutationID   string                                       `json:"client_mutation_id"`
	SessionConfig      map[string]any                               `json:"-"`
	ArtifactReferences []transcriptstore.UserArtifactReferenceInput `json:"-"`
	MessageContext     string                                       `json:"-"`
}

type compatibilityBranchContinuation struct {
	TargetBranchID     string
	ExpectedBranchID   string
	ExpectedGeneration int64
	ClientMutationID   string
}

type compatibilityFrameMessageRuntimeOptions struct {
	Continuation         *compatibilityBranchContinuation
	DeferFrameActivation bool
	MessageOrigin        string
}

type compatibilityFrameMessageResult struct {
	RootFrameID string
	FrameID     string
	MessageID   string
	Status      string
}

type compatibilityFrameMessageError struct {
	Status int
	Detail string
}

const branchAuthorityUnavailableDetail = "Conversation branching is unavailable until unified transcript authority is enabled"

func (e *compatibilityFrameMessageError) Error() string { return e.Detail }

func (s *Server) handleCompatibilityFrameMessageSubmission(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityFrameMessageRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid message request: "+err.Error())
		return
	}
	if input.InputData == nil {
		writeV11Detail(w, http.StatusBadRequest, "input_data must be an object")
		return
	}

	result, err := s.submitCompatibilityFrameMessage(frame, input)
	if err != nil {
		var requestErr *compatibilityFrameMessageError
		if errors.As(err, &requestErr) {
			writeV11Detail(w, requestErr.Status, requestErr.Detail)
			return
		}
		writeV11StoreError(w, err)
		return
	}
	writeCompatibilityFrameMessageResponse(w, result)
}

func (s *Server) submitCompatibilityFrameMessage(
	frame workspace.CompatibilityFrame,
	input compatibilityFrameMessageRequest,
) (compatibilityFrameMessageResult, error) {
	if input.InputData == nil {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusBadRequest, Detail: "input_data must be an object",
		}
	}
	inputData := sanitizeCompatibilityUserInputData(input.InputData)
	if err := validateCompatibilityMessageGoal(inputData); err != nil {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusBadRequest, Detail: err.Error(),
		}
	}
	if frame.ParentFrameID != "" {
		for _, key := range []string{"plan_mode", "ultra_mode", "verifier_mode", "memory_mode"} {
			delete(inputData, key)
		}
	}
	input.InputData = inputData
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	current, found, err := s.workspaceStore.GetCompatibilityFrame(frame.ID)
	if err != nil {
		return compatibilityFrameMessageResult{}, err
	}
	if !found {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusNotFound, Detail: "Frame " + frame.ID + " not found",
		}
	}
	input.TargetBranchID = strings.TrimSpace(input.TargetBranchID)
	if input.TargetBranchID != "" {
		if current.ParentFrameID != "" {
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusBadRequest, Detail: "target_branch_id is only valid for root conversations",
			}
		}
		if s.transcriptStore != nil {
			return s.submitCompatibilityTranscriptBranchMessage(current, input)
		}
		_, found, err := s.workspaceStore.GetCompatibilityBranchMessages(current.ID, input.TargetBranchID)
		if err != nil {
			if strings.Contains(err.Error(), "branch id") {
				return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{Status: http.StatusBadRequest, Detail: "Invalid branch id"}
			}
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusInternalServerError, Detail: "Internal workspace error",
			}
		}
		if !found {
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{Status: http.StatusNotFound, Detail: "Branch not found"}
		}
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusConflict, Detail: branchAuthorityUnavailableDetail,
		}
	}
	requestText := compatibilityInputRequestText(inputData["request"])
	messageID := uuid.NewString()
	if input.ClientMutationID != "" {
		messageID = uuid.NewSHA1(
			uuid.NameSpaceURL,
			[]byte("synon-biomed:frame-message:"+current.ID+"\x00"+input.ClientMutationID),
		).String()
	}
	if requestText != "" && input.ClientMutationID != "" {
		if s.transcriptStore != nil {
			accepted, acceptedErr := s.compatibilityTranscriptMessageAccepted(current.ID, messageID)
			if acceptedErr != nil {
				if errors.Is(acceptedErr, transcriptstore.ErrEventConflict) {
					return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
						Status: http.StatusConflict, Detail: "loading_id is already bound to a different message",
					}
				}
				return compatibilityFrameMessageResult{}, acceptedErr
			}
			if accepted {
				if err := s.prepareCompatibilityFrameMessageRuntime(
					current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
					input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{
						DeferFrameActivation: isCompatibilityTerminalRootMessageResume(current),
					},
				); err != nil {
					if errors.Is(err, transcriptstore.ErrEventConflict) {
						return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
							Status: http.StatusConflict, Detail: "loading_id is already bound to a different message",
						}
					}
					return compatibilityFrameMessageResult{}, err
				}
				// A retry can arrive after the user input was durably admitted but
				// before the terminal root was resumed. Re-establish the dispatch only
				// after confirming that input exists, preserving the same ordering as
				// the first request without creating a duplicate message.
				if isCompatibilityTerminalRootMessageResume(current) {
					if err := s.resumeTerminalRootCompatibilityFrameForMessage(current, input); err != nil {
						return compatibilityFrameMessageResult{}, err
					}
				}
				return compatibilityFrameMessageResult{
					RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "accepted",
				}, nil
			}
		}
		delivery := compatibilityFrameMessageDeliveryPayload(
			current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
			input.ArtifactReferences, input.MessageContext,
		)
		if _, queued, queueErr := s.workspaceStore.ResolveCompatibilityMessageIntent(current.ID, messageID, delivery); queueErr != nil {
			return compatibilityFrameMessageResult{}, compatibilityQueuedMessageError(queueErr, messageID)
		} else if queued {
			return compatibilityFrameMessageResult{
				RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "message_queued",
			}, nil
		}
	}
	if current.Status == "processing" {
		if requestText != "" {
			delivery := compatibilityFrameMessageDeliveryPayload(current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking, input.ArtifactReferences, input.MessageContext)
			direct, directErr := s.shouldDirectlyDeliverExpiredTranscriptMessage(current.ID)
			if directErr != nil {
				return compatibilityFrameMessageResult{}, directErr
			}
			if direct {
				if err := s.prepareCompatibilityFrameMessageRuntime(
					current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
					input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{
						MessageOrigin: "input_response",
					},
				); err != nil {
					return compatibilityFrameMessageResult{}, err
				}
				if err := s.ensureExpiredTranscriptContinuationDispatch(context.Background(), current); err != nil {
					return compatibilityFrameMessageResult{}, err
				}
				return compatibilityFrameMessageResult{
					RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "accepted",
				}, nil
			}
			_, event, idempotent, queueErr := s.workspaceStore.QueueCompatibilityMessage(
				current.ID, messageID, delivery, delivery,
			)
			if queueErr != nil {
				return compatibilityFrameMessageResult{}, compatibilityQueuedMessageError(queueErr, messageID)
			}
			if !idempotent {
				if err := s.publishWorkspaceEvent(event); err != nil {
					return compatibilityFrameMessageResult{}, fmt.Errorf("message queued but realtime delivery failed: %w", err)
				}
			}
		}
		if requestText == "" {
			messageID = ""
		}
		return compatibilityFrameMessageResult{
			RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "message_queued",
		}, nil
	}
	switch current.Status {
	case "awaiting_plan_approval":
		if requestText == "" {
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusConflict, Detail: "Frame " + current.ID + " is awaiting a plan decision.",
			}
		}
		if err := s.prepareCompatibilityFrameMessageRuntime(
			current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
			input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{},
		); err != nil {
			return compatibilityFrameMessageResult{}, err
		}
		resumed, err := s.workspaceStore.CreateAutoResumeDispatch(
			current.ID, current.RootFrameID, current.ProjectID, current.AgentName, "plan_text_decision",
		)
		if err != nil {
			return compatibilityFrameMessageResult{}, err
		}
		if resumed.Event != nil {
			if err := s.registerFrameResumeDispatch(resumed.Event.ID, defaultFrameResumeReservationTTL); err != nil {
				return compatibilityFrameMessageResult{}, err
			}
			if err := s.publishWorkspaceEvent(*resumed.Event); err != nil {
				return compatibilityFrameMessageResult{}, err
			}
		}
		return compatibilityFrameMessageResult{
			RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "accepted",
		}, nil
	case "awaiting_user_response":
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusConflict, Detail: "Frame " + current.ID + " is awaiting input \u2014 answer its question card first.",
		}
	case "cancelled", "failed":
		if isCompatibilityTerminalRootMessageResume(current) && s.transcriptStore != nil && requestText != "" {
			// Admit the new task input before making the resume dispatch visible.
			// The dispatcher may claim immediately; publishing it first lets that
			// claim observe the terminal attempt's stale checkpoint and fail before
			// the user's superseding message reaches Transcript.
			if err := s.prepareCompatibilityFrameMessageRuntime(
				current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
				input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{
					DeferFrameActivation: true, MessageOrigin: "input_response",
				},
			); err != nil {
				return compatibilityFrameMessageResult{}, err
			}
			if err := s.resumeTerminalRootCompatibilityFrameForMessage(current, input); err != nil {
				return compatibilityFrameMessageResult{}, err
			}
			return compatibilityFrameMessageResult{
				RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "accepted",
			}, nil
		}
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusConflict, Detail: "Frame " + current.ID + " is " + current.Status + " \u2014 resume it from its parent before sending a message.",
		}
	case "completed":
	default:
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusBadRequest,
			Detail: "Frame not addressable. Status: " + current.Status + ". Only 'completed' frames can receive direct messages.",
		}
	}
	activated, err := s.workspaceStore.ActivateCompletedCompatibilityFrameRequest(current.ID)
	if err != nil {
		return compatibilityFrameMessageResult{}, err
	}
	if !activated {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusConflict, Detail: "Frame state changed while sending the message \u2014 refresh and retry.",
		}
	}
	if err := s.prepareCompatibilityFrameMessageRuntime(
		current, messageID, inputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
		input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{},
	); err != nil {
		completed := "completed"
		_, _ = s.workspaceStore.UpdateFrame(current.ID, workspace.UpdateFrameInput{Status: &completed})
		if errors.Is(err, transcriptstore.ErrEventConflict) {
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusConflict, Detail: "loading_id is already bound to a different message",
			}
		}
		return compatibilityFrameMessageResult{}, err
	}
	return compatibilityFrameMessageResult{
		RootFrameID: current.RootFrameID, FrameID: current.ID, MessageID: messageID, Status: "accepted",
	}, nil
}

// ensureExpiredTranscriptContinuationDispatch binds an explicit user
// continuation to the existing durable task after its runner lease expired.
// The input response is always persisted first. A single Frame resume dispatch
// then owns checkpoint recovery, so it cannot compete with the fresh-task
// runner path or leave a non-resumable correction checkpoint stuck forever.
func (s *Server) ensureExpiredTranscriptContinuationDispatch(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil || !found {
		return err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, frame.ID)
	if err != nil || !found {
		return err
	}
	state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(ctx, stream.UID, frameContext.UserID)
	if err != nil || !found {
		return err
	}
	if state.Status != "running" || state.ExpiresAt.After(time.Now().UTC()) {
		return nil
	}
	reasonCode := "explicit_user_continue"
	if interruption, found, interruptionErr := s.transcriptStore.LatestRunnerInterruption(
		ctx, stream.UID, frameContext.UserID,
	); interruptionErr != nil {
		return interruptionErr
	} else if found && strings.TrimSpace(interruption.ReasonCode) != "" {
		reasonCode = strings.TrimSpace(interruption.ReasonCode)
	}
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = frame.ID
	}
	resumed, err := s.workspaceStore.CreateAutoResumeDispatch(
		frame.ID, rootFrameID, frame.ProjectID, frame.AgentName, reasonCode,
	)
	if err != nil {
		return err
	}
	if resumed.Event == nil {
		return errors.New("explicit continuation resume dispatch was not created")
	}
	if err := s.registerFrameResumeDispatch(resumed.Event.ID, defaultFrameResumeReservationTTL); err != nil {
		return err
	}
	return s.publishWorkspaceEvent(*resumed.Event)
}

func (s *Server) compatibilityTranscriptMessageAccepted(frameID, messageID string) (bool, error) {
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		return false, err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(context.Background(), frameContext.UserID, frameID)
	if err != nil || !found {
		return false, err
	}
	event, found, err := s.transcriptStore.GetEventByClientMessageID(
		context.Background(), stream.UID, frameContext.UserID, messageID,
	)
	if err != nil || !found {
		return false, err
	}
	if event.Type != "user_message" && event.Type != "user_input_response" {
		return false, transcriptstore.ErrEventConflict
	}
	return true, nil
}

func (s *Server) submitCompatibilityTranscriptBranchMessage(
	frame workspace.CompatibilityFrame,
	input compatibilityFrameMessageRequest,
) (compatibilityFrameMessageResult, error) {
	if s.transcriptContractErr != nil {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusServiceUnavailable, Detail: "Transcript runtime is not available",
		}
	}
	input.ExpectedBranchID = strings.TrimSpace(input.ExpectedBranchID)
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	if input.ExpectedBranchID == "" || input.ExpectedGeneration <= 0 || input.ClientMutationID == "" {
		return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
			Status: http.StatusBadRequest, Detail: "target branch continuation requires expected branch state and client mutation id",
		}
	}
	messageID := "branch-continue:" + input.ClientMutationID
	continuation := &compatibilityBranchContinuation{
		TargetBranchID: input.TargetBranchID, ExpectedBranchID: input.ExpectedBranchID,
		ExpectedGeneration: input.ExpectedGeneration, ClientMutationID: input.ClientMutationID,
	}
	if err := s.prepareCompatibilityFrameMessageRuntime(
		frame, messageID, input.InputData, input.SessionConfig, input.Model, input.Effort, input.Thinking,
		input.ArtifactReferences, input.MessageContext, compatibilityFrameMessageRuntimeOptions{Continuation: continuation},
	); err != nil {
		switch {
		case errors.Is(err, transcriptstore.ErrBranchTargetNotFound), errors.Is(err, transcriptstore.ErrOwnerMismatch):
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{Status: http.StatusNotFound, Detail: "Branch not found"}
		case errors.Is(err, transcriptstore.ErrBranchRequestInvalid):
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{Status: http.StatusBadRequest, Detail: "Invalid branch id"}
		case errors.Is(err, transcriptstore.ErrBranchStateStale), errors.Is(err, transcriptstore.ErrEventConflict):
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusConflict, Detail: "Conversation branch changed; refresh and retry",
			}
		case errors.Is(err, transcriptstore.ErrSchemaUnavailable):
			return compatibilityFrameMessageResult{}, &compatibilityFrameMessageError{
				Status: http.StatusServiceUnavailable, Detail: "Transcript runtime is not available",
			}
		default:
			return compatibilityFrameMessageResult{}, err
		}
	}
	return compatibilityFrameMessageResult{
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, MessageID: messageID, Status: "accepted",
	}, nil
}

func compatibilityQueuedMessageError(err error, messageID string) error {
	switch {
	case errors.Is(err, workspace.ErrCompatibilityIntentUsed):
		return &compatibilityFrameMessageError{
			Status: http.StatusConflict,
			Detail: "intent_id " + messageID + " was already used \u2014 ids are minted once per send",
		}
	case errors.Is(err, workspace.ErrCompatibilityIntentPayloadMismatch):
		return &compatibilityFrameMessageError{
			Status: http.StatusConflict,
			Detail: "intent_id " + messageID + " is already queued with a different payload \u2014 retries must be byte-identical; mint a new id for an edited send",
		}
	case errors.Is(err, workspace.ErrCompatibilityMessageQueueFull):
		return &compatibilityFrameMessageError{
			Status: http.StatusTooManyRequests,
			Detail: "Queue for this conversation is full (64 pending messages) \u2014 wait for the agent to catch up",
		}
	default:
		return err
	}
}

func isCompatibilityTerminalRootMessageResume(frame workspace.CompatibilityFrame) bool {
	return frame.ParentFrameID == "" && (frame.Status == "failed" || frame.Status == "cancelled")
}

func (s *Server) resumeTerminalRootCompatibilityFrameForMessage(
	frame workspace.CompatibilityFrame,
	input compatibilityFrameMessageRequest,
) error {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return errors.New("transcript root frame recovery runtime is unavailable")
	}
	result, err := s.workspaceStore.ResumeCompatibilityFrameConversation(frame.ID, workspace.ResumeCompatibilityFrameInput{
		Model: input.Model,
	})
	if err != nil {
		return err
	}
	if result.Event == nil {
		return nil
	}
	if err := s.registerFrameResumeDispatch(result.Event.ID, defaultFrameResumeReservationTTL); err != nil {
		return fmt.Errorf("root frame resumed but dispatch registration failed: %w", err)
	}
	if err := s.publishWorkspaceEvent(*result.Event); err != nil {
		return fmt.Errorf("root frame resumed but event delivery failed: %w", err)
	}
	return nil
}

// shouldDirectlyDeliverExpiredTranscriptMessage closes the recovery gap after
// a bounded transcript interruption. An active runner must keep using the
// compatibility queue, but an expired lease cannot consume that queue because
// the frame is intentionally non-terminal; appending the explicit new user
// message to transcript makes the normal runner claim path wake without an
// unbounded retry or a hidden state transition.
func (s *Server) shouldDirectlyDeliverExpiredTranscriptMessage(frameID string) (bool, error) {
	if s == nil || s.transcriptStore == nil || s.workspaceStore == nil {
		return false, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frameID)
	if err != nil || !found {
		return false, err
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(
		context.Background(), frameContext.UserID, frameID,
	)
	if err != nil || !found {
		return false, err
	}
	state, found, err := s.transcriptStore.GetLatestRunnerRuntimeState(
		context.Background(), stream.UID, frameContext.UserID,
	)
	if err != nil || !found {
		return false, err
	}
	if state.Phase == transcriptstore.RunnerPhaseWaitingApproval ||
		state.Phase == transcriptstore.RunnerPhaseWaitingUser ||
		state.Phase == transcriptstore.RunnerPhaseWaitingExternal {
		// An expired lease is intentional at a durable waiting checkpoint: the
		// approval/user-response path owns the wake. A new message must remain
		// in the canonical queue until that checkpoint is resolved, otherwise
		// it can be appended to the same stream while the old turn is still
		// waiting and the model sees two logical tasks in one generation.
		return false, nil
	}
	return state.Status == "running" && !state.ExpiresAt.After(time.Now().UTC()), nil
}

func sanitizeCompatibilityUserInputData(input map[string]any) map[string]any {
	clean := make(map[string]any, len(input))
	for key, value := range input {
		if !strings.HasPrefix(key, "_") {
			clean[key] = value
		}
	}
	return clean
}

func validateCompatibilityMessageGoal(input map[string]any) error {
	value, found := input["goal_text"]
	if !found || value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok || text == "" || len([]rune(text)) > 4000 || strings.TrimSpace(text) == "" {
		return fmt.Errorf("goal_text must be a non-empty string of at most 4,000 characters, or null to clear")
	}
	return fmt.Errorf("goal_text is not available in this build")
}

func compatibilityInputRequestText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(typed)
	case []any:
		parts := make([]string, len(typed))
		for index := range typed {
			parts[index] = compatibilityInputRequestText(typed[index])
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func compatibilityFrameMessageDeliveryPayload(
	frame workspace.CompatibilityFrame,
	messageID string,
	inputData map[string]any,
	sessionConfig map[string]any,
	model, effort *string,
	thinking *bool,
	artifactReferences []transcriptstore.UserArtifactReferenceInput,
	messageContext string,
) map[string]any {
	config := copyMapAny(sessionConfig)
	if config == nil {
		config = map[string]any{}
	}
	config["agentName"] = frame.AgentName
	if model != nil && *model != "" {
		config["model"] = *model
	}
	if effort != nil && *effort != "" {
		config["effort"] = *effort
	}
	if thinking != nil {
		config["thinking_enabled"] = *thinking
	}
	payload := map[string]any{
		"messageUuid": messageID, "clientMessageId": messageID,
		"role": "user", "text": compatibilityInputRequestText(inputData["request"]),
		"inputData": copyMapAny(inputData), "sessionConfig": config,
	}
	if len(artifactReferences) > 0 {
		payload["artifactRefs"] = artifactReferences
	}
	if messageContext != "" {
		payload["messageContext"] = messageContext
	}
	return payload
}

func (s *Server) prepareCompatibilityFrameMessageRuntime(
	frame workspace.CompatibilityFrame,
	messageID string,
	inputData map[string]any,
	sessionConfig map[string]any,
	model, effort *string,
	thinking *bool,
	artifactReferences []transcriptstore.UserArtifactReferenceInput,
	messageContext string,
	options compatibilityFrameMessageRuntimeOptions,
) error {
	requestText := compatibilityInputRequestText(inputData["request"])
	if requestText == "" {
		requestText = "[System] Continue this frame with the supplied structured input."
	}
	payload := compatibilityFrameMessageDeliveryPayload(
		frame, messageID, inputData, sessionConfig, model, effort, thinking, artifactReferences, messageContext,
	)
	runtimeConfig, _ := payload["sessionConfig"].(map[string]any)
	if s.transcriptStore != nil {
		submission := frameMessageSubmission{
			FrameID: frame.ID, MessageUUID: messageID, ClientMessageID: messageID, Text: requestText,
			MessageOrigin:      strings.TrimSpace(options.MessageOrigin),
			InputData:          copyMapAny(inputData),
			RuntimeConfig:      runtimeConfig,
			ArtifactReferences: artifactReferences, MessageContext: messageContext,
			DeferFrameActivation: options.DeferFrameActivation,
		}
		if options.Continuation != nil {
			submission.TargetBranchID = options.Continuation.TargetBranchID
			submission.ExpectedBranchID = options.Continuation.ExpectedBranchID
			submission.ExpectedGeneration = options.Continuation.ExpectedGeneration
			submission.ClientMutationID = options.Continuation.ClientMutationID
		}
		_, _, err := s.submitFrameMessage(s.workspaceStore, submission)
		return err
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		return err
	}
	if !found {
		metadata = workspace.FrameRuntimeMetadata{FrameID: frame.ID, ContextData: map[string]any{}}
	}
	metadata.ContextData = copyMapAny(metadata.ContextData)
	if metadata.ContextData == nil {
		metadata.ContextData = map[string]any{}
	}
	metadata.ContextData["_is_manual_continuation"] = true
	if model != nil && *model != "" {
		metadata.ContextData["_model"] = *model
	}
	if effort != nil && *effort != "" {
		metadata.ContextData["_effort"] = *effort
	}
	if thinking != nil {
		metadata.ContextData["_thinking"] = *thinking
	}
	if _, err := s.workspaceStore.SetFrameRuntimeMetadata(frame.ID, metadata); err != nil {
		return err
	}
	if _, _, err := s.submitFrameMessage(s.workspaceStore, frameMessageSubmission{
		FrameID: frame.ID, MessageUUID: messageID, ClientMessageID: messageID, Text: requestText,
		InputData:     inputData,
		RuntimeConfig: runtimeConfig, ArtifactReferences: artifactReferences, MessageContext: messageContext,
	}); err != nil {
		return err
	}
	if err := s.applyCompatibilityQueuedSessionConfig(frame.ID, payload); err != nil {
		return err
	}
	session, found, err := s.sessionStore.Get(frame.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("frame message session %q was not created", frame.ID)
	}
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		orchestration = map[string]any{}
	}
	orchestration["inputData"] = copyMapAny(inputData)
	session.Orchestration = orchestration
	return s.sessionStore.Save(session)
}

func writeCompatibilityFrameMessageResponse(w http.ResponseWriter, result compatibilityFrameMessageResult) {
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": result.RootFrameID, "frame_id": result.FrameID,
		"message_id": result.MessageID, "status": result.Status,
	})
}
