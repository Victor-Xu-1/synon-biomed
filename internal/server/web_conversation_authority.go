package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) webConversationConfigOptions(userID string, frame workspace.CompatibilityFrame) ([]map[string]any, error) {
	options := make([]map[string]any, 0, 2)
	modelOption, found, err := s.webConversationModelOption(userID, frame)
	if err != nil {
		return nil, err
	}
	if found {
		options = append(options, modelOption)
	}
	permissionOption, found, err := s.webConversationPermissionOption(frame)
	if err != nil {
		return nil, err
	}
	if found {
		options = append(options, permissionOption)
	}
	return options, nil
}

func (s *Server) webConversationPermissionOption(frame workspace.CompatibilityFrame) (map[string]any, bool, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found {
		return nil, false, err
	}
	if _, ok := metadata.ContextData["web_assistant"].(map[string]any); !ok {
		return nil, false, nil
	}
	frameContext, contextFound, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil || !contextFound {
		return nil, false, err
	}
	assistant := metadata.ContextData["web_assistant"].(map[string]any)
	assistantID := strings.TrimSpace(webString(assistant["id"]))
	if assistantID == "" {
		return nil, false, nil
	}
	if _, assistantFound, err := s.webAssistantRecord(frameContext.UserID, assistantID); err != nil || !assistantFound {
		return nil, false, err
	}
	selection, selectionFound, err := s.webSessionPermissionSelection(frame.ID)
	if err != nil {
		return nil, false, err
	}
	if !selectionFound {
		return nil, false, nil
	}
	currentValue := webConversationPermissionOptionValue(selection)
	options := []map[string]any{
		{"value": "default", "name": "Default", "description": "Ask before every tool operation that requires authorization"},
		{"value": "smart", "name": "Smart", "description": "Auto-approve routine sandboxed work and ask only for risky operations"},
		{"value": "bypassPermissions", "name": "Bypass Permissions", "description": "Auto-approve every tool operation in the backend without approval prompts"},
	}
	if currentValue == "deny" {
		options = append(options, map[string]any{"value": "deny", "name": "Deny all", "description": "Deny tool operations"})
	}
	return map[string]any{
		"id": "mode", "category": "mode", "option_type": "select", "type": "select",
		"current_value": currentValue, "options": options,
	}, true, nil
}

func webConversationPermissionOptionValue(mode string) string {
	switch mode {
	case "confirm", "smart":
		return "smart"
	case "allow":
		return "bypassPermissions"
	case "deny":
		return "deny"
	default:
		return "default"
	}
}

