package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleSubmitRequestCompatibility(w http.ResponseWriter, r *http.Request) {
	s.handleSubmitRequestCompatibilityForProject(w, r, "", true)
}

func (s *Server) handleProjectSubmitRequestCompatibility(w http.ResponseWriter, r *http.Request, projectID string) {
	s.handleSubmitRequestCompatibilityForProject(w, r, projectID, false)
}

func (s *Server) handleSubmitRequestCompatibilityForProject(
	w http.ResponseWriter,
	r *http.Request,
	pathProjectID string,
	requireTargetAgent bool,
) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	userID := compatAgentUserID(r)
	if s.sessionStore == nil || s.eventJournal == nil || s.sessionSockets == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Session runtime is not configured")
		return
	}
	var input workspaceProjectRequestInput
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := strings.TrimSpace(pathProjectID)
	if projectID == "" {
		projectID = firstNonEmpty(input.ProjectID, input.ProjectIDSnake)
	}
	requestText := projectRequestText(input)
	agentName := firstNonEmpty(input.TargetAgent, input.TargetAgentSnake)
	if !requireTargetAgent && agentName == "" {
		agentName = "OPERON"
	}
	if projectID == "" || requestText == "" || requireTargetAgent && agentName == "" {
		writeV11Detail(w, http.StatusBadRequest, "project_id, target_agent, and input_data.request are required")
		return
	}
	input.TargetAgent = agentName
	onboardingMode := firstNonEmpty(input.OnboardingMode, input.OnboardingModeSnake)
	artifactReferences, messageContext, err := compatibilityStructuredOnboardingInput(input, agentName, onboardingMode)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if onboardingMode != "" && s.transcriptStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Structured onboarding transcript authority is unavailable")
		return
	}
	owned, err := store.ProjectOwnedBy(projectID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
		return
	}
	hidden := false
	if onboardingMode != "" && s.agentCatalog == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Structured onboarding agent authority is unavailable")
		return
	}
	if s.agentCatalog != nil {
		agent, found := s.agentCatalog.Agent(agentName)
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Agent "+agentName+" not found in registry")
			return
		}
		hidden = agent.UserHidden
		if onboardingMode != "" && !hidden {
			writeV11Detail(w, http.StatusServiceUnavailable, "Structured onboarding hidden agent authority is unavailable")
			return
		}
	}
	clientMessageID := firstNonEmpty(input.IntentID, input.IntentIDSnake, input.ClientMessageID, input.ClientMessageIDSnake)
	if clientMessageID == "" {
		clientMessageID = uuid.NewString()
	}
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	frameID := firstNonEmpty(input.FrameID, input.FrameIDSnake, input.RootFrameID, input.RootFrameIDSnake)
	hasFrameLocator := frameID != ""
	if frameID == "" {
		frameID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(projectID+"\x00"+clientMessageID)).String()
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeV11Detail(w, workspaceStatus(err), err.Error())
		return
	}
	created := false
	if found && !hasFrameLocator {
		writeV11Detail(w, http.StatusConflict, "intent_id "+clientMessageID+" was already used \u2014 ids are minted once per send")
		return
	}
	if !found {
		frame, err = store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: projectID, AgentName: agentName,
			Status: "processing", ConversationType: "agent", Name: compactText(requestText, 120),
		})
		if err != nil {
			writeV11Detail(w, workspaceStatus(err), err.Error())
			return
		}
		created = true
		inputData := projectRequestInputData(input)
		inputData["request"] = requestText
		if err := store.SetFrameSubmissionMetadata(frame.ID, inputData, hidden); err != nil {
			_ = store.DeleteFrame(frame.ID)
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
		taskSummary := compactText(requestText, 500)
		if _, err := store.UpdateCompatibilityFrame(frame.ID, workspace.UpdateCompatibilityFrameInput{TaskSummary: &taskSummary}); err != nil {
			_ = store.DeleteFrame(frame.ID)
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
		createdEvent, eventErr := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: frame.ID, Type: "frame_created",
			Payload: map[string]any{
				"projectId": projectID, "agentName": agentName,
				"status": "processing", "conversationType": "agent",
			},
		})
		if eventErr != nil || s.publishWorkspaceEvent(createdEvent) != nil {
			_ = store.DeleteFrame(frame.ID)
			writeV11Detail(w, http.StatusInternalServerError, "Could not persist submitted frame")
			return
		}
	} else if frame.ProjectID != projectID || frame.RootFrameID != frame.ID {
		writeV11Detail(w, http.StatusConflict, "Frame belongs to another conversation")
		return
	}
	dedupePayload := compatibilityRequestDedupePayload(input, requestText, artifactReferences, messageContext)
	deliveryPayload := compatibilityRequestDeliveryPayload(input, projectID, agentName, clientMessageID, requestText, artifactReferences, messageContext)
	if disposition, exists, dispositionErr := store.ResolveCompatibilityMessageIntent(frame.ID, clientMessageID, dedupePayload); dispositionErr != nil {
		if created {
			_ = store.DeleteFrame(frame.ID)
		}
		writeCompatibilityIntentError(w, dispositionErr, clientMessageID, frame.ID)
		return
	} else if exists {
		writeJSON(w, http.StatusOK, map[string]any{
			"root_frame_id": frame.RootFrameID,
			"frame_id":      frame.ID,
			"status":        compatibilityIntentResponseStatus(disposition.State),
		})
		return
	}
	queued := false
	activatedExisting := false
	originalStatus := frame.Status
	if !created {
		activated, activateErr := store.ActivateCompatibilityFrameRequest(frame.ID)
		if activateErr != nil {
			writeV11Detail(w, workspaceStatus(activateErr), activateErr.Error())
			return
		}
		queued = !activated
		activatedExisting = activated
	}
	if queued {
		disposition, event, idempotent, queueErr := store.QueueCompatibilityMessage(
			frame.ID, clientMessageID, dedupePayload, deliveryPayload,
		)
		if queueErr != nil {
			writeCompatibilityIntentError(w, queueErr, clientMessageID, frame.ID)
			return
		}
		if !idempotent && s.publishWorkspaceEvent(event) != nil {
			writeV11Detail(w, http.StatusInternalServerError, "Could not persist queued message delivery")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"root_frame_id": frame.RootFrameID,
			"frame_id":      frame.ID,
			"status":        compatibilityIntentResponseStatus(disposition.State),
		})
		return
	}
	_, _, err = s.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frame.ID, MessageUUID: clientMessageID,
		ClientMessageID: clientMessageID, Text: requestText,
		InputData: projectRequestInputData(input), RuntimeConfig: projectRequestSessionConfig(input),
		ArtifactReferences: artifactReferences, MessageContext: messageContext,
	})
	if err != nil {
		if created {
			_ = store.DeleteFrame(frame.ID)
			_ = s.eventJournal.Remove(frame.ID)
		} else if activatedExisting {
			_, _ = store.UpdateFrame(frame.ID, workspace.UpdateFrameInput{Status: &originalStatus})
		}
		writeV11Detail(w, workspaceStatus(err), err.Error())
		return
	}
	if _, err := store.RecordCompatibilityDeliveredIntent(frame.ID, clientMessageID, dedupePayload, deliveryPayload); err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "Could not persist submitted intent disposition")
		return
	}
	if session, found, err := s.sessionStore.Get(frame.ID); err == nil && found {
		orchestration := copyMapAny(session.Orchestration)
		if orchestration == nil {
			orchestration = map[string]any{}
		}
		orchestration["sessionConfig"] = projectRequestSessionConfig(input)
		session.Orchestration = orchestration
		if err := s.sessionStore.Save(session); err != nil {
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": frame.RootFrameID,
		"frame_id":      frame.ID,
		"status":        "accepted",
	})
}

