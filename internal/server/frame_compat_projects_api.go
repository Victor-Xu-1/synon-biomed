package server

import (
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
)

const v11DefaultContextLimit = defaultRunnerContextWindow

func (s *Server) handleProjectsCompatibility(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	userID := compatAgentUserID(r)
	switch r.Method {
	case http.MethodGet:
		projects, err := s.workspaceStore.ListCompatibilityProjects(
			userID,
			compatPositiveQuery(r, "limit", 100, 1000),
			compatNonNegativeQuery(r, "offset", 0),
		)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		total, err := s.workspaceStore.CountCompatibilityProjects(userID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		response := make([]map[string]any, 0, len(projects))
		for _, project := range projects {
			item := compatibilityProjectResponse(project)
			lastActive, err := s.workspaceStore.CompatibilityProjectLastActiveAt(userID, project.ID)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			item["last_active_at"] = lastActive
			response = append(response, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": response, "total": total})
	case http.MethodPost:
		var input struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			Context      any    `json:"context"`
			FindOrCreate bool   `json:"find_or_create"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project request: "+err.Error())
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			writeV11Detail(w, http.StatusBadRequest, "Project name is required")
			return
		}
		projectID, err := newCompatibilityProjectID()
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		project, created, err := s.workspaceStore.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
			ID: projectID, UserID: userID, Name: input.Name, Description: input.Description,
			ContextData: input.Context, FindOrCreate: input.FindOrCreate,
		})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if created {
			if _, err := s.publishProjectEvent(project.ID, "frame_update", map[string]any{"action": "project_created"}); err != nil {
				writeV11StoreError(w, fmt.Errorf("project created but event delivery failed: %w", err))
				return
			}
		}
		status := http.StatusCreated
		if input.FindOrCreate {
			status = http.StatusOK
		}
		writeJSON(w, status, compatibilityProjectResponse(project))
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleFramesCompatibility(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	userID := compatAgentUserID(r)
	switch r.Method {
	case http.MethodGet:
		rootOnly := r.URL.Query().Get("root_only") == "true" || r.URL.Query().Get("root_only") == "1"
		limit := compatPositiveQuery(r, "limit", 100, 1000)
		frames, err := s.workspaceStore.ListVisibleCompatibilityFrames(
			userID, strings.TrimSpace(r.URL.Query().Get("project_id")), rootOnly,
			limit,
		)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		response := make([]map[string]any, 0, len(frames))
		for _, frame := range frames {
			projection, err := s.compatibilityFrameResponse(frame, false, map[string]bool{})
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			response = append(response, projection)
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var input struct {
			ProjectID string `json:"project_id"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid frame request: "+err.Error())
			return
		}
		projectID := strings.TrimSpace(input.ProjectID)
		owned, err := s.workspaceStore.ProjectOwnedBy(projectID, userID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !owned {
			writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
			return
		}
		frameID := uuid.NewString()
		frame, err := s.workspaceStore.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "completed", ConversationType: "agent",
		})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if _, err := s.publishProjectEvent(projectID, "frame_update", map[string]any{
			"action": "frame_created", "frame_id": frame.ID, "root_frame_id": frame.RootFrameID,
		}); err != nil {
			writeV11StoreError(w, fmt.Errorf("frame created but event delivery failed: %w", err))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"project_id": projectID, "root_frame_id": frame.RootFrameID})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleFrameCompatibility(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/frames/"))
	if len(segments) == 0 {
		writeV11Detail(w, http.StatusNotFound, "Frame endpoint not found")
		return
	}
	frameID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(frameID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Invalid frame id")
		return
	}
	if len(segments) == 2 && (segments[1] == "export" || segments[1] == "bundle") {
		s.handleFrameExport(w, r)
		return
	}
	frame, found, err := s.workspaceStore.GetCompatibilityFrame(frameID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	userID := compatAgentUserID(r)
	if !found {
		if len(segments) == 2 && segments[1] == "artifacts" {
			writeV11Detail(w, http.StatusNotFound, "Conversation "+frameID+" not found")
			return
		}
		writeV11Detail(w, http.StatusNotFound, "Frame "+frameID+" not found")
		return
	}
	owned, err := s.workspaceStore.ProjectOwnedBy(frame.ProjectID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frameID+" not found")
		return
	}

	if len(segments) == 1 {
		s.handleCompatibilityFrameRecord(w, r, frame, userID)
		return
	}
	if segments[1] == "messages" {
		s.handleCompatibilityFrameMessages(w, r, frame, segments)
		return
	}
	if len(segments) == 2 && segments[1] == "branches" {
		s.handleCompatibilityTranscriptBranches(w, r, frame)
		return
	}
	if len(segments) == 4 && segments[1] == "branches" && segments[3] == "messages" {
		s.handleCompatibilityBranchMessages(w, r, frame, segments[2])
		return
	}
	if len(segments) == 2 && segments[1] == "token-classes" {
		s.handleCompatibilityFrameTokenClasses(w, r, frame, userID)
		return
	}
	if len(segments) == 2 && segments[1] == "message" {
		s.handleCompatibilityFrameMessageSubmission(w, r, frame)
		return
	}
	if (len(segments) == 2 || len(segments) == 3) && segments[1] == "transcript-annotations" {
		s.handleCompatibilityTranscriptAnnotations(w, r, frame, segments)
		return
	}
	if len(segments) == 2 && segments[1] == "read-cursor" {
		s.handleCompatibilityFrameReadCursor(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "cancel" {
		s.handleCompatibilityFrameCancel(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "resume" {
		s.handleCompatibilityFrameResume(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "session-config" {
		s.handleCompatibilitySessionConfig(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "resolve-input" {
		s.handleCompatibilityResolveInput(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "approve-plan" {
		s.handleCompatibilityApprovePlan(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "discard-plan" {
		s.handleCompatibilityDiscardPlan(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "aside" {
		s.handleCompatibilityCreateAside(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "fork" {
		s.handleCompatibilityRootFork(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "fork-at-answer" {
		s.handleCompatibilityRootForkAtAnswer(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "streaming" {
		s.handleCompatibilityFrameStreaming(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "streaming-batch" {
		s.handleCompatibilityFrameStreamingBatch(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "cross-session-refs" {
		s.handleCompatibilityCrossSessionReferences(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "artifacts" {
		if frame.ParentFrameID != "" {
			writeV11Detail(w, http.StatusNotFound, "Conversation "+frameID+" not found")
			return
		}
		s.handleCompatibilityConversationArtifacts(w, r, frame, userID)
		return
	}
	if len(segments) == 2 && segments[1] == "move" {
		s.handleCompatibilityFrameMove(w, r, frame, userID)
		return
	}
	if len(segments) == 2 && segments[1] == "compaction-archives" {
		s.handleCompatibilityCompactionArchives(w, r, frame)
		return
	}
	if len(segments) == 3 && segments[1] == "compaction-archives" {
		s.handleCompatibilityCompactionArchive(w, r, frame, segments[2])
		return
	}
	if len(segments) == 3 && segments[1] == "queued-messages" {
		s.handleCompatibilityQueuedMessage(w, r, frame, segments[2])
		return
	}
	if len(segments) == 2 && segments[1] == "execution-log" {
		s.handleCompatibilityExecutionLog(w, r, frame, userID)
		return
	}
	if len(segments) == 2 && segments[1] == "verification" {
		s.handleCompatibilityVerification(w, r, frame, userID)
		return
	}
	if len(segments) == 2 && segments[1] == "audit" {
		s.handleCompatibilityFrameAudit(w, r, frame)
		return
	}
	if len(segments) == 2 && segments[1] == "trace-shallow" ||
		len(segments) == 3 && segments[1] == "artifacts" {
		s.handleFrameExport(w, r)
		return
	}
	writeV11Detail(w, http.StatusNotFound, "Frame endpoint not found")
}

func (s *Server) handleCompatibilityCrossSessionReferences(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	references, err := s.workspaceStore.ListCrossSessionArtifactRefs(frame.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": references})
}

func (s *Server) handleCompatibilityFrameMove(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, userID string) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input struct {
		TargetProjectID string `json:"target_project_id"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid move request: "+err.Error())
		return
	}
	input.TargetProjectID = strings.TrimSpace(input.TargetProjectID)
	if input.TargetProjectID == "" {
		writeV11Detail(w, http.StatusBadRequest, "target_project_id is required")
		return
	}
	owned, err := s.workspaceStore.ProjectOwnedBy(input.TargetProjectID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Project "+input.TargetProjectID+" not found")
		return
	}
	result, err := s.workspaceStore.MoveCompatibilityConversationToProject(frame.ID, input.TargetProjectID)
	if err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "does not exist"):
			writeV11Detail(w, http.StatusNotFound, err.Error())
		case strings.Contains(message, "not a conversation root"),
			strings.Contains(message, "cannot be moved"),
			strings.Contains(message, "cannot move session"):
			writeV11Detail(w, http.StatusBadRequest, err.Error())
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	if result.Event != nil {
		if err := s.publishWorkspaceEvent(*result.Event); err != nil {
			writeV11StoreError(w, fmt.Errorf("conversation moved but event delivery failed: %w", err))
			return
		}
		if _, err := s.publishProjectEvent(result.FromProjectID, "frame_update", map[string]any{
			"action": "frame_moved", "frame_id": result.RootFrameID,
			"root_frame_id": result.RootFrameID, "project_id": result.FromProjectID,
		}); err != nil {
			writeV11StoreError(w, fmt.Errorf("conversation moved but source project event delivery failed: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": result.RootFrameID, "from_project_id": result.FromProjectID,
		"to_project_id": result.ToProjectID, "frames_moved": result.FramesMoved,
		"artifacts_moved": result.ArtifactsMoved,
	})
}
