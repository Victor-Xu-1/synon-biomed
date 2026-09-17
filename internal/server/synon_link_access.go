package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) handleSynonLinkAccessRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		userID := resolveUserID(r, nil)
		if userID == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "userId is required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"requests": s.synonLink.ListAccessRequests(userID, r.URL.Query().Get("status")),
		})
	case http.MethodPost:
		var body struct {
			UserID   string `json:"userId"`
			ClientID string `json:"clientId"`
			Scope    string `json:"scope"`
			Reason   string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
			return
		}
		userID := resolveUserID(r, map[string]any{"userId": body.UserID})
		request, err := s.synonLink.RequestAccess(userID, body.ClientID, body.Scope, body.Reason)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "request": request})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link access requests support GET and POST")
	}
}

func (s *Server) handleSynonLinkAccessRequest(w http.ResponseWriter, r *http.Request, tail string) {
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 2 && parts[1] == "decision" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link access decision only supports POST")
			return
		}
		var body struct {
			UserID   string `json:"userId"`
			Approved bool   `json:"approved"`
			Reason   string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
			return
		}
		userID := resolveUserID(r, map[string]any{"userId": body.UserID})
		request, err := s.synonLink.DecideAccess(userID, parts[0], body.Approved, body.Reason)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "request": request})
		return
	}

	if len(parts) == 1 && parts[0] != "" {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Synon Link access revoke only supports DELETE")
			return
		}
		userID := resolveUserID(r, nil)
		request, err := s.synonLink.RevokeAccess(userID, parts[0], r.URL.Query().Get("reason"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "request": request})
		return
	}

	writeError(w, http.StatusNotFound, "NOT_FOUND", "Unknown Synon Link access route")
}