func compatibilityRequestDedupePayload(
	input workspaceProjectRequestInput,
	requestText string,
	artifactReferences []transcriptstore.UserArtifactReferenceInput,
	messageContext string,
) map[string]any {
	control := map[string]any{}
	for key, value := range map[string]any{
		"model":            input.Model,
		"subagent_model":   firstNonEmpty(input.SubagentModel, input.SubagentModelSnake),
		"effort":           input.Effort,
		"thinking_enabled": input.Thinking,
	} {
		if value != nil && value != "" {
			control[key] = value
		}
	}
	var normalizedControl any
	if len(control) > 0 {
		normalizedControl = control
	}
	payload := map[string]any{
		"text":       requestText,
		"plan_mode":  firstNonNil(input.PlanMode, input.PlanModeSnake),
		"ultra_mode": firstNonNil(input.UltraMode, input.UltraModeSnake),
		"control":    normalizedControl,
	}
	if goal := firstNonEmpty(input.GoalText, input.GoalTextSnake); goal != "" {
		payload["goal_text"] = goal
	}
	if len(artifactReferences) > 0 {
		payload["artifact_refs"] = compatibilityArtifactReferencePayload(artifactReferences)
	}
	if messageContext != "" {
		payload["message_context"] = messageContext
	}
	if mode := firstNonEmpty(input.OnboardingMode, input.OnboardingModeSnake); mode != "" {
		payload["onboarding_mode"] = mode
	}
	return payload
}

