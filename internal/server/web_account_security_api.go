package server

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

func (s *Server) handleWebAccountSecurity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, ok := s.webUser(r)
	if !ok {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "message": "authentication required",
		})
		return
	}
	currentToken := webSessionCookieToken(r)
	if currentToken == "" || s.webSessions == nil {
		if r.URL.Path == "/api/account/security" && r.Method == http.MethodGet &&
			!s.webAuthenticationEnabled() && isLoopbackRequest(r) {
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{
				"success": true, "managed": false, "loginMethods": []string{"local"},
				"sessions": []webSessionView{}, "events": []storedWebSecurityEvent{},
			})
			return
		}
		writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{
			"success": false, "message": "account security requires a Web session",
		})
		return
	}
	if r.URL.Path == "/api/account/security" {
		if r.Method != http.MethodGet {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET is required"})
			return
		}
		s.handleWebAccountSecurityOverview(w, r, user, currentToken)
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "POST is required"})
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/account/security/")
	switch tail {
	case "sessions/revoke-others":
		count, err := s.webSessions.RevokeOthers(user.ID, currentToken)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to revoke sessions"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "revoked": count})
	case "sessions/revoke-all":
		count, err := s.webSessions.RevokeAll(user.ID, currentToken)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to revoke sessions"})
			return
		}
		s.clearWebAuthCookies(w, r)
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "revoked": count, "signedOut": true})
	default:
		const prefix = "sessions/"
		const suffix = "/revoke"
		if !strings.HasPrefix(tail, prefix) || !strings.HasSuffix(tail, suffix) {
			http.NotFound(w, r)
			return
		}
		sessionID := strings.TrimSuffix(strings.TrimPrefix(tail, prefix), suffix)
		if sessionID == "" || strings.Contains(sessionID, "/") {
			http.NotFound(w, r)
			return
		}
		revoked, current, err := s.webSessions.RevokeDevice(user.ID, sessionID, currentToken)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to revoke device"})
			return
		}
		if revoked == 0 {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "device not found"})
			return
		}
		if current {
			s.clearWebAuthCookies(w, r)
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"success": true, "revoked": revoked, "signedOut": current,
		})
	}
}

func (s *Server) handleWebAccountSecurityOverview(
	w http.ResponseWriter,
	r *http.Request,
	user synonLinkAuthUser,
	currentToken string,
) {
	metadata, deviceToken, err := s.webSessions.metadataForSessionCreation(r)
	if err != nil || s.webSessions.BindCurrentDevice(user.ID, currentToken, metadata) != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to load devices"})
		return
	}
	s.writeWebDeviceCookie(w, r, deviceToken)
	sessions, err := s.webSessions.List(user.ID, currentToken)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to load sessions"})
		return
	}
	methods := []string{}
	if s.webAccounts != nil {
		methods = s.webAccounts.LoginMethods(user.ID)
	}
	if s.synonLinkAuth != nil && s.synonLinkAuth.enabled && user.ID == s.synonLinkAuth.user().ID &&
		!slices.Contains(methods, "local") {
		methods = append(methods, "local")
	}
	slices.Sort(methods)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"managed":      true,
		"loginMethods": methods,
		"sessions":     sessions,
		"events":       s.webSessions.Events(user.ID, 20),
	})
}

func (s *Server) clearWebAuthCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{webSessionCookieName, webCSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			Expires: time.Unix(1, 0).UTC(), HttpOnly: name != webCSRFCookieName,
			Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
		})
	}
}
