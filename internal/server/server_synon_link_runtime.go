package server

import (
	"encoding/json"
	"errors"

	"net/http"

	"os"

	"path/filepath"

	"strconv"
	"strings"

	"synon-go/internal/synonlink"
)

func (s *Server) handleSynonLink(w http.ResponseWriter, r *http.Request) {
	tail := r.URL.Path[len("/api/synon-link"):]
	if tail == "" || tail == "/" || tail == "/clients" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link clients only supports GET")
			return
		}
		userID := resolveUserID(r, nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"clients": s.synonLink.ListClients(userID),
		})
		return
	}

	if tail == "/capabilities" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link capabilities only supports GET")
			return
		}
		defaults := s.synonLink.PolicyDefaults()
		writeJSON(w, http.StatusOK, map[string]any{
			"protocolVersion": "1",
			"policyVersion":   "2",
			"policyDefaults":  defaults,
			"actions":         synonlink.ActionProfilesWithDefaults(defaults),
		})
		return
	}

	if tail == "/doctor" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link doctor only supports GET")
			return
		}
		s.handleSynonLinkDoctor(w, r)
		return
	}

	if tail == "/inspect" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link inspect only supports GET")
			return
		}
		s.handleSynonLinkInspect(w, r)
		return
	}

	if tail == "/running" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link running only supports GET")
			return
		}
		userID := resolveUserID(r, nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"running": s.synonLink.ListRunning(userID),
		})
		return
	}

	if tail == "/logs" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link logs only supports GET")
			return
		}
		userID := resolveUserID(r, nil)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		writeJSON(w, http.StatusOK, map[string]any{
			"logs": s.synonLink.ListTaskLogs(userID, limit),
		})
		return
	}

	if tail == "/access-requests" {
		s.handleSynonLinkAccessRequests(w, r)
		return
	}

	if strings.HasPrefix(tail, "/access-requests/") {
		s.handleSynonLinkAccessRequest(w, r, strings.TrimPrefix(tail, "/access-requests/"))
		return
	}

	if strings.HasPrefix(tail, "/devices/") {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link device revoke only supports DELETE")
			return
		}
		userID := resolveUserID(r, nil)
		deviceID := strings.TrimPrefix(tail, "/devices/")
		if userID == "" || deviceID == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "userId and deviceId are required")
			return
		}
		if !s.synonLink.RevokeDevice(userID, deviceID, "api revoked") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Synon Link device not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deviceId": deviceID})
		return
	}

	if tail == "/ws" {
		s.handleSynonLinkWebSocket(w, r)
		return
	}

	if tail == "/commands" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link commands only supports POST")
			return
		}
		s.handleSynonLinkCommand(w, r)
		return
	}

	writeError(w, http.StatusNotFound, "NOT_FOUND", "Unknown Synon Link route")
}

func (s *Server) handleSynonLinkInspect(w http.ResponseWriter, r *http.Request) {
	userID := resolveUserID(r, nil)
	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	if userID == "" || clientID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "userId and clientId are required")
		return
	}
	var client synonlink.Client
	found := false
	for _, candidate := range s.synonLink.ListClients(userID) {
		if candidate.ID == clientID {
			client = candidate
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Synon Link client not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	defaults := s.synonLink.PolicyDefaults()
	running := filterRunningByClient(s.synonLink.ListRunning(userID), clientID)
	logs := filterTaskLogsByClient(s.synonLink.ListTaskLogs(userID, limit), clientID, limit)
	accessRequests := filterAccessRequestsByClient(s.synonLink.ListAccessRequests(userID, ""), clientID)
	supported, blocked := inspectClientActions(client, defaults)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"client":         client,
		"running":        running,
		"logs":           logs,
		"accessRequests": accessRequests,
		"policyDefaults": defaults,
		"actions": map[string]any{
			"supported": supported,
			"blocked":   blocked,
		},
		"summary": map[string]any{
			"running":        len(running),
			"logs":           len(logs),
			"accessRequests": len(accessRequests),
			"supported":      len(supported),
			"blocked":        len(blocked),
		},
	})
}

