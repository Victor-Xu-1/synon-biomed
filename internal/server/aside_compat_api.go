package server

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

var compatibilityIntentUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type compatibilityAsideRequest struct {
	Request   string  `json:"request"`
	Model     *string `json:"model"`
	IntentID  *string `json:"intent_id"`
	AsSession bool    `json:"as_session"`
}

type compatibilityAsideError struct {
	Status int
	Detail string
	Cause  error
}

func (e *compatibilityAsideError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Detail + ": " + e.Cause.Error()
	}
	return e.Detail
}

func (e *compatibilityAsideError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newCompatibilityAsideError(status int, detail string, cause error) error {
	return &compatibilityAsideError{Status: status, Detail: detail, Cause: cause}
}

func (s *Server) handleCompatibilityCreateAside(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityAsideRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid aside request: "+err.Error())
		return
	}
	result, err := s.createCompatibilityAside(r, frame, input)
	if err != nil {
		var requestErr *compatibilityAsideError
		if errors.As(err, &requestErr) {
			writeV11Detail(w, requestErr.Status, requestErr.Detail)
			return
		}
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"frame_id": result.Frame.ID, "prefix_len": result.PrefixLen,
	})
}

func (s *Server) createCompatibilityAside(
	r *http.Request,
	frame workspace.CompatibilityFrame,
	input compatibilityAsideRequest,
) (workspace.CompatibilityAsideResult, error) {
	if s.sessionStore == nil || s.eventJournal == nil || s.sessionSockets == nil {
		return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(
			http.StatusServiceUnavailable, "Session runtime is not configured", nil,
		)
	}
	if input.Request == "" {
		return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(
			http.StatusBadRequest, "request must contain at least 1 character(s)", nil,
		)
	}
	if frame.ParentFrameID != "" {
		detail := "Aside parent must be a root frame"
		if input.AsSession {
			detail = "Fork source must be a root frame"
		}
		return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(http.StatusBadRequest, detail, nil)
	}
	agentName := frame.AgentName
	found, err := s.validateCompatibilityTargetAgent(r, &agentName)
	if err != nil {
		return workspace.CompatibilityAsideResult{}, err
	}
	if !found {
		return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(
			http.StatusNotFound, "Agent "+frame.AgentName+" not found in registry", nil,
		)
	}
	intentID := ""
	if input.IntentID != nil {
		intentID = strings.ToLower(strings.TrimSpace(*input.IntentID))
		if !compatibilityIntentUUIDPattern.MatchString(intentID) {
			return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(
				http.StatusBadRequest,
				"intent_id must be a UUID (8-4-4-4-12 hex); mint one per user send",
				nil,
			)
		}
	}

	s.compatRequestMu.Lock()
	messageID := intentID
	if messageID == "" {
		messageID = uuid.NewString()
	}
	asideInput := workspace.CompatibilityAsideInput{
		ID: uuid.NewString(), ParentRootFrameID: frame.ID,
		Request: input.Request, Model: input.Model, IntentID: intentID,
		AsSession: input.AsSession,
	}
	var result workspace.CompatibilityAsideResult
	if s.transcriptStore != nil {
		result, err = s.workspaceStore.CreateCompatibilityAsideWithTranscript(
			r.Context(), asideInput, messageID, []string{transcriptWebDestination},
		)
	} else {
		result, err = s.workspaceStore.CreateCompatibilityAside(asideInput)
	}
	s.compatRequestMu.Unlock()
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityIntentUsed) {
			return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(
				http.StatusConflict, "intent_id "+intentID+" was already used \u2014 ids are minted once per send", err,
			)
		}
		if errors.Is(err, workspace.ErrCompatibilityAsideParentNotRoot) {
			detail := "Aside parent must be a root frame"
			if input.AsSession {
				detail = "Fork source must be a root frame"
			}
			return workspace.CompatibilityAsideResult{}, newCompatibilityAsideError(http.StatusBadRequest, detail, err)
		}
		return workspace.CompatibilityAsideResult{}, err
	}
	cleanupLegacy := func() {
		if s.transcriptStore != nil {
			return
		}
		_ = s.workspaceStore.DeleteFrame(result.Frame.ID)
		_, _ = s.sessionStore.Delete(result.Frame.ID)
		_ = s.eventJournal.Remove(result.Frame.ID)
	}
	if s.transcriptStore == nil {
		if err := s.prepareCompatibilityAsideSession(result); err != nil {
			cleanupLegacy()
			return workspace.CompatibilityAsideResult{}, fmt.Errorf("aside persisted but runtime preparation failed: %w", err)
		}
	}
	if err := s.publishWorkspaceEvent(result.Event); err != nil {
		cleanupLegacy()
		return workspace.CompatibilityAsideResult{}, fmt.Errorf("aside persisted but realtime delivery failed: %w", err)
	}
	if s.transcriptStore == nil {
		if _, _, err := s.submitFrameMessage(s.workspaceStore, frameMessageSubmission{
			FrameID: result.Frame.ID, MessageUUID: messageID,
			ClientMessageID: messageID, Text: input.Request,
		}); err != nil {
			cleanupLegacy()
			return workspace.CompatibilityAsideResult{}, fmt.Errorf("aside runtime dispatch failed: %w", err)
		}
	}
	return result, nil
}

func (s *Server) prepareCompatibilityAsideSession(result workspace.CompatibilityAsideResult) error {
	s.sessionSubmissionMu.Lock()
	defer s.sessionSubmissionMu.Unlock()
	journalMessages := make([]eventjournal.Message, 0, len(result.SeedMessages))
	for _, source := range result.SeedMessages {
		message := eventjournal.Message{}
		for key, value := range source {
			message[key] = value
		}
		role := strings.TrimSpace(stringValue(message["role"]))
		if role == "" {
			role = "user"
			message["role"] = role
		}
		if _, found := message["type"]; !found {
			message["type"] = role + "_message"
		}
		if _, found := message["text"]; !found {
			message["text"] = runnerMessageText(message)
		}
		journalMessages = append(journalMessages, message)
	}
	entries, err := s.eventJournal.ReplaceSession(result.Frame.ID, journalMessages)
	if err != nil {
		return err
	}
	project, found, err := s.workspaceStore.GetProject(result.Frame.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("aside project %q not found", result.Frame.ProjectID)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: result.Frame.ID, Title: result.Frame.Name, WorkDir: s.fileRoot,
		CreatedAt: result.Frame.CreatedAt, UpdatedAt: now,
		Project: &sessionstore.Project{
			ID: project.ID, Name: project.Name, Path: project.Path, BoundAt: now,
		},
	}
	session = sessionWithJournalStats(session, entries)
	session.Runner = nil
	config := map[string]any{"agentName": result.Frame.AgentName}
	if result.Model != nil {
		config["model"] = result.Model
	}
	if result.Effort != nil {
		config["effort"] = result.Effort
	}
	for _, key := range []string{
		"ultra_mode", "verifier_mode", "memory_mode", "auto_mode", "reviewer_model",
		"rc_context_ceiling", "python_version", "kernel_idle_timeout", "async_local_exec_wallclock_cap_s", "gpu_mode",
	} {
		if value, found := result.InputData[key]; found {
			config[key] = value
		}
	}
	session.Orchestration = map[string]any{
		"sessionConfig": config,
		"aside": map[string]any{
			"prefixLen": result.PrefixLen,
			"asSession": result.InputData["_aside_parent"] == nil,
		},
	}
	return s.sessionStore.Save(session)
}