func compatibilityRequestDeliveryPayload(
	input workspaceProjectRequestInput,
	projectID string,
	agentName string,
	clientMessageID string,
	requestText string,
	artifactReferences []transcriptstore.UserArtifactReferenceInput,
	messageContext string,
) map[string]any {
	inputData := projectRequestInputData(input)
	inputData["request"] = requestText
	payload := map[string]any{
		"messageUuid": clientMessageID, "clientMessageId": clientMessageID,
		"role": "user", "content": requestText, "text": requestText,
		"projectId": projectID, "targetAgent": agentName,
		"inputData":     inputData,
		"sessionConfig": projectRequestSessionConfig(input),
	}
	if len(artifactReferences) > 0 {
		payload["artifactRefs"] = compatibilityArtifactReferencePayload(artifactReferences)
	}
	if messageContext != "" {
		payload["messageContext"] = messageContext
	}
	return payload
}

func compatibilityStructuredOnboardingInput(
	input workspaceProjectRequestInput,
	agentName string,
	mode string,
) ([]transcriptstore.UserArtifactReferenceInput, string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		if len(input.ArtifactRefs) > 0 {
			return nil, "", errors.New("artifact_refs require structured onboarding mode")
		}
		return nil, "", nil
	}
	if mode != structuredOnboardingModeV1 {
		return nil, "", errors.New("onboarding_mode is invalid")
	}
	if normalizeBundledAgentName(agentName) != "ONBOARDING" {
		return nil, "", errors.New("structured onboarding requires the ONBOARDING agent")
	}
	refs := make([]transcriptstore.UserArtifactReferenceInput, 0, len(input.ArtifactRefs))
	if len(input.ArtifactRefs) > structuredOnboardingMaximumAttachments {
		return nil, "", errors.New("structured onboarding supports at most 20 artifact_refs")
	}
	seen := make(map[string]struct{}, len(input.ArtifactRefs))
	for _, value := range input.ArtifactRefs {
		artifactID := strings.TrimSpace(value.ArtifactID)
		versionID := strings.TrimSpace(value.VersionID)
		if artifactID == "" || versionID == "" {
			return nil, "", errors.New("artifact_id and version_id are required")
		}
		key := artifactID + "\x00" + versionID
		if _, duplicate := seen[key]; duplicate {
			return nil, "", errors.New("artifact_refs must be unique")
		}
		seen[key] = struct{}{}
		refs = append(refs, transcriptstore.UserArtifactReferenceInput{ArtifactID: artifactID, VersionID: versionID})
	}
	if err := validateStructuredOnboardingUserData(input, len(refs)); err != nil {
		return nil, "", err
	}
	return refs, structuredOnboardingMessageContext, nil
}

func compatibilityArtifactReferencePayload(values []transcriptstore.UserArtifactReferenceInput) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{"artifact_id": value.ArtifactID, "version_id": value.VersionID})
	}
	return result
}

func compatibilityIntentResponseStatus(state string) string {
	switch state {
	case "queued":
		return "message_queued"
	case "retracted":
		return "already_retracted"
	default:
		return "already_delivered"
	}
}

func writeCompatibilityIntentError(w http.ResponseWriter, err error, intentID, frameID string) {
	switch {
	case errors.Is(err, workspace.ErrCompatibilityIntentUsed):
		writeV11Detail(w, http.StatusConflict, "intent_id "+intentID+" was already used \u2014 ids are minted once per send")
	case errors.Is(err, workspace.ErrCompatibilityIntentPayloadMismatch):
		writeV11Detail(w, http.StatusConflict, "intent_id "+intentID+" is already queued with a different payload \u2014 retries must be byte-identical; mint a new id for an edited send")
	case errors.Is(err, workspace.ErrCompatibilityMessageQueueFull):
		writeV11Detail(w, http.StatusTooManyRequests, "Queue for this conversation is full (64 pending messages) \u2014 wait for the agent to catch up")
	default:
		writeV11Detail(w, workspaceStatus(err), err.Error())
	}
}