func (s *Server) setWebConversationPermissionMode(
	ctx context.Context,
	userID string,
	frame workspace.CompatibilityFrame,
	canonical string,
) error {
	authorityFrame, err := s.webConversationPermissionAuthorityFrame(frame)
	if err != nil {
		return err
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(authorityFrame.ID)
	if err != nil {
		return err
	}
	if !found {
		return &webConversationRequestError{Status: http.StatusConflict, Detail: "conversation assistant metadata is unavailable"}
	}
	assistant, ok := metadata.ContextData["web_assistant"].(map[string]any)
	if !ok {
		return &webConversationRequestError{Status: http.StatusConflict, Detail: "conversation assistant metadata is unavailable"}
	}
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	if overrides == nil {
		overrides = map[string]any{}
	}
	// Keep the safe default explicit. Removing the key would make the runtime
	// fall back to assistant-level defaults and split authority between two UI
	// controls again.
	overrides["permission"] = canonical
	assistant["conversation_overrides"] = overrides
	assistant["permission_changed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	assistant["permission_change_source"] = "composer"
	metadata.ContextData["web_assistant"] = assistant
	if _, err = s.workspaceStore.SetFrameRuntimeMetadata(authorityFrame.ID, metadata); err != nil {
		return err
	}
	return s.reconcileWebConversationPendingTree(
		ctx, userID, authorityFrame, webConversationPermissionRuntimeMode(canonical),
	)
}

func (s *Server) webConversationPermissionAuthorityFrame(
	frame workspace.CompatibilityFrame,
) (workspace.CompatibilityFrame, error) {
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" || rootFrameID == frame.ID {
		return frame, nil
	}
	root, found, err := s.workspaceStore.GetCompatibilityFrame(rootFrameID)
	if err != nil {
		return workspace.CompatibilityFrame{}, err
	}
	if !found || root.ID != rootFrameID || root.RootFrameID != rootFrameID || root.ProjectID != frame.ProjectID {
		return workspace.CompatibilityFrame{}, &webConversationRequestError{
			Status: http.StatusConflict, Detail: "conversation permission root authority is unavailable",
		}
	}
	return root, nil
}

func (s *Server) reconcileWebConversationPendingTree(
	ctx context.Context,
	userID string,
	frame workspace.CompatibilityFrame,
	mode string,
) error {
	authorityFrame, err := s.webConversationPermissionAuthorityFrame(frame)
	if err != nil {
		return err
	}
	activeFrames, err := s.workspaceStore.ListActiveFramesForRoot(authorityFrame.ProjectID, authorityFrame.ID)
	if err != nil {
		return err
	}
	frames := make([]workspace.Frame, 0, len(activeFrames)+1)
	frames = append(frames, authorityFrame.Frame)
	for _, candidate := range activeFrames {
		if candidate.ID != authorityFrame.ID {
			frames = append(frames, candidate)
		}
	}
	for _, candidate := range frames {
		current, found, err := s.workspaceStore.GetCompatibilityFrame(candidate.ID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := s.reconcileWebConversationPendingTools(ctx, userID, current, mode); err != nil {
			return fmt.Errorf("reconcile permission for frame %s: %w", candidate.ID, err)
		}
	}
	return nil
}

// reconcileWebConversationPendingTools applies a newly selected composer mode
// to already-visible approval requests. AskUser questions are not permissions
// and remain untouched. Each request is resolved through the same durable
// input-resolution authority used by the frontend; no synthetic frontend click
// or second approval protocol is introduced.
func (s *Server) reconcileWebConversationPendingTools(
	ctx context.Context,
	userID string,
	frame workspace.CompatibilityFrame,
	mode string,
) error {
	confirmations, err := s.webConversationPendingConfirmationsContext(ctx, frame.ID)
	if err != nil {
		return err
	}
	for _, confirmation := range confirmations {
		currentMode, err := s.webSessionApprovalMode(frame.ID)
		if err != nil {
			return err
		}
		if currentMode != mode {
			// A newer composer selection owns the decision. Its request handler
			// will reconcile the same durable pending set with the newer mode.
			return nil
		}
		kind := strings.ToLower(strings.TrimSpace(webString(confirmation["kind"])))
		if kind == "" || kind == "ask" {
			continue
		}
		requestID := strings.TrimSpace(webString(confirmation["id"]))
		if requestID == "" {
			continue
		}
		decision, err := webPermissionDecision(mode, webPendingPermissionRisky(confirmation))
		if err != nil {
			return err
		}
		if decision == "ask" {
			continue
		}
		current, found, err := s.workspaceStore.GetCompatibilityFrame(frame.ID)
		if err != nil {
			return err
		}
		if !found {
			return &webConversationRequestError{Status: http.StatusNotFound, Detail: "conversation not found"}
		}
		approved := decision == "allow"
		action := "deny"
		if approved {
			action = "allow_once"
		}
		resolutionContext := withCompatibilityApprovalResolutionAuthority(ctx, compatibilityApprovalResolutionAuthority{
			Source: "policy", ActorID: "system",
		})
		request, err := http.NewRequestWithContext(resolutionContext, http.MethodPost, "/api/frames/"+frame.ID+"/resolve-input", nil)
		if err != nil {
			return err
		}
		request.Header.Set("X-Synon-User-Id", strings.TrimSpace(userID))
		if _, err := s.resolveCompatibilityInput(request, current, compatibilityResolveInputRequest{
			Responses: []compatibilityInputResponse{{
				RequestID: requestID, Action: action, Approved: &approved, Scope: "once",
			}},
		}); err != nil {
			latest, latestErr := s.webConversationPendingConfirmationsContext(ctx, frame.ID)
			if latestErr == nil && !webConfirmationRequestPending(latest, requestID) {
				continue
			}
			return fmt.Errorf("reconcile pending %s request %s: %w", kind, requestID, err)
		}
	}
	return nil
}

func webConfirmationRequestPending(confirmations []map[string]any, requestID string) bool {
	want := strings.TrimSpace(requestID)
	for _, confirmation := range confirmations {
		if strings.TrimSpace(webString(confirmation["id"])) == want {
			return true
		}
	}
	return false
}

func webPendingPermissionRisky(confirmation map[string]any) bool {
	kind := strings.ToLower(strings.TrimSpace(webString(confirmation["kind"])))
	switch kind {
	case "local_exec":
		return false
	case agentToolApprovalKind:
		return agentRuntimeSmartApprovalRisk(webString(confirmation["tool"]), nil)
	case "network", "host", "mcp_tool", "artifact_delete", "capability_install":
		return true
	default:
		return true
	}
}

func (s *Server) wakeModelProviderUnavailableDispatch(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	modelRevision int64,
) (workspace.CompatibilityFrameResumeDispatchWakeState, error) {
	if s == nil || s.workspaceStore == nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, nil
	}
	event, state, err := s.workspaceStore.SignalCompatibilityFrameResumeDispatchModelSwitch(
		strings.TrimSpace(frame.ID), modelRevision, sessionRunnerModelProviderUnavailableReasonCode,
	)
	if err != nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	if strings.TrimSpace(event.ID) != "" {
		if publishErr := s.publishWorkspaceEvent(event); publishErr != nil {
			log.Printf("durable model-switch resume signal committed but realtime delivery was deferred: frame_id=%s event_id=%s error=%v", frame.ID, event.ID, publishErr)
		}
	}
	if state != workspace.CompatibilityFrameResumeDispatchWakeNone {
		return state, nil
	}
	if s.transcriptStore == nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(strings.TrimSpace(frame.ID))
	if err != nil || !found {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	authority, found, err := s.transcriptStore.GetFrameAuthorityBySession(ctx, frameContext.UserID, frame.ID)
	if err != nil || !found || strings.TrimSpace(authority.ActiveStreamUID) == "" {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	interruption, found, err := s.transcriptStore.LatestRunnerInterruption(
		ctx, authority.ActiveStreamUID, frameContext.UserID,
	)
	if err != nil || !found || interruption.ReasonCode != sessionRunnerModelProviderUnavailableReasonCode {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = strings.TrimSpace(frame.ID)
	}
	resumed, err := s.workspaceStore.CreateAutoResumeDispatch(
		frame.ID, rootFrameID, frame.ProjectID, frame.AgentName, interruption.ReasonCode,
	)
	if err != nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	if resumed.Event == nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone,
			errors.New("model switch resume dispatch event was not created")
	}
	if err := s.registerFrameResumeDispatch(resumed.Event.ID, defaultFrameResumeReservationTTL); err != nil {
		return workspace.CompatibilityFrameResumeDispatchWakeNone, err
	}
	if publishErr := s.publishWorkspaceEvent(*resumed.Event); publishErr != nil {
		log.Printf("model-switch resume dispatch committed but realtime delivery was deferred: frame_id=%s event_id=%s error=%v", frame.ID, resumed.Event.ID, publishErr)
	}
	return workspace.CompatibilityFrameResumeDispatchWakeWoken, nil
}

type webConversationModelChoice struct {
	Provider workspace.ModelProvider
	Value    string
}

func (s *Server) webConversationModelChoices(userID string) ([]webConversationModelChoice, error) {
	providers, err := s.workspaceStore.ListModelProviders(userID)
	if err != nil {
		return nil, err
	}
	modelCounts := make(map[string]int, len(providers))
	for _, provider := range providers {
		if provider.Enabled && strings.TrimSpace(provider.Model) != "" {
			modelCounts[strings.TrimSpace(provider.Model)]++
		}
	}
	choices := make([]webConversationModelChoice, 0, len(providers))
	for _, provider := range providers {
		model := strings.TrimSpace(provider.Model)
		if !provider.Enabled || model == "" {
			continue
		}
		value := model
		if modelCounts[model] > 1 {
			value = "provider:" + provider.ID
		}
		choices = append(choices, webConversationModelChoice{Provider: provider, Value: value})
	}
	sort.Slice(choices, func(i, j int) bool {
		left := strings.ToLower(choices[i].Provider.Model + "\x00" + choices[i].Provider.Name + "\x00" + choices[i].Provider.ID)
		right := strings.ToLower(choices[j].Provider.Model + "\x00" + choices[j].Provider.Name + "\x00" + choices[j].Provider.ID)
		return left < right
	})
	return choices, nil
}

func (s *Server) webConversationModelOption(userID string, conversation ...workspace.CompatibilityFrame) (map[string]any, bool, error) {
	choices, err := s.webConversationModelChoices(userID)
	if err != nil || len(choices) == 0 {
		return nil, false, err
	}
	conversationSelection := ""
	if len(conversation) > 0 {
		conversationSelection, err = s.webConversationFrameModelSelection(conversation[0])
		if err != nil {
			return nil, false, err
		}
	}
	if s.settingsStore == nil {
		return nil, false, errors.New("model settings storage is not configured")
	}
	activeProviderID := ""
	if setting, found, err := s.settingsStore.Get(webConversationActiveProviderSetting); err != nil {
		return nil, false, err
	} else if found {
		activeProviderID = strings.TrimSpace(webString(setting.Value))
	}
	currentValue := ""
	options := make([]map[string]any, 0, len(choices))
	for _, choice := range choices {
		if conversationSelection != "" && choice.Value == conversationSelection {
			currentValue = choice.Value
		} else if conversationSelection == "" && choice.Provider.ID == activeProviderID {
			currentValue = choice.Value
		}
		description := strings.TrimSpace(choice.Provider.Name)
		if providerType := strings.TrimSpace(choice.Provider.Type); providerType != "" {
			if description != "" {
				description += " - "
			}
			description += providerType
		}
		options = append(options, map[string]any{
			"value": choice.Value, "name": choice.Provider.Model, "description": description,
		})
	}
	return map[string]any{
		"id": "model", "category": "model", "option_type": "select", "type": "select",
		"current_value": currentValue, "options": options,
	}, true, nil
}
