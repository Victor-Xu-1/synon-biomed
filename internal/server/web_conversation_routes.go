package server

import (
	"errors"
	"github.com/google/uuid"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
	"unicode/utf8"
)

const (
	maxWebConversationBodyBytes          = 1 << 20
	maxWebConversationNameRunes          = 255
	defaultWebMessagePageSize            = 200
	maxWebMessagePageSize                = 100000
	webConversationActiveProviderSetting = "model.activeProviderId"
	webConversationRuntimeMetadataCode   = "CONVERSATION_RUNTIME_METADATA_UNAVAILABLE"
	webConversationRuntimeStateCode      = "CONVERSATION_RUNTIME_STATE_UNAVAILABLE"
)

type webConversationAssistantInput struct {
	ID                    string         `json:"id"`
	Locale                string         `json:"locale"`
	ConversationOverrides map[string]any `json:"conversation_overrides"`
}

type webConversationCreateInput struct {
	Type      string                         `json:"type"`
	ID        string                         `json:"id"`
	Name      string                         `json:"name"`
	Assistant *webConversationAssistantInput `json:"assistant"`
	Extra     map[string]any                 `json:"extra"`
}

type webConversationSendInput struct {
	Content            string                                  `json:"content"`
	Files              []string                                `json:"files"`
	ArtifactRefs       []webConversationArtifactReferenceInput `json:"artifact_refs"`
	MessageContext     string                                  `json:"message_context"`
	LoadingID          string                                  `json:"loading_id"`
	InjectSkills       []string                                `json:"inject_skills"`
	InjectMCPServerIDs []string                                `json:"inject_mcp_server_ids"`
	SessionOptions     map[string]any                          `json:"session_options"`
}

func (s *Server) handleWebConversation(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "workspace storage is not configured"})
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/conversations/"))
	if len(segments) == 0 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation endpoint not found"})
		return
	}
	if len(segments) == 1 && segments[0] == "clone" {
		s.handleWebConversationClone(w, r)
		return
	}
	if len(segments) == 1 && segments[0] == "active-count" {
		s.handleWebConversationActiveCount(w, r)
		return
	}
	if len(segments) == 1 && segments[0] == "search" {
		s.handleWebConversationSearch(w, r)
		return
	}
	conversationID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(conversationID) == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid conversation id"})
		return
	}
	frame, project, ok := s.webConversationAccess(w, r, conversationID)
	if !ok {
		return
	}
	switch {
	case len(segments) == 1:
		s.handleWebConversationRecord(w, r, frame, project)
	case len(segments) == 2 && segments[1] == "lineage":
		s.handleWebConversationLineage(w, r, frame)
	case len(segments) >= 2 && segments[1] == "messages":
		s.handleWebConversationMessages(w, r, frame, segments)
	case len(segments) == 2 && segments[1] == "artifacts":
		s.handleWebConversationArtifacts(w, r, frame)
	case len(segments) >= 2 && segments[1] == "workspace":
		s.handleWebConversationWorkspace(w, r, frame, project, segments[2:])
	case len(segments) == 2 && segments[1] == "associated":
		s.handleWebConversationAssociated(w, r, frame, project.Name)
	case len(segments) == 2 && segments[1] == "reset":
		s.handleWebConversationReset(w, r, frame)
	case len(segments) == 3 && segments[1] == "runtime" && segments[2] == "ensure":
		s.handleWebConversationRuntimeEnsure(w, r, frame)
	case len(segments) == 2 && segments[1] == "active-lease":
		s.handleWebConversationActiveLease(w, r, frame)
	case len(segments) == 3 && segments[1] == "config-options":
		s.handleWebConversationConfigOption(w, r, frame, segments[2])
	case len(segments) == 2 && segments[1] == "slash-commands":
		s.handleWebConversationSlashCommands(w, r, frame)
	case len(segments) == 2 && segments[1] == "composer-capabilities":
		s.handleWebConversationComposerCapabilities(w, r, frame)
	case len(segments) == 2 && segments[1] == "context-usage":
		s.handleWebContextUsage(w, r, frame)
	case len(segments) == 2 && segments[1] == "side-question":
		s.handleWebConversationSideQuestion(w, r, frame)
	case len(segments) == 2 && segments[1] == "confirmations":
		s.handleWebConversationConfirmations(w, r, frame)
	case len(segments) == 4 && segments[1] == "confirmations" && segments[3] == "confirm":
		s.handleWebConversationConfirmation(w, r, frame, segments[2])
	case len(segments) == 3 && segments[1] == "approvals" && segments[2] == "check":
		s.handleWebConversationApprovalCheck(w, r, frame)
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation endpoint not found"})
	}
}

