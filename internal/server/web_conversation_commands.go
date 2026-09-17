package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) handleWebConversationMessageSubmission(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	var input webConversationSendInput
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid message request: " + err.Error()})
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "message content is required"})
		return
	}
	if err := s.validateWebConversationSendInput(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame, &input); err != nil {
		writeWebConversationError(w, err)
		return
	}
	originalFiles := append([]string(nil), input.Files...)
	materializedFiles, err := s.materializeWebConversationInputFiles(r.Context(), frame, input.Files)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	input.Files = materializedFiles
	input.Content = rewriteWebConversationAttachedFilePaths(input.Content, originalFiles, materializedFiles)
	artifactReferences, err := s.validateWebConversationArtifactReferences(
		r.Context(), strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame, input.ArtifactRefs, input.MessageContext,
	)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	inputData := map[string]any{"request": input.Content}
	if len(input.Files) > 0 {
		inputData["files"] = input.Files
	}
	if len(input.InjectSkills) > 0 {
		inputData["inject_skills"] = input.InjectSkills
	}
	if len(input.InjectMCPServerIDs) > 0 {
		inputData["inject_mcp_server_ids"] = input.InjectMCPServerIDs
	}
	for key, value := range input.SessionOptions {
		inputData[key] = value
	}
	var model *string
	if value := strings.TrimSpace(webString(input.SessionOptions["model"])); value != "" {
		model = &value
	}
	var effort *string
	if value := strings.TrimSpace(webString(input.SessionOptions["effort"])); value != "" {
		effort = &value
	}
	targetBranchID := strings.TrimSpace(webString(input.SessionOptions["target_branch_id"]))
	delete(inputData, "target_branch_id")
	expectedBranchID := strings.TrimSpace(webString(input.SessionOptions["expected_branch_id"]))
	delete(inputData, "expected_branch_id")
	expectedGeneration, _ := webPositiveSafeInteger(input.SessionOptions["expected_generation"])
	delete(inputData, "expected_generation")
	result, err := s.submitCompatibilityFrameMessage(frame, compatibilityFrameMessageRequest{
		InputData: inputData, Model: model, Effort: effort,
		TargetBranchID: targetBranchID, ExpectedBranchID: expectedBranchID,
		ExpectedGeneration: expectedGeneration, ClientMutationID: strings.TrimSpace(input.LoadingID),
		SessionConfig:      webConversationRunnerSessionConfig(input.SessionOptions),
		ArtifactReferences: artifactReferences, MessageContext: input.MessageContext,
	})
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if err := s.publishWebConversationListChange(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame.ID, "updated"); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "message accepted but realtime delivery failed"})
		return
	}
	runtimeFrame := frame
	runtimeFrame.Status = "processing"
	writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{
		"msg_id": result.MessageID, "turn_id": frame.ID,
		"runtime": webConversationRuntime(runtimeFrame),
	})
}

func (s *Server) handleWebConversationAssociated(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	projectName string,
) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	items := make([]map[string]any, 0, len(frame.ChildIDs))
	for _, childID := range frame.ChildIDs {
		child, found, err := s.workspaceStore.GetCompatibilityFrame(childID)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		if found {
			items = append(items, webConversation(child, projectName))
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, items)
}

func (s *Server) handleWebConversationRuntimeEnsure(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	runtime, err := s.webConversationRuntimeSnapshot(frame)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	options, err := s.webConversationConfigOptions(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"recovered": true, "config_options": options, "runtime": runtime,
	})
}

