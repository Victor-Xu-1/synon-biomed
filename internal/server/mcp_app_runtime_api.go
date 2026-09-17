package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	mcpAppBridgeBodyLimit = 3 << 20
	mcpAppPinContentLimit = 2 << 20
)

type mcpAppToolCallRequest struct {
	RootFrameID string         `json:"root_frame_id"`
	RequestID   string         `json:"request_id"`
	MountID     string         `json:"mount_id"`
	Name        string         `json:"name"`
	Arguments   map[string]any `json:"arguments"`
}

type mcpAppPinRequest struct {
	RootFrameID     string         `json:"root_frame_id"`
	FrameID         string         `json:"frame_id"`
	ArtifactID      string         `json:"artifact_id"`
	Filename        string         `json:"filename"`
	ContentType     string         `json:"content_type"`
	Content         string         `json:"content"`
	ContentEncoding string         `json:"content_encoding"`
	AgentName       string         `json:"agent_name"`
	Tool            string         `json:"tool"`
	Arguments       map[string]any `json:"arguments"`
}

type mcpAppRegistrationRequest struct {
	RootFrameID string                 `json:"root_frame_id"`
	FrameID     string                 `json:"frame_id"`
	Server      string                 `json:"server"`
	ArtifactID  string                 `json:"artifact_id"`
	Tools       []mcpAppToolDescriptor `json:"tools"`
}

type mcpAppResultRequest struct {
	RegistrationID    string          `json:"registration_id"`
	RequestID         string          `json:"request_id"`
	Content           []mcpAppContent `json:"content"`
	StructuredContent any             `json:"structured_content"`
	IsError           bool            `json:"is_error"`
}

func (s *Server) handleMCPAppRegistrations(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.mcpApps == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app broker is unavailable")
		return
	}
	if r.Method == http.MethodDelete {
		registrationID := strings.TrimSpace(r.URL.Query().Get("registration_id"))
		if registrationID == "" {
			writeV11Detail(w, http.StatusBadRequest, "registration_id is required")
			return
		}
		if err := s.mcpApps.Unregister(compatAgentUserID(r), registrationID); err != nil {
			writeMCPAppBrokerError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body mcpAppRegistrationRequest
	if !decodeMCPAppBridgeBody(w, r, &body) {
		return
	}
	body.RootFrameID = strings.TrimSpace(body.RootFrameID)
	body.FrameID = strings.TrimSpace(body.FrameID)
	body.ArtifactID = strings.TrimSpace(body.ArtifactID)
	body.Server = strings.TrimSpace(body.Server)
	body.ArtifactID = strings.TrimSpace(body.ArtifactID)
	if body.FrameID == "" {
		body.FrameID = body.RootFrameID
	}
	rootAccess, ok := s.mcpAppFrameAccess(w, r, body.RootFrameID)
	if !ok {
		return
	}
	frameAccess, ok := s.mcpAppFrameAccess(w, r, body.FrameID)
	if !ok {
		return
	}
	if frameAccess.Frame.ProjectID != rootAccess.Frame.ProjectID || frameAccess.Frame.RootFrameID != rootAccess.Frame.RootFrameID {
		writeV11Detail(w, http.StatusBadRequest, "frame_id must belong to root_frame_id")
		return
	}
	connector, runtimeContext, found, err := s.workspaceMCPRuntimeTargetWithContext(r.Context(), body.RootFrameID, body.Server)
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to resolve MCP app connector")
		return
	}
	if !found || !connector.Enabled {
		writeV11Detail(w, http.StatusNotFound, "MCP app connector is not attached and enabled for this Frame")
		return
	}
	if runtimeContext.UserID != rootAccess.UserID {
		writeV11Detail(w, http.StatusForbidden, "MCP app connector owner does not match the Frame owner")
		return
	}
	registration, err := s.mcpApps.Register(mcpAppRegistrationInput{
		UserID: rootAccess.UserID, ProjectID: rootAccess.Frame.ProjectID,
		RootFrameID: rootAccess.Frame.RootFrameID, FrameID: frameAccess.Frame.ID,
		ServerID: connector.ID, ServerName: connector.Name, ServerSource: connector.Source,
		ArtifactID: body.ArtifactID, Tools: body.Tools,
	})
	if err != nil {
		writeMCPAppBrokerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registration)
}