func (s *Server) createWebConversation(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	var input webConversationCreateInput
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid conversation request: " + err.Error()})
		return
	}
	frame, project, err := s.createWebConversationRecord(r, userID, input)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if err := s.publishWebConversationListChange(userID, frame.ID, "created"); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "conversation created but realtime delivery failed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusCreated, webConversation(frame, project.Name))
}

func (s *Server) createWebConversationRecord(
	r *http.Request,
	userID string,
	input webConversationCreateInput,
) (workspace.CompatibilityFrame, workspace.CompatibilityProject, error) {
	if s.transcriptContractErr != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, transcriptWebStorageError(s.transcriptContractErr)
	}
	project, err := s.resolveWebConversationProject(userID, input.Extra)
	if err != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	frameID := strings.TrimSpace(input.ID)
	if frameID == "" {
		frameID = uuid.NewString()
	} else if _, err := uuid.Parse(frameID); err != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "conversation id must be a UUID",
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "New conversation"
	}
	if utf8.RuneCountInString(name) > maxWebConversationNameRunes {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "conversation name exceeds 255 characters",
		}
	}
	agentName, assistantMetadata, err := s.resolveWebConversationAssistant(userID, input.Assistant)
	if err != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	created, err := s.workspaceStore.CreateFrameRealtime(r.Context(), workspace.CreateFrameInput{
		ID: frameID, ProjectID: project.ID, AgentName: agentName,
		Status: "completed", ConversationType: "agent", Name: name,
	}, userID, "", "")
	if err != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	contextData := map[string]any{"web_extra": copyMapAny(input.Extra)}
	if assistantMetadata != nil {
		contextData["web_assistant"] = assistantMetadata
	}
	if _, err := s.workspaceStore.SetFrameRuntimeMetadata(created.ID, workspace.FrameRuntimeMetadata{
		FrameID: created.ID, ContextData: contextData, TaskSummary: name,
	}); err != nil {
		_, _ = s.workspaceStore.DeleteCompatibilityFrameTree(created.ID, userID, created.IncarnationID)
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	if _, err := s.ensureTranscriptFrameStream(r.Context(), userID, created.ID); err != nil {
		_, _ = s.workspaceStore.DeleteCompatibilityFrameTree(created.ID, userID, created.IncarnationID)
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	frame, found, err := s.workspaceStore.GetCompatibilityFrame(created.ID)
	if err != nil {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, err
	}
	if !found {
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, errors.New("created conversation disappeared")
	}
	return frame, project, nil
}

func (s *Server) resolveWebConversationProject(userID string, extra map[string]any) (workspace.CompatibilityProject, error) {
	projectID := webConversationProjectID(extra)
	if projectID != "" {
		project, found, err := s.workspaceStore.GetCompatibilityProject(userID, projectID)
		if err != nil {
			return workspace.CompatibilityProject{}, err
		}
		if !found {
			return workspace.CompatibilityProject{}, &webConversationRequestError{
				Status: http.StatusNotFound, Detail: "project not found",
			}
		}
		return project, nil
	}
	name := strings.TrimSpace(webString(extra["project_name"]))
	if name == "" {
		name = "Personal Workspace"
	}
	projectID, err := newCompatibilityProjectID()
	if err != nil {
		return workspace.CompatibilityProject{}, err
	}
	project, _, err := s.workspaceStore.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: projectID, UserID: userID, Name: name, FindOrCreate: true,
	})
	return project, err
}

func webConversationProjectID(extra map[string]any) string {
	if extra == nil {
		return ""
	}
	if projectID := strings.TrimSpace(webString(extra["project_id"])); projectID != "" {
		return projectID
	}
	workspaceURL := strings.TrimSpace(webString(extra["workspace"]))
	for _, prefix := range []string{"synonbiomed://project/", "synonbiomed://"} {
		if strings.HasPrefix(workspaceURL, prefix) {
			value, err := url.PathUnescape(strings.TrimPrefix(workspaceURL, prefix))
			if err == nil && value != "" && !strings.Contains(value, "/") {
				return value
			}
		}
	}
	return ""
}

func (s *Server) webConversationAgentName(userID string, assistant *webConversationAssistantInput) (string, error) {
	agentName, _, err := s.resolveWebConversationAssistant(userID, assistant)
	return agentName, err
}