func (s *Server) handleWebConversationActiveLease(w http.ResponseWriter, r *http.Request, _ workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebConversationReset(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	result, err := s.cancelCompatibilityFrameTree(r.Context(), frame, "conversation_reset")
	if err != nil {
		if s.transcriptStore != nil {
			err = transcriptWebStorageError(err)
		}
		writeWebConversationError(w, err)
		return
	}
	for _, event := range result.Events {
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "conversation reset but terminal delivery failed"})
			return
		}
	}
	runtimeCloseCtx, cancelRuntimeClose := compatibilityRuntimeCleanupContext(r.Context())
	defer cancelRuntimeClose()
	cleanupWarnings := s.stopCompatibilityFrameRuntime(runtimeCloseCtx, frame.RootFrameID)
	cleanupWarnings = append(cleanupWarnings, s.removeCompatibilityFrameRuntime(frame.RootFrameID)...)
	if err := s.publishWebConversationListChange(strings.TrimSpace(r.Header.Get("X-Synon-User-Id")), frame.ID, "updated"); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "conversation reset but realtime delivery failed"})
		return
	}
	if len(cleanupWarnings) > 0 {
		log.Printf("compatibility conversation reset committed with runtime cleanup warnings: conversation_id=%s failures=%d errors=%v", frame.ID, len(cleanupWarnings), errors.Join(cleanupWarnings...))
		w.Header().Set("X-Synon-Runtime-Cleanup-Warnings", strconv.Itoa(len(cleanupWarnings)))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebConversationConfigOption(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, rawOptionID string) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	optionID, err := url.PathUnescape(rawOptionID)
	if err != nil || optionID != "model" && optionID != "mode" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation config option not found"})
		return
	}
	var input struct {
		Value string `json:"value"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		message := "invalid model selection"
		if optionID == "mode" {
			message = "invalid permission mode"
		}
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": message})
		return
	}
	input.Value = strings.TrimSpace(input.Value)
	if input.Value == "" {
		message := "model selection is required"
		if optionID == "mode" {
			message = "permission mode is required"
		}
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": message})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if optionID == "mode" {
		canonical, valid := normalizeWebAssistantPermissionMode(input.Value)
		if !valid {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "selected permission mode is not supported"})
			return
		}
		if err := s.setWebConversationPermissionMode(r.Context(), userID, frame, canonical); err != nil {
			writeWebConversationError(w, err)
			return
		}
		options, err := s.webConversationConfigOptions(userID, frame)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		observed := false
		for _, option := range options {
			if option["id"] == "mode" && option["current_value"] == webConversationPermissionOptionValue(canonical) {
				observed = true
				break
			}
		}
		if !observed {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "permission mode activation was not observed"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"confirmation": "observed", "applies_to": "current_and_next_tool_calls", "config_options": options,
		})
		return
	}
	choices, err := s.webConversationModelChoices(userID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	selectionFound := false
	for _, choice := range choices {
		if choice.Value == input.Value {
			selectionFound = true
			break
		}
	}
	if !selectionFound {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "selected model is not enabled for this user"})
		return
	}
	modelUpdate, err := s.workspaceStore.SetCompatibilityConversationModel(frame.ID, input.Value)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to update the conversation model"})
		return
	}
	options, err := s.webConversationConfigOptions(userID, frame)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	observed := false
	for _, option := range options {
		if option["id"] == "model" && webString(option["current_value"]) == input.Value {
			observed = true
			break
		}
	}
	if !observed {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "model activation was not observed"})
		return
	}
	response := map[string]any{
		"confirmation": "observed", "applies_to": "next_model_call", "config_options": options,
	}
	resumeCtx, cancelResume := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	resumeState, wakeErr := s.wakeModelProviderUnavailableDispatch(resumeCtx, frame, modelUpdate.Revision)
	cancelResume()
	if wakeErr != nil {
		log.Printf("conversation model switch persisted but durable resume signal was deferred: frame_id=%s revision=%d error=%v", frame.ID, modelUpdate.Revision, wakeErr)
		response["resume"] = "deferred"
	} else if resumeState != workspace.CompatibilityFrameResumeDispatchWakeNone {
		response["resume"] = string(resumeState)
	}
	writeWorkspaceJSON(w, http.StatusOK, response)
}