func (s *Server) handleMCPAppRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.mcpApps == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app broker is unavailable")
		return
	}
	registrationID := strings.TrimSpace(r.URL.Query().Get("registration_id"))
	if registrationID == "" {
		writeV11Detail(w, http.StatusBadRequest, "registration_id is required")
		return
	}
	pollContext, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	request, found, err := s.mcpApps.Poll(pollContext, compatAgentUserID(r), registrationID)
	if errors.Is(err, context.DeadlineExceeded) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeMCPAppBrokerError(w, err)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) handleMCPAppResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.mcpApps == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app broker is unavailable")
		return
	}
	var body mcpAppResultRequest
	if !decodeMCPAppBridgeBody(w, r, &body) {
		return
	}
	body.RegistrationID = strings.TrimSpace(body.RegistrationID)
	body.RequestID = strings.TrimSpace(body.RequestID)
	if body.RegistrationID == "" || body.RequestID == "" {
		writeV11Detail(w, http.StatusBadRequest, "registration_id and request_id are required")
		return
	}
	if err := validateMCPAppResult(body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.mcpApps.Resolve(compatAgentUserID(r), body.RegistrationID, body.RequestID, mcpAppCallResult{
		Content: body.Content, StructuredContent: body.StructuredContent, IsError: body.IsError,
	}); err != nil {
		writeMCPAppBrokerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMCPAppToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body mcpAppToolCallRequest
	if !decodeMCPAppBridgeBody(w, r, &body) {
		return
	}
	body.RootFrameID = strings.TrimSpace(body.RootFrameID)
	body.RequestID = strings.TrimSpace(body.RequestID)
	body.MountID = strings.TrimSpace(body.MountID)
	body.Name = strings.TrimSpace(body.Name)
	if body.RootFrameID == "" || body.RequestID == "" || body.MountID == "" || body.Name == "" {
		writeV11Detail(w, http.StatusBadRequest, "root_frame_id, request_id, mount_id, and name are required")
		return
	}
	access, ok := s.mcpAppFrameAccess(w, r, body.RootFrameID)
	if !ok {
		return
	}
	payload := map[string]any{
		"type": "mcp_app_tool_call", "request_id": body.RequestID,
		"mount_id": body.MountID, "name": body.Name, "arguments": body.Arguments,
	}
	event, err := s.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "mcp-app-tool-call:" + body.RequestID, UserID: access.UserID,
		ProjectID: access.Frame.ProjectID, RootFrameID: access.Frame.RootFrameID,
		FrameID: access.Frame.ID, Type: "mcp_app_tool_call", Payload: payload,
	})
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to publish MCP app tool call")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"request_id": body.RequestID, "sequence": event.Sequence,
	})
}

