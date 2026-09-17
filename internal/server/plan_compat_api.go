package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityApprovePlanRequest struct {
	EditedPlan   map[string]any `json:"edited_plan"`
	VerifierMode *string        `json:"verifier_mode"`
	MemoryMode   *string        `json:"memory_mode"`
	UltraMode    *bool          `json:"ultra_mode"`
	PlanMode     *bool          `json:"plan_mode"`
	TargetAgent  *string        `json:"target_agent"`
}

const compatibilityPlanApprovalText = "[System] The user approved the proposed plan. Continue execution."

func (s *Server) handleCompatibilityApprovePlan(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityApprovePlanRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid approve-plan request: "+err.Error())
		return
	}
	if !validateCompatibilityPlanModes(w, input.VerifierMode, input.MemoryMode) {
		return
	}
	found, err := s.validateCompatibilityTargetAgent(r, input.TargetAgent)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusBadRequest, "Agent "+stringPointerValue(input.TargetAgent)+" not found in registry")
		return
	}

	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	current, found, err := s.workspaceStore.GetCompatibilityFrame(frame.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(current.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	contextData := copyMapAny(metadata.ContextData)
	if contextData == nil {
		contextData = map[string]any{}
	}
	if stringValue(contextData["_plan_artifact_id"]) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Frame has no plan to approve. Status: "+current.Status+".")
		return
	}
	expectedPlanArtifact := stringValue(contextData["_plan_artifact_id"])
	expectedPlanVersion := stringValue(contextData["_plan_version_id"])
	identityBaseVersion := expectedPlanVersion
	if storedBase := stringValue(contextData["_plan_approval_base_version_id"]); storedBase != "" {
		identityBaseVersion = storedBase
	}
	controls := compatibilityPlanControlInput(input)
	runtimeConfig := compatibilityPlanRuntimeConfig(current.AgentName, controls)
	approvalID, approvalFingerprint, identityErr := workspace.BuildCompatibilityPlanApprovalIdentity(
		workspace.CompatibilityPlanApprovalIdentityInput{
			FrameID: current.ID, ArtifactID: expectedPlanArtifact, BaseVersionID: identityBaseVersion,
			Text: compatibilityPlanApprovalText, AgentName: stringValue(runtimeConfig["agentName"]),
			RuntimeConfig: runtimeConfig, EditedPlan: input.EditedPlan,
		},
	)
	if identityErr != nil {
		writeV11StoreError(w, identityErr)
		return
	}
	idempotentReplay := false
	if compatibilityPlanBool(contextData["_plan_approved"]) {
		if stringValue(contextData["_plan_approval_id"]) == approvalID &&
			stringValue(contextData["_plan_approval_fingerprint"]) == approvalFingerprint {
			idempotentReplay = true
		} else {
			writeCompatibilityPlanError(w, "This plan has already been approved.", "plan_already_approved")
			return
		}
	}
	if !idempotentReplay && current.Status == "processing" {
		writeCompatibilityPlanError(w, "LLM is still working. You can approve the plan once it finishes.", "plan_frame_processing")
		return
	}
	if !idempotentReplay && current.Status != "awaiting_plan_approval" {
		writeV11Detail(w, http.StatusBadRequest, "No plan awaiting approval. Frame status changed \u2014 refresh and retry.")
		return
	}
	originalContext := copyMapAny(contextData)
	contextData["_plan_approved"] = true
	contextData["_plan_approved_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if input.EditedPlan != nil {
		contextData["_plan_json"] = copyMapAny(input.EditedPlan)
		contextData["_step_statuses"] = map[string]any{}
		resetGeneratedPlanResearchSourceCursors(contextData)
		contextData["_plan_step_denials"] = float64(0)
		contextData["_plan_steps"] = nil
	}
	if s.transcriptStore != nil {
		var editedPlanJSON []byte
		if input.EditedPlan != nil {
			var encodeErr error
			editedPlanJSON, encodeErr = json.MarshalIndent(input.EditedPlan, "", "  ")
			if encodeErr != nil {
				writeV11StoreError(w, encodeErr)
				return
			}
		}
		result, err := s.workspaceStore.ApproveCompatibilityPlanWithTranscript(r.Context(), workspace.ApproveCompatibilityPlanWithTranscriptInput{
			FrameID: current.ID, ApprovalID: approvalID, ApprovalFingerprint: approvalFingerprint, ClientMessageID: approvalID,
			Text:                 compatibilityPlanApprovalText,
			ExpectedPlanArtifact: expectedPlanArtifact, ExpectedPlanVersion: identityBaseVersion,
			AgentName: stringValue(runtimeConfig["agentName"]), ContextData: contextData, RuntimeConfig: runtimeConfig,
			EditedPlanJSON: editedPlanJSON,
		})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !result.Idempotent {
			s.signalTranscriptWebDelivery()
			if err := s.publishWorkspaceEvent(result.ResumeEvent); err != nil {
				log.Printf("plan approval event projection deferred frame=%s event=%s; durable resume remains authoritative", current.ID, result.ResumeEvent.ID)
			}
		}
		if result.BlobFinalizeDeferred {
			log.Printf("plan edited artifact blob finalization deferred frame=%s version=%s; durable marker will recover", current.ID, result.EditedPlanVersionID)
		}
		writeCompatibilityFrameMessageResponse(w, compatibilityFrameMessageResult{
			RootFrameID: result.Frame.RootFrameID, FrameID: result.Frame.ID, Status: "accepted",
		})
		return
	}
	if input.EditedPlan != nil {
		versionID, saveErr := s.saveCompatibilityEditedPlan(current, contextData, input.EditedPlan)
		if saveErr != nil {
			writeV11Detail(w, http.StatusBadRequest, saveErr.Error())
			return
		}
		contextData["_plan_version_id"] = versionID
	}
	claimed, err := s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "awaiting_plan_approval", "processing")
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !claimed {
		writeV11Detail(w, http.StatusBadRequest, "No plan awaiting approval. Frame status changed \u2014 refresh and retry.")
		return
	}
	metadata.ContextData = contextData
	if _, err := s.workspaceStore.SetFrameRuntimeMetadata(current.ID, metadata); err != nil {
		_, _ = s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "processing", "awaiting_plan_approval")
		writeV11StoreError(w, err)
		return
	}
	if err := s.prepareCompatibilityPlanApprovalRuntime(current, controls); err != nil {
		metadata.ContextData = originalContext
		_, _ = s.workspaceStore.SetFrameRuntimeMetadata(current.ID, metadata)
		_, _ = s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "processing", "awaiting_plan_approval")
		writeV11StoreError(w, err)
		return
	}
	writeCompatibilityFrameMessageResponse(w, compatibilityFrameMessageResult{
		RootFrameID: current.RootFrameID, FrameID: current.ID, Status: "accepted",
	})
}

