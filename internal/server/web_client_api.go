package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	webClientSettingsScope = "ui-preferences"
	maxWebClientSettings   = 256
	maxWebClientBodyBytes  = 256 * 1024
)

var webClientUIPreferenceKeys = map[string]struct{}{
	"language": {}, "theme": {}, "colorScheme": {}, "ui.zoomFactor": {},
	"ui.fontSize.chat": {}, "ui.fontSize.markdown": {}, "ui.fontSize.code": {},
	"window.bounds": {}, "webui.desktop.enabled": {}, "webui.desktop.allowRemote": {},
	"webui.desktop.port": {}, "customCss": {}, "css.themes": {}, "css.activeThemeId": {},
	"theme.activeId": {}, "theme.userThemes": {}, "workspace.pasteConfirm": {},
	"guid.lastAssistantId": {}, "upload.saveToWorkspace": {}, "system.closeToTray": {},
	"system.notificationEnabled": {}, "system.cronNotificationEnabled": {},
	"system.keepAwake": {}, "system.autoPreviewOfficeFiles": {}, "pet.enabled": {},
	"pet.size": {}, "pet.dnd": {}, "pet.confirmEnabled": {},
}

func (s *Server) handleWebClientSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "client settings storage is not configured"})
		return
	}
	publicUI := r.Method == http.MethodGet && r.URL.Query().Get("scope") == webClientSettingsScope
	userID, authenticated := s.webClientSettingsUser(r, publicUI)
	if !authenticated {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "authentication required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		requested := parseWebClientSettingKeys(r)
		settings, err := s.readWebClientSettings(userID, requested)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to read client settings"})
			return
		}
		if !publicUI && webClientSettingRequested(requested, webSpeechSettingKey) {
			value, found, readErr := s.readWebSpeechConfig(userID)
			if readErr != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to read protected speech settings"})
				return
			}
			if found {
				settings[webSpeechSettingKey] = value
			}
		}
		if publicUI {
			for key := range settings {
				if _, allowed := webClientUIPreferenceKeys[key]; !allowed {
					delete(settings, key)
				}
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, maxWebClientBodyBytes)
		decoder := json.NewDecoder(r.Body)
		var input map[string]any
		if err := decoder.Decode(&input); err != nil || len(input) > maxWebClientSettings {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "client settings body must be a bounded JSON object"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "client settings body must contain one JSON object"})
			return
		}
		var speechValue any
		speechPresent := false
		for key, value := range input {
			if !validWebClientSettingKey(key) {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid client setting key"})
				return
			}
			if key == webSpeechSettingKey {
				speechPresent = true
				speechValue = value
				if value != nil {
					if _, _, err := decodeWebSpeechConfigSetting(value); err != nil {
						writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid speech-to-text configuration"})
						return
					}
				}
			}
		}
		if speechPresent {
			if err := s.writeWebSpeechConfig(userID, speechValue); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to persist protected speech settings"})
				return
			}
		}
		prefix := webClientSettingsPrefix(userID)
		for key, value := range input {
			if key == webSpeechSettingKey {
				continue
			}
			var err error
			if value == nil {
				_, err = s.settingsStore.Delete(prefix + key)
			} else {
				_, err = s.settingsStore.Set(prefix+key, value)
			}
			if err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to persist client settings"})
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

func (s *Server) webClientSettingsUser(r *http.Request, allowPublicUI bool) (string, bool) {
	if user, ok := s.webUser(r); ok {
		return user.ID, true
	}
	if allowPublicUI && s.synonLinkAuth != nil && s.synonLinkAuth.enabled {
		return s.synonLinkAuth.user().ID, true
	}
	return "", false
}

func (s *Server) readWebClientSettings(userID string, requested []string) (map[string]any, error) {
	stored, err := s.settingsStore.List()
	if err != nil {
		return nil, err
	}
	prefix := webClientSettingsPrefix(userID)
	wanted := map[string]struct{}{}
	for _, key := range requested {
		wanted[key] = struct{}{}
	}
	result := map[string]any{}
	for key, setting := range stored {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		clientKey := strings.TrimPrefix(key, prefix)
		if clientKey == webSpeechSettingKey {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[clientKey]; !ok {
				continue
			}
		}
		result[clientKey] = setting.Value
	}
	return result, nil
}

func webClientSettingRequested(requested []string, key string) bool {
	if len(requested) == 0 {
		return true
	}
	for _, candidate := range requested {
		if candidate == key {
			return true
		}
	}
	return false
}

func parseWebClientSettingKeys(r *http.Request) []string {
	result := make([]string, 0)
	for _, raw := range r.URL.Query()["keys"] {
		for _, key := range strings.Split(raw, ",") {
			key = strings.TrimSpace(key)
			if validWebClientSettingKey(key) {
				result = append(result, key)
			}
		}
	}
	return result
}

func webClientSettingsPrefix(userID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID)))
	return "web.client." + hex.EncodeToString(digest[:16]) + "."
}