func (s *Server) handleMCPAppPin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body mcpAppPinRequest
	if !decodeMCPAppBridgeBody(w, r, &body) {
		return
	}
	body.RootFrameID = strings.TrimSpace(body.RootFrameID)
	body.FrameID = strings.TrimSpace(body.FrameID)
	body.Filename = strings.TrimSpace(body.Filename)
	body.ContentType = strings.TrimSpace(body.ContentType)
	body.AgentName = strings.TrimSpace(body.AgentName)
	body.Tool = strings.TrimSpace(body.Tool)
	if body.FrameID == "" {
		body.FrameID = body.RootFrameID
	}
	if body.RootFrameID == "" || body.Filename == "" || body.ContentType == "" {
		writeV11Detail(w, http.StatusBadRequest, "root_frame_id, filename, and content_type are required")
		return
	}
	rootAccess, ok := s.mcpAppFrameAccess(w, r, body.RootFrameID)
	if !ok {
		return
	}
	frameAccess, ok := s.mcpAppFrameAccess(w, r, body.FrameID)
	if !ok {
		return
	}
	if frameAccess.Frame.ProjectID != rootAccess.Frame.ProjectID || frameAccess.Frame.RootFrameID != rootAccess.Frame.RootFrameID {
		writeV11Detail(w, http.StatusBadRequest, "frame_id must belong to root_frame_id")
		return
	}
	content, err := decodeMCPAppPinContent(body.Content, body.ContentEncoding)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	mutationContext, generatedArtifactID := mcpAppPinMutation(r, rootAccess.UserID, rootAccess.Frame.RootFrameID, body.Filename)
	artifactID := body.ArtifactID
	if artifactID == "" {
		artifactID = generatedArtifactID
	} else {
		existing, found, lookupErr := s.workspaceStore.GetArtifact(artifactID)
		if lookupErr != nil {
			writeV11Detail(w, http.StatusInternalServerError, "failed to inspect MCP app artifact")
			return
		}
		if !found || existing.ProjectID != rootAccess.Frame.ProjectID {
			writeV11Detail(w, http.StatusNotFound, "MCP app artifact not found")
			return
		}
	}
	interactions := []map[string]any{{
		"kind": "app_tool", "frame_id": rootAccess.Frame.RootFrameID,
		"mount_id": artifactID, "tool": body.Tool, "args": body.Arguments,
	}}
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(
		mutationContext,
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: rootAccess.Frame.ProjectID,
			Name: body.Filename, ContentType: body.ContentType,
			Content: bytes.NewReader(content), MaxBytes: mcpAppPinContentLimit,
			CreatedBy: body.AgentName, Interactions: interactions,
			RootFrameID: rootAccess.Frame.RootFrameID, FrameID: frameAccess.Frame.ID,
		},
		rootAccess.UserID,
	)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := map[string]any{
		"type": "mcp_app_pin", "project_id": artifact.ProjectID,
		"artifact_id": artifact.ID, "version_id": version.ID,
	}
	if _, err := s.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "mcp-app-pin:" + version.ID, UserID: rootAccess.UserID,
		ProjectID: artifact.ProjectID, RootFrameID: rootAccess.Frame.RootFrameID,
		FrameID: frameAccess.Frame.ID, Type: "mcp_app_pin", Payload: payload,
	}); err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "artifact was saved but MCP app pin publication failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "filename": artifact.Name,
	})
}

func (s *Server) mcpAppFrameAccess(w http.ResponseWriter, r *http.Request, frameID string) (workspace.KernelFrameAccess, bool) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "workspace store is unavailable")
		return workspace.KernelFrameAccess{}, false
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccess(strings.TrimSpace(frameID))
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to inspect frame")
		return workspace.KernelFrameAccess{}, false
	}
	if !found || access.UserID != compatAgentUserID(r) {
		writeV11Detail(w, http.StatusNotFound, "Frame "+strings.TrimSpace(frameID)+" not found")
		return workspace.KernelFrameAccess{}, false
	}
	return access, true
}

func decodeMCPAppBridgeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, mcpAppBridgeBodyLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeV11Detail(w, http.StatusBadRequest, "request body must contain one JSON object")
		return false
	}
	return true
}

func decodeMCPAppPinContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf8", "utf-8":
		if len(content) > mcpAppPinContentLimit {
			return nil, errors.New("MCP app pin content exceeds 2 MiB")
		}
		return []byte(content), nil
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, errors.New("content is not valid base64")
		}
		if len(decoded) > mcpAppPinContentLimit {
			return nil, errors.New("MCP app pin content exceeds 2 MiB")
		}
		return decoded, nil
	default:
		return nil, errors.New("content_encoding must be utf8 or base64")
	}
}

func mcpAppPinMutation(r *http.Request, userID, rootFrameID, filename string) (context.Context, string) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-Synon-Idempotency-Key"))
	}
	if key == "" {
		key = uuid.NewString()
	}
	identity := strings.Join(
		[]string{"synon-mcp-app-pin-v1", userID, rootFrameID, filename, key},
		"\x00",
	)
	return workspace.WithMutationIdempotencyKey(r.Context(), key), uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
}

func validateMCPAppResult(body mcpAppResultRequest) error {
	if len(body.Content) > 128 {
		return errors.New("MCP app result contains too many content items")
	}
	totalTextBytes := 0
	for _, item := range body.Content {
		if item.Type != "text" {
			return errors.New("MCP app result content type must be text")
		}
		totalTextBytes += len([]byte(item.Text))
		if totalTextBytes > 1<<20 {
			return errors.New("MCP app result text exceeds 1 MiB")
		}
	}
	return nil
}

func writeMCPAppBrokerError(w http.ResponseWriter, err error) {
	if errors.Is(err, errMCPAppBrokerClosed) {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app broker is unavailable")
		return
	}
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "not found") || strings.Contains(message, "no longer active") {
		writeV11Detail(w, http.StatusNotFound, message)
		return
	}
	writeV11Detail(w, http.StatusBadRequest, message)
}