func (s *Server) handleCompatibilityDiscardPlan(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body map[string]json.RawMessage
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid discard-plan request: "+err.Error())
		return
	}
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	current, found, err := s.workspaceStore.GetCompatibilityFrame(frame.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(current.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	contextData := copyMapAny(metadata.ContextData)
	if contextData == nil {
		contextData = map[string]any{}
	}
	if current.Status != "awaiting_plan_approval" {
		if current.Status == "processing" && compatibilityPlanBool(contextData["_plan_approved"]) {
			writeCompatibilityPlanError(w, "This plan has already been approved.", "plan_already_approved")
			return
		}
		if current.Status == "processing" {
			writeCompatibilityPlanError(w, "LLM is still working. Try again once it finishes.", "plan_frame_processing")
			return
		}
		writeV11Detail(w, http.StatusBadRequest, "No plan awaiting approval. Frame status: "+current.Status+".")
		return
	}
	if s.transcriptStore != nil {
		settled, event, _, err := s.workspaceStore.DiscardCompatibilityPlanWithTranscript(r.Context(), current.ID, "")
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeCompatibilityFrameMessageResponse(w, compatibilityFrameMessageResult{
			RootFrameID: settled.RootFrameID, FrameID: settled.ID, Status: "completed",
		})
		return
	}
	claimed, err := s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "awaiting_plan_approval", "completed")
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !claimed {
		writeV11Detail(w, http.StatusBadRequest, "No plan awaiting approval \u2014 frame state changed while discarding.")
		return
	}
	originalContext := copyMapAny(contextData)
	planArtifactID := stringValue(contextData["_plan_artifact_id"])
	for _, key := range compatibilityDiscardedPlanContextKeys {
		delete(contextData, key)
	}
	metadata.ContextData = contextData
	if _, err := s.workspaceStore.SetFrameRuntimeMetadata(current.ID, metadata); err != nil {
		_, _ = s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "completed", "awaiting_plan_approval")
		writeV11StoreError(w, err)
		return
	}
	if err := s.recordCompatibilityPlanDiscard(current, planArtifactID); err != nil {
		metadata.ContextData = originalContext
		_, _ = s.workspaceStore.SetFrameRuntimeMetadata(current.ID, metadata)
		_, _ = s.workspaceStore.TransitionCompatibilityFrameStatus(current.ID, "completed", "awaiting_plan_approval")
		writeV11StoreError(w, err)
		return
	}
	writeCompatibilityFrameMessageResponse(w, compatibilityFrameMessageResult{
		RootFrameID: current.RootFrameID, FrameID: current.ID, Status: "completed",
	})
}

