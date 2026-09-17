package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceDashboard(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	dashboard, err := store.DashboardForUser(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "projects": dashboard.Projects,
		"totals": map[string]any{
			"projects": dashboard.ProjectCount, "benches": dashboard.BenchCount,
			"processing": dashboard.ProcessingCount, "artifacts": dashboard.ArtifactCount,
		},
	})
}

func (s *Server) handleWorkspaceProcessingCounts(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	counts, err := store.GetProcessingCountsForUser(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "byStatus": counts.ByStatus, "totalProcessing": counts.TotalProcessing})
}

func (s *Server) handleWorkspaceProjectBatch(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, resource string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	projectIDs, err := workspaceProjectIDs(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	limit := workspaceListQuery(r, "limit", 100, 1)
	query := r.URL.Query().Get("q")
	projects := make(map[string]any, len(projectIDs))
	for _, projectID := range projectIDs {
		if !workspaceProjectOwned(w, store, projectID, userID) {
			return
		}
		switch resource {
		case "benches":
			items, err := store.ListBenches(projectID, limit, query)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			projects[projectID] = items
		case "artifacts":
			items, err := store.ListArtifacts(projectID, limit, 0)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			projects[projectID] = items
		default:
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "batch resource not found"})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "projects": projects})
}

func workspaceProjectIDs(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("pids"))
	if raw == "" {
		return nil, errors.New("pids is required")
	}
	seen := map[string]bool{}
	ids := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		id := strings.TrimSpace(item)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("at least one project id is required")
	}
	if len(ids) > 100 {
		return nil, errors.New("project batch is limited to 100 ids")
	}
	return ids, nil
}

type workspaceProjectRequestInput struct {
	ProjectID            string                                  `json:"projectId"`
	ProjectIDSnake       string                                  `json:"project_id"`
	FrameID              string                                  `json:"frameId"`
	FrameIDSnake         string                                  `json:"frame_id"`
	RootFrameID          string                                  `json:"rootFrameId"`
	RootFrameIDSnake     string                                  `json:"root_frame_id"`
	Request              string                                  `json:"request"`
	InputData            map[string]any                          `json:"inputData"`
	InputDataSnake       map[string]any                          `json:"input_data"`
	ClientMessageID      string                                  `json:"clientMessageId"`
	ClientMessageIDSnake string                                  `json:"client_message_id"`
	MessageUUID          string                                  `json:"messageUuid"`
	MessageUUIDSnake     string                                  `json:"message_uuid"`
	Model                string                                  `json:"model"`
	SubagentModel        string                                  `json:"subagentModel"`
	SubagentModelSnake   string                                  `json:"subagent_model"`
	Effort               string                                  `json:"effort"`
	Thinking             any                                     `json:"thinking"`
	TargetAgent          string                                  `json:"targetAgent"`
	TargetAgentSnake     string                                  `json:"target_agent"`
	PlanMode             any                                     `json:"planMode"`
	PlanModeSnake        any                                     `json:"plan_mode"`
	GoalText             string                                  `json:"goalText"`
	GoalTextSnake        string                                  `json:"goal_text"`
	AsRoutine            bool                                    `json:"asRoutine"`
	AsRoutineSnake       bool                                    `json:"as_routine"`
	GPUMode              any                                     `json:"gpuMode"`
	GPUModeSnake         any                                     `json:"gpu_mode"`
	UltraMode            any                                     `json:"ultraMode"`
	UltraModeSnake       any                                     `json:"ultra_mode"`
	VerifierMode         any                                     `json:"verifierMode"`
	VerifierModeSnake    any                                     `json:"verifier_mode"`
	MemoryMode           any                                     `json:"memoryMode"`
	MemoryModeSnake      any                                     `json:"memory_mode"`
	ViewportContext      any                                     `json:"viewportContext"`
	ViewportContextSnake any                                     `json:"viewport_context"`
	IntentID             string                                  `json:"intentId"`
	IntentIDSnake        string                                  `json:"intent_id"`
	OnboardingMode       string                                  `json:"onboardingMode"`
	OnboardingModeSnake  string                                  `json:"onboarding_mode"`
	ArtifactRefs         []webConversationArtifactReferenceInput `json:"artifact_refs"`
	SessionKnobs         map[string]any                          `json:"sessionKnobs"`
	SessionKnobsSnake    map[string]any                          `json:"session_knobs"`
}