func (s *Server) handleSynonLinkDoctor(w http.ResponseWriter, r *http.Request) {
	userID := resolveUserID(r, nil)
	clients := s.synonLink.ListClients(userID)
	defaults := s.synonLink.PolicyDefaults()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"report": map[string]any{
			"server": map[string]any{
				"protocolVersion": "1",
				"policyVersion":   "1",
				"apiAuth":         "userId",
			},
			"clients":        clients,
			"running":        s.synonLink.ListRunning(userID),
			"logs":           s.synonLink.ListTaskLogs(userID, 50),
			"warnings":       synonLinkWarnings(clients),
			"policyDefaults": defaults,
			"rememberedApprovals": map[string]any{
				"count": s.rememberedApprovalDecisionCount(),
				"key":   approvalRememberedSettingKey,
			},
			"policyMatrix": synonlink.PolicyMatrixWithDefaults(defaults),
		},
	})
}

func (s *Server) handleSynonLinkCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   string         `json:"userId"`
		ClientID string         `json:"clientId"`
		Name     string         `json:"name"`
		Payload  map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	userID := resolveUserID(r, map[string]any{"userId": body.UserID})
	if userID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "userId is required")
		return
	}
	if body.ClientID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "clientId is required")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "name is required")
		return
	}

	command, resultCh, err := s.synonLink.SendCommand(r.Context(), userID, body.ClientID, body.Name, body.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	select {
	case result := <-resultCh:
		if !result.OK {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", result.Error)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"commandId": command.ID,
			"result":    result.Value,
		})
	case <-r.Context().Done():
		writeError(w, http.StatusRequestTimeout, "REQUEST_TIMEOUT", "Synon Link command request was cancelled")
	}
}

func filterRunningByClient(items []synonlink.RunningTask, clientID string) []synonlink.RunningTask {
	filtered := make([]synonlink.RunningTask, 0)
	for _, item := range items {
		if item.ClientID == clientID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterTaskLogsByClient(items []synonlink.TaskLog, clientID string, limit int) []synonlink.TaskLog {
	filtered := make([]synonlink.TaskLog, 0)
	for _, item := range items {
		if item.ClientID != clientID {
			continue
		}
		filtered = append(filtered, item)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func filterAccessRequestsByClient(items []synonlink.AccessRequest, clientID string) []synonlink.AccessRequest {
	filtered := make([]synonlink.AccessRequest, 0)
	for _, item := range items {
		if item.ClientID == clientID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func inspectClientActions(client synonlink.Client, defaults synonlink.PolicyDefaults) ([]map[string]any, []map[string]any) {
	supported := make([]map[string]any, 0)
	blocked := make([]map[string]any, 0)
	for _, profile := range synonlink.ActionProfilesWithDefaults(defaults) {
		if client.Kind != "" && profile.ClientKind != "" && client.Kind != profile.ClientKind {
			continue
		}
		reasons := actionBlockReasons(client, profile)
		record := map[string]any{
			"action":                  profile.Action,
			"family":                  profile.Family,
			"readOnly":                profile.ReadOnly,
			"approvalPolicy":          profile.ApprovalPolicy,
			"effectiveApprovalPolicy": profile.EffectiveApprovalPolicy,
			"capabilities":            profile.Capabilities,
		}
		if len(reasons) == 0 {
			supported = append(supported, record)
		} else {
			record["reasons"] = reasons
			blocked = append(blocked, record)
		}
	}
	return supported, blocked
}

func actionBlockReasons(client synonlink.Client, profile synonlink.ActionProfile) []string {
	reasons := make([]string, 0)
	if len(client.SupportedActions) > 0 && !stringSliceContains(client.SupportedActions, profile.Action) {
		reasons = append(reasons, "not advertised in supportedActions")
	}
	for _, capability := range profile.Capabilities {
		if !stringSliceContains(client.Capabilities, capability) {
			reasons = append(reasons, "missing capability "+capability)
		}
	}
	return reasons
}

func (s *Server) handleSynonLinkDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link download only supports GET and HEAD")
		return
	}
	if s.linkPackagePath == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Synon Link extension package is not configured")
		return
	}
	info, err := os.Stat(s.linkPackagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Synon Link extension package is not available")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Synon Link extension package could not be read")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Synon Link extension package is not available")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="synon-link-extension.zip"`)
	w.Header().Set("Content-Length", stringInt(info.Size()))
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.ServeFile(w, r, filepath.Clean(s.linkPackagePath))
}