func validWebClientSettingKey(key string) bool {
	if key == "" || len(key) > 128 || strings.Contains(key, "..") {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handleWebConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.createWebConversation(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "authentication required"})
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "workspace storage is not configured"})
		return
	}
	listStarted := time.Now()
	limit := 100
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && parsed > 0 {
		limit = min(parsed, 1000)
	}
	var before *workspace.CompatibilityFrameSummaryCursor
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		parsed, err := parseWebConversationListCursor(rawCursor)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid conversation cursor"})
			return
		}
		before = &workspace.CompatibilityFrameSummaryCursor{UpdatedAt: parsed.UpdatedAt, FrameID: parsed.FrameID}
	}
	summaries, err := s.workspaceStore.ListVisibleRootCompatibilityFrameSummaries(r.Context(), userID, limit+1, before)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to list conversations"})
		return
	}
	hasMore := len(summaries) > limit
	if hasMore {
		summaries = summaries[:limit]
	}
	items := make([]map[string]any, 0, len(summaries))
	for _, summary := range summaries {
		conversation, err := s.webConversationSnapshot(summary.Frame, summary.ProjectName)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		items = append(items, conversation)
	}
	var nextCursor any
	if hasMore && len(summaries) > 0 {
		last := summaries[len(summaries)-1].Frame
		nextCursor = encodeWebConversationListCursor(webConversationListCursor{
			Version: 1, UpdatedAt: last.UpdatedAt.UTC(), FrameID: last.ID,
		})
	}
	payload := map[string]any{
		"items": items, "total": len(items), "has_more": hasMore, "next_cursor": nextCursor,
	}
	writePrivateRevalidatedWorkspaceJSON(
		withWebServerTiming(w, "conversation_list", listStarted), r, userID, payload,
	)
}

type webConversationListCursor struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	FrameID   string    `json:"frame_id"`
}

func encodeWebConversationListCursor(cursor webConversationListCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseWebConversationListCursor(value string) (webConversationListCursor, error) {
	if len(value) == 0 || len(value) > 1024 {
		return webConversationListCursor{}, errors.New("invalid conversation cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) == 0 || len(raw) > 512 {
		return webConversationListCursor{}, errors.New("invalid conversation cursor")
	}
	var cursor webConversationListCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.UpdatedAt.IsZero() ||
		strings.TrimSpace(cursor.FrameID) == "" || cursor.FrameID != strings.TrimSpace(cursor.FrameID) || len(cursor.FrameID) > 255 {
		return webConversationListCursor{}, errors.New("invalid conversation cursor")
	}
	return cursor, nil
}

func webConversation(frame workspace.CompatibilityFrame, projectName string) map[string]any {
	agentName := strings.TrimSpace(frame.DelegateName)
	if agentName == "" {
		agentName = strings.TrimSpace(frame.AgentName)
	}
	if agentName == "" {
		agentName = "SYNON_BIOMED"
	}
	if projectName == "" {
		projectName = frame.ProjectID
	}
	status := webConversationRuntime(frame)["task_status"]
	return map[string]any{
		"id": frame.ID, "type": "acp", "name": frame.Name, "desc": frame.TaskSummary,
		"source": "synonbiomed", "created_at": frame.CreatedAt.UnixMilli(), "modified_at": frame.UpdatedAt.UnixMilli(),
		"status":    status,
		"runtime":   webConversationRuntime(frame),
		"assistant": map[string]any{"id": webAssistantID(agentName), "source": "builtin", "name": agentName, "avatar": "", "backend": "synonbiomed"},
		"extra":     map[string]any{"backend": "synonbiomed", "agent_name": agentName, "project_id": frame.ProjectID, "project_name": projectName, "root_frame_id": frame.RootFrameID, "parent_frame_id": frame.ParentFrameID, "conversation_type": frame.ConversationType, "frame_status": frame.Status, "status_description": frame.StatusDescription, "workspace": "synonbiomed://" + frame.ProjectID, "custom_workspace": false},
	}
}

func (s *Server) webConversationSnapshot(frame workspace.CompatibilityFrame, projectName string) (map[string]any, error) {
	conversation := webConversation(frame, projectName)
	runtime, err := s.webConversationRuntimeSnapshot(frame)
	if err != nil {
		return nil, err
	}
	conversation["runtime"] = runtime
	conversation["status"] = runtime["task_status"]
	return conversation, nil
}

func (s *Server) handleSynonBiomedExpertProfiles(w http.ResponseWriter, r *http.Request) {
	const publicPrefix = "/api/synonbiomed/expert-profiles"
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	clone.URL = &urlCopy
	suffix := strings.TrimPrefix(r.URL.Path, publicPrefix)
	if suffix == "" {
		clone.URL.Path = "/api/agents"
		s.handleAgentCompatibility(w, clone)
		return
	}
	clone.URL.Path = "/api/agents" + suffix
	s.handleAgentProfileCompatibility(w, clone)
}