var compatibilityDiscardedPlanContextKeys = workspace.CompatibilityPlanContextKeys()

func validateCompatibilityPlanModes(w http.ResponseWriter, verifier, memory *string) bool {
	if err := validateCompatibilityPlanModeValues(verifier, memory); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func compatibilityPlanControlInput(input compatibilityApprovePlanRequest) map[string]any {
	controls := map[string]any{"_plan_approved": true}
	for key, value := range map[string]any{
		"verifier_mode": input.VerifierMode, "memory_mode": input.MemoryMode,
		"ultra_mode": input.UltraMode, "plan_mode": input.PlanMode,
	} {
		switch typed := value.(type) {
		case *string:
			if typed != nil {
				controls[key] = *typed
			}
		case *bool:
			if typed != nil {
				controls[key] = *typed
			}
		}
	}
	if input.TargetAgent != nil {
		controls["agentName"] = *input.TargetAgent
	}
	return controls
}

func compatibilityPlanRuntimeConfig(defaultAgent string, controls map[string]any) map[string]any {
	config := map[string]any{"agentName": strings.TrimSpace(defaultAgent)}
	for key, value := range controls {
		if key != "_plan_approved" {
			config[key] = value
		}
	}
	return config
}

func (s *Server) prepareCompatibilityPlanApprovalRuntime(frame workspace.CompatibilityFrame, controls map[string]any) error {
	messageID := uuid.NewString()
	config := compatibilityPlanRuntimeConfig(frame.AgentName, controls)
	if target := stringValue(config["agentName"]); target != "" {
		frame.AgentName = target
	}
	if _, _, err := s.submitFrameMessage(s.workspaceStore, frameMessageSubmission{
		FrameID: frame.ID, MessageUUID: messageID, ClientMessageID: messageID,
		Text: "[System] The user approved the proposed plan. Continue execution.", MessageOrigin: "input_response",
		RuntimeConfig: config,
	}); err != nil {
		return err
	}
	if s.transcriptStore != nil {
		return nil
	}
	return s.applyCompatibilityQueuedSessionConfig(frame.ID, map[string]any{"sessionConfig": config})
}

func (s *Server) recordCompatibilityPlanDiscard(frame workspace.CompatibilityFrame, artifactID string) error {
	messageID := uuid.NewString()
	message := map[string]any{
		"type": "user_message", "role": "user", "messageUuid": messageID,
		"clientMessageId": messageID, "_uuid": messageID, "_harness_notice": true,
		"_plan_discarded": true,
		"text":            "[System] The user discarded the proposed plan without approving it. Do not execute it. Wait for further instructions.",
	}
	if artifactID != "" {
		message["_plan_discarded_artifact_id"] = artifactID
	}
	message["content"] = []any{map[string]any{"type": "text", "text": message["text"]}}
	if err := s.appendClientWebSocketMessage(frame.ID, "user", message); err != nil {
		return err
	}
	session, found, err := s.sessionStore.Get(frame.ID)
	if err != nil {
		return err
	}
	if found {
		session.LastRole = "system"
		if err := s.sessionStore.Save(session); err != nil {
			return err
		}
	}
	event, err := s.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: frame.ID, Type: "user_message", Payload: message,
	})
	if err != nil {
		return err
	}
	return s.publishWorkspaceEvent(event)
}

func compatibilityPlanBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func writeCompatibilityPlanError(w http.ResponseWriter, detail, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"detail": detail, "code": code})
}