func (s *Server) resolveWebConversationAssistant(
	userID string,
	assistant *webConversationAssistantInput,
) (string, map[string]any, error) {
	if assistant == nil {
		agentName, err := s.resolveWebAssistantRuntimeAgent(userID, webAssistantID("OPERON"))
		return agentName, nil, err
	}
	value := strings.TrimSpace(assistant.ID)
	if value == "" || len(value) > 256 {
		return "", nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant id is invalid"}
	}
	record, found, err := s.webAssistantRecord(userID, value)
	if err != nil {
		return "", nil, err
	}
	if !found || !record.Agent.Enabled {
		return "", nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "selected assistant is unavailable or disabled"}
	}
	overrides, err := s.normalizeWebAssistantConversationOverrides(userID, record, assistant.ConversationOverrides)
	if err != nil {
		return "", nil, err
	}
	agentName, err := s.resolveWebAssistantRuntimeAgent(userID, value)
	if err != nil {
		var requestErr *agentProfileRequestError
		if errors.As(err, &requestErr) {
			return "", nil, &webConversationRequestError{Status: requestErr.Status, Detail: requestErr.Detail}
		}
		return "", nil, err
	}
	locale := strings.TrimSpace(assistant.Locale)
	if len(locale) > 64 {
		return "", nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "assistant locale is invalid"}
	}
	return agentName, map[string]any{
		"id": record.RuntimeID, "locale": locale, "conversation_overrides": overrides,
	}, nil
}

func (s *Server) webConversationAccess(
	w http.ResponseWriter,
	r *http.Request,
	conversationID string,
) (workspace.CompatibilityFrame, workspace.CompatibilityProject, bool) {
	frame, found, err := s.workspaceStore.GetCompatibilityFrame(conversationID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load conversation"})
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, false
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation not found"})
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, false
	}
	project, owned, err := s.workspaceStore.GetCompatibilityProject(userID, frame.ProjectID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to authorize conversation"})
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, false
	}
	if !owned {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation not found"})
		return workspace.CompatibilityFrame{}, workspace.CompatibilityProject{}, false
	}
	return frame, project, true
}

func (s *Server) handleWebConversationRecord(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	project workspace.CompatibilityProject,
) {
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	switch r.Method {
	case http.MethodGet:
		conversation, err := s.webConversationSnapshot(frame, project.Name)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, conversation)
	case http.MethodPatch:
		var input struct {
			Name        *string        `json:"name"`
			Description *string        `json:"desc"`
			Extra       map[string]any `json:"extra"`
			MergeExtra  bool           `json:"merge_extra"`
		}
		if err := decodeWebConversationJSON(w, r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid conversation update: " + err.Error()})
			return
		}
		if input.Name != nil {
			trimmed := strings.TrimSpace(*input.Name)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > maxWebConversationNameRunes {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "conversation name must contain 1 to 255 characters"})
				return
			}
			input.Name = &trimmed
		}
		_, err := s.workspaceStore.UpdateCompatibilityFrame(frame.ID, workspace.UpdateCompatibilityFrameInput{
			Name: input.Name, TaskSummary: input.Description,
		})
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		if input.Extra != nil {
			metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
			if err != nil {
				writeWebConversationError(w, err)
				return
			}
			if !found {
				metadata = workspace.FrameRuntimeMetadata{FrameID: frame.ID, ContextData: map[string]any{}}
			}
			metadata.ContextData = copyMapAny(metadata.ContextData)
			if metadata.ContextData == nil {
				metadata.ContextData = map[string]any{}
			}
			if input.MergeExtra {
				current, _ := metadata.ContextData["web_extra"].(map[string]any)
				merged := copyMapAny(current)
				if merged == nil {
					merged = map[string]any{}
				}
				for key, value := range input.Extra {
					merged[key] = value
				}
				metadata.ContextData["web_extra"] = merged
			} else {
				metadata.ContextData["web_extra"] = copyMapAny(input.Extra)
			}
			if _, err := s.workspaceStore.SetFrameRuntimeMetadata(frame.ID, metadata); err != nil {
				writeWebConversationError(w, err)
				return
			}
		}
		if err := s.publishWebConversationListChange(userID, frame.ID, "updated"); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "conversation updated but realtime delivery failed"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, true)
	case http.MethodDelete:
		runtimeCloseCtx, cancelRuntimeClose := compatibilityRuntimeCleanupContext(r.Context())
		defer cancelRuntimeClose()
		cleanupWarnings := s.stopCompatibilityFrameRuntime(runtimeCloseCtx, frame.RootFrameID)
		_, err := s.workspaceStore.DeleteCompatibilityFrameTree(frame.ID, userID, frame.IncarnationID)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		cleanupWarnings = append(cleanupWarnings, s.stopCompatibilityFrameRuntime(runtimeCloseCtx, frame.RootFrameID)...)
		cleanupWarnings = append(cleanupWarnings, s.removeCompatibilityFrameRuntime(frame.RootFrameID)...)
		if len(cleanupWarnings) > 0 {
			log.Printf("compatibility conversation delete committed with runtime cleanup warnings: conversation_id=%s failures=%d errors=%v", frame.ID, len(cleanupWarnings), errors.Join(cleanupWarnings...))
			w.Header().Set("X-Synon-Runtime-Cleanup-Warnings", strconv.Itoa(len(cleanupWarnings)))
		}
		writeWorkspaceJSON(w, http.StatusOK, true)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	}
}