func (s *Server) handleWorkspaceProjectRequest(w http.ResponseWriter, r *http.Request, store *workspace.Store, projectID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.sessionStore == nil || s.eventJournal == nil || s.sessionSockets == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "session runtime is not configured"})
		return
	}
	var input workspaceProjectRequestInput
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	requestText := projectRequestText(input)
	if requestText == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "project request text is required"})
		return
	}
	if _, found, err := store.GetProject(projectID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	} else if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "project not found"})
		return
	}
	clientMessageID := firstNonEmpty(input.ClientMessageID, input.ClientMessageIDSnake)
	if clientMessageID == "" {
		clientMessageID = uuid.NewString()
	}
	frameID := firstNonEmpty(input.FrameID, input.FrameIDSnake)
	if frameID == "" {
		frameID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(projectID+"\x00"+clientMessageID)).String()
	}
	messageUUID := firstNonEmpty(input.MessageUUID, input.MessageUUIDSnake, clientMessageID)
	agentName := firstNonEmpty(input.TargetAgent, input.TargetAgentSnake, "synon")
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	created := false
	if !found {
		frame, err = store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: projectID, AgentName: agentName,
			Status: "running", ConversationType: "task", Name: compactText(requestText, 120),
		})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		created = true
		event, eventErr := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: frame.ID, Type: "frame_created",
			Payload: map[string]any{"projectId": projectID, "agentName": agentName, "status": "running", "conversationType": "task"},
		})
		if eventErr != nil {
			_ = store.DeleteFrame(frame.ID)
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": eventErr.Error()})
			return
		}
		if err := s.publishWorkspaceEvent(event); err != nil {
			_ = store.DeleteFrame(frame.ID)
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "project request frame was created but its realtime event could not be persisted: " + err.Error()})
			return
		}
	} else if frame.ProjectID != projectID || frame.RootFrameID != frame.ID {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "project request frame id belongs to another conversation"})
		return
	}
	messageEvent, idempotent, err := s.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frame.ID, MessageUUID: messageUUID,
		ClientMessageID: clientMessageID, Text: requestText,
	})
	if err != nil {
		if created {
			_ = store.DeleteFrame(frame.ID)
			_ = s.eventJournal.Remove(frame.ID)
		}
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	config := projectRequestSessionConfig(input)
	session, _, err := s.sessionStore.Get(frame.ID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		orchestration = map[string]any{}
	}
	orchestration["sessionConfig"] = config
	session.Orchestration = orchestration
	if err := s.sessionStore.Save(session); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "frame": frame, "event": messageEvent,
		"sessionId": frame.ID, "idempotent": idempotent,
	})
}

func projectRequestSessionConfig(input workspaceProjectRequestInput) map[string]any {
	config := copyMapAny(input.SessionKnobs)
	if config == nil {
		config = copyMapAny(input.SessionKnobsSnake)
	}
	if config == nil {
		config = map[string]any{}
	}
	delete(config, "as_routine")
	delete(config, "asRoutine")
	delete(config, "onboarding_mode")
	delete(config, "onboardingMode")
	for key, value := range map[string]any{
		"model":           input.Model,
		"subagentModel":   firstNonEmpty(input.SubagentModel, input.SubagentModelSnake),
		"effort":          input.Effort,
		"thinking":        input.Thinking,
		"targetAgent":     firstNonEmpty(input.TargetAgent, input.TargetAgentSnake),
		"planMode":        firstNonNil(input.PlanMode, input.PlanModeSnake),
		"goalText":        firstNonEmpty(input.GoalText, input.GoalTextSnake),
		"asRoutine":       input.AsRoutine || input.AsRoutineSnake,
		"gpuMode":         firstNonNil(input.GPUMode, input.GPUModeSnake),
		"ultraMode":       firstNonNil(input.UltraMode, input.UltraModeSnake),
		"verifierMode":    firstNonNil(input.VerifierMode, input.VerifierModeSnake),
		"memoryMode":      firstNonNil(input.MemoryMode, input.MemoryModeSnake),
		"viewportContext": firstNonNil(input.ViewportContext, input.ViewportContextSnake),
		"intentId":        firstNonEmpty(input.IntentID, input.IntentIDSnake),
		"onboardingMode":  firstNonEmpty(input.OnboardingMode, input.OnboardingModeSnake),
	} {
		if value == nil || value == "" || value == false {
			continue
		}
		config[key] = value
	}
	return config
}

func projectRequestInputData(input workspaceProjectRequestInput) map[string]any {
	data := copyMapAny(input.InputData)
	if data == nil {
		data = copyMapAny(input.InputDataSnake)
	}
	if data == nil {
		data = map[string]any{}
	}
	return data
}

func projectRequestText(input workspaceProjectRequestInput) string {
	data := projectRequestInputData(input)
	request, _ := data["request"].(string)
	return strings.TrimSpace(firstNonEmpty(input.Request, request))
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (s *Server) handleWorkspaceProjectBenches(w http.ResponseWriter, r *http.Request, store *workspace.Store, projectID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	benches, err := store.ListBenches(projectID, workspaceListQuery(r, "limit", 100, 1), r.URL.Query().Get("q"))
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "benches": benches})
}

func (s *Server) handleWorkspaceBench(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	frameID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/go/benches/"))
	if frameID == "" || strings.Contains(frameID, "/") {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "bench not found"})
		return
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found || !workspaceProjectOwned(w, store, frame.ProjectID, userID) {
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "bench not found"})
		}
		return
	}
	if r.Method == http.MethodGet {
		bench, found, err := store.GetBench(frameID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "bench not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "bench": bench})
		return
	}
	var input struct {
		Name             *string `json:"name"`
		TaskSummary      *string `json:"taskSummary"`
		TaskSummarySnake *string `json:"task_summary"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	taskSummary := input.TaskSummary
	if taskSummary == nil {
		taskSummary = input.TaskSummarySnake
	}
	bench, event, err := store.UpdateBench(frameID, workspace.UpdateBenchInput{Name: input.Name, TaskSummary: taskSummary})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "bench updated but its realtime event could not be persisted: " + err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "bench": bench, "event": event})
}
